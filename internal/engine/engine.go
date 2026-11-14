// Package engine loads workflow definitions (YAML data, no code), validates them against the
// block registry, and runs them step by step. Blocks are compiled in and self-register.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"gopkg.in/yaml.v3"

	"github.com/02david20/Sentinel/internal/store"
)

// Effect classifies what a block may change.
type Effect string

const (
	Read          Effect = "read"
	WriteInternal Effect = "write-internal"
	WriteExternal Effect = "write-external"
)

// Spec describes a block to the validator and to `sentinel blocks`.
type Spec struct {
	Name    string            `json:"name"`
	Effect  Effect            `json:"effect"`
	Inputs  map[string]string `json:"inputs"`  // name → type (string|number|bool|list|map|any); "?" suffix = optional
	Outputs map[string]string `json:"outputs"` // name → type
	Doc     string            `json:"doc"`
	Source  string            `json:"source,omitempty"` // "" for compiled-in blocks, "external" for loaded ones
}

// Env is what a block sees while it runs.
type Env struct {
	Ctx      context.Context
	Workflow string
	Store    *store.Store
	StateDir string         // where task files and logs live
	Planned  map[string]any // dry run only: what earlier steps would have created, for later steps to show
	Dry      bool
	Services map[string]any // connectors, set up by the CLI (webhook, ...)
	Log      func(format string, a ...any)
}

// Options are the run-time dependencies of Run.
type Options struct {
	Store    *store.Store
	StateDir string
	Services map[string]any
	Dry      bool
	Log      func(format string, a ...any)
	// OnAuthFailure runs once after a login failure paused the workflow, so the caller can tell
	// the user how to fix it (a scheduled run has nobody watching the terminal).
	OnAuthFailure func(workflow string, err error)
}

// AmbiguousError marks a failed external write whose outcome is unknown (a timeout after the
// request left). Its idempotency key stays claimed: repeating the write could duplicate it.
type AmbiguousError struct{ Err error }

func (e AmbiguousError) Error() string { return e.Err.Error() + " (outcome unknown; not retried)" }
func (e AmbiguousError) Unwrap() error { return e.Err }

type Block interface {
	Spec() Spec
	// Run gets resolved inputs. A write-external block must honour env.Dry (plan only).
	Run(env *Env, in map[string]any) (map[string]any, error)
}

var registry = map[string]Block{}

func Register(b Block) { registry[b.Spec().Name] = b }

// IsRegistered reports whether a block of that name exists.
func IsRegistered(name string) bool { _, ok := registry[name]; return ok }

// IsBuiltin reports whether a compiled-in block (not a loaded plugin) has that name.
func IsBuiltin(name string) bool {
	b, ok := registry[name]
	return ok && b.Spec().Source != "external"
}

// ZeroOutputs returns a zero value for every declared output, for steps that did not run.
func ZeroOutputs(outs map[string]string) map[string]any { return zeroOutputs(outs) }

// Func turns a function into a Block, so a compiled-in block needs no type of its own:
//
//	engine.Register(engine.Func(engine.Spec{Name: "My.Block", ...}, func(env *engine.Env, in map[string]any) (map[string]any, error) {...}))
func Func(spec Spec, run func(env *Env, in map[string]any) (map[string]any, error)) Block {
	return funcBlock{spec, run}
}

type funcBlock struct {
	spec Spec
	run  func(env *Env, in map[string]any) (map[string]any, error)
}

func (f funcBlock) Spec() Spec { return f.spec }
func (f funcBlock) Run(env *Env, in map[string]any) (map[string]any, error) {
	return f.run(env, in)
}

func Blocks() []Spec {
	var out []Spec
	for _, b := range registry {
		out = append(out, b.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- workflow definition ----

type Step struct {
	ID    string         `yaml:"id" json:"id"`
	Block string         `yaml:"block" json:"block"`
	In    map[string]any `yaml:"in" json:"in"`
	When  string         `yaml:"when,omitempty" json:"when,omitempty"` // expression over step outputs; false = skip
	Key   string         `yaml:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
}

type Workflow struct {
	Name       string   `yaml:"name" json:"name"`
	Every      string   `yaml:"every,omitempty" json:"every,omitempty"`       // "4h", "30m", "1d": the easy way to say how often
	Schedule   string   `yaml:"schedule,omitempty" json:"schedule,omitempty"` // advanced: a systemd OnCalendar expression
	Summary    string   `yaml:"summary,omitempty" json:"summary,omitempty"`   // one sentence about a finished run, with ${step.output} references
	WriteAllow []string `yaml:"write_allow,omitempty" json:"write_allow,omitempty"`
	Steps      []Step   `yaml:"steps" json:"steps"`
}

// Package is a frozen, hashed workflow.
type Package struct {
	Workflow Workflow
	Hash     string
}

func Load(data []byte) (*Package, error) {
	var w Workflow
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	canon, _ := json.Marshal(w)
	sum := sha256.Sum256(append(canon, versions()...))
	return &Package{Workflow: w, Hash: hex.EncodeToString(sum[:])[:16]}, nil
}

// versions pins the block set into the hash: a block change is a different package.
func versions() []byte {
	b, _ := json.Marshal(Blocks())
	return b
}

var calendarRe = regexp.MustCompile(`^[A-Za-z0-9*:/.,~ -]{1,80}$`) // no newlines or other control characters

var everyRe = regexp.MustCompile(`^([1-9][0-9]*)([mhd])$`)

// EveryToCalendar turns "4h" into a systemd OnCalendar expression and a phrase ("every 4 hours").
func EveryToCalendar(every string) (calendar, phrase string, err error) {
	m := everyRe.FindStringSubmatch(strings.TrimSpace(every))
	if m == nil {
		return "", "", fmt.Errorf("every %q: write a number and m, h or d, for example 30m, 4h or 1d", every)
	}
	n := 0
	fmt.Sscanf(m[1], "%d", &n)
	switch m[2] {
	case "m":
		if n > 59 || 60%n != 0 {
			return "", "", fmt.Errorf("every %q: minutes must divide 60 evenly (1, 2, 5, 10, 15, 20, 30)", every)
		}
		return fmt.Sprintf("*-*-* *:0/%d:00", n), plural("every", n, "minute"), nil
	case "h":
		if n > 23 || 24%n != 0 {
			return "", "", fmt.Errorf("every %q: hours must divide 24 evenly (1, 2, 3, 4, 6, 8, 12)", every)
		}
		return fmt.Sprintf("*-*-* 00/%d:00:00", n), plural("every", n, "hour"), nil
	}
	if n != 1 {
		return "", "", fmt.Errorf("every %q: for days use 1d (daily)", every)
	}
	return "*-*-* 00:00:00", "every day", nil
}

func plural(prefix string, n int, unit string) string {
	if n == 1 {
		return prefix + " " + unit
	}
	return fmt.Sprintf("%s %d %ss", prefix, n, unit)
}

// Calendar returns the OnCalendar expression of a workflow and a human phrase for it.
func (w Workflow) Calendar() (calendar, phrase string, err error) {
	switch {
	case w.Every != "":
		return EveryToCalendar(w.Every)
	case w.Schedule != "":
		return w.Schedule, "on schedule " + w.Schedule, nil
	}
	return "", "", fmt.Errorf("workflow %s has no `every` or `schedule`", w.Name)
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

var refRe = regexp.MustCompile(`^\$\{([a-zA-Z0-9_-]+)\.([a-zA-Z0-9_.-]+)\}$`)

// Validate returns every problem found, so the author can fix them in one pass.
func Validate(w Workflow) []string {
	var errs []string
	if w.Every != "" && w.Schedule != "" {
		errs = append(errs, "use either `every` or `schedule`, not both")
	}
	if w.Every != "" {
		if _, _, err := EveryToCalendar(w.Every); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if w.Schedule != "" && !calendarRe.MatchString(w.Schedule) {
		errs = append(errs, fmt.Sprintf("schedule %q is not a systemd calendar expression (for a simple interval write `every: 4h`)", w.Schedule))
	}
	if w.Name == "" {
		errs = append(errs, "name is required")
	} else if !nameRe.MatchString(w.Name) {
		errs = append(errs, "name must start with a letter or digit and use only letters, digits, - and _ (it names files and the timer)")
	}
	if len(w.Steps) == 0 {
		errs = append(errs, "at least one step is required")
	}
	allow := map[string]bool{}
	for _, a := range w.WriteAllow {
		allow[a] = true
	}
	outputs := map[string]map[string]string{}
	seen := map[string]bool{}
	unknown := map[string]bool{} // steps whose block does not exist: references to them are not reported again
	for i, st := range w.Steps {
		at := fmt.Sprintf("step %d (%s)", i+1, st.ID)
		if st.ID == "" || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(st.ID) {
			errs = append(errs, at+": id must match [a-zA-Z0-9_-]+")
			continue
		}
		if seen[st.ID] {
			errs = append(errs, at+": duplicate id")
		}
		seen[st.ID] = true
		b, ok := registry[st.Block]
		if !ok {
			errs = append(errs, at+": unknown block "+st.Block)
			unknown[st.ID] = true
			continue
		}
		sp := b.Spec()
		outputs[st.ID] = sp.Outputs
		for name, typ := range sp.Inputs {
			if _, given := st.In[name]; !given && !strings.HasSuffix(typ, "?") {
				errs = append(errs, fmt.Sprintf("%s: missing input %q", at, name))
			}
		}
		for name, v := range st.In {
			typ, known := sp.Inputs[name]
			if !known {
				errs = append(errs, fmt.Sprintf("%s: unknown input %q for %s", at, name, st.Block))
				continue
			}
			if ref, isRef := v.(string); isRef && refRe.MatchString(ref) {
				m := refRe.FindStringSubmatch(ref)
				outs, prior := outputs[m[1]]
				if unknown[m[1]] {
					continue
				}
				if !prior || m[1] == st.ID {
					errs = append(errs, fmt.Sprintf("%s: %q refers to a step that does not run before it", at, ref))
					continue
				}
				top := strings.SplitN(m[2], ".", 2)[0]
				got, has := outs[top]
				if !has {
					errs = append(errs, fmt.Sprintf("%s: step %s has no output %q", at, m[1], top))
				} else if !compatible(strings.TrimSuffix(typ, "?"), got) {
					errs = append(errs, fmt.Sprintf("%s: input %q wants %s but %s.%s is %s", at, name, strings.TrimSuffix(typ, "?"), m[1], top, got))
				}
			} else {
				if !literalFits(strings.TrimSuffix(typ, "?"), v) {
					errs = append(errs, fmt.Sprintf("%s: input %q wants %s", at, name, strings.TrimSuffix(typ, "?")))
				}
				errs = append(errs, checkRefs(v, at+" input "+name, outputs, st.ID)...)
			}
		}
		errs = append(errs, checkRefs(st.Key, at+" idempotency_key", outputs, st.ID)...)
		if st.When != "" {
			env := map[string]any{}
			for id, outs := range outputs {
				if id != st.ID {
					env[id] = zeroOutputs(outs)
				}
			}
			if prog, cerr := expr.Compile(st.When, expr.Env(env)); cerr != nil {
				errs = append(errs, fmt.Sprintf("%s: when %q: %v", at, st.When, cerr))
			} else if r, rerr := expr.Run(prog, env); rerr == nil && r != nil {
				// Outputs are typed only as maps, so the compiler cannot see a non-boolean result.
				// Evaluating once on zero values can (errors here, such as an empty list, are not
				// problems of the workflow and are ignored).
				if _, isBool := r.(bool); !isBool {
					errs = append(errs, fmt.Sprintf("%s: when %q must be true or false, not %T", at, st.When, r))
				}
			}
		}
		if sp.Effect == WriteExternal {
			if st.Key == "" {
				errs = append(errs, at+": external write needs idempotency_key")
			}
			if len(w.WriteAllow) > 0 && !allow[st.Block] {
				errs = append(errs, at+": block "+st.Block+" is not in write_allow")
			}
		}
		if sp.Effect == WriteInternal && len(w.WriteAllow) > 0 && !allow[st.Block] {
			errs = append(errs, at+": block "+st.Block+" is not in write_allow")
		}
	}
	return errs
}

func compatible(want, got string) bool { return want == "any" || got == "any" || want == got }

func literalFits(typ string, v any) bool {
	switch typ {
	case "any":
		return true
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case int, int64, float64:
			return true
		}
		return false
	case "bool":
		_, ok := v.(bool)
		return ok
	case "list":
		_, ok := v.([]any)
		return ok
	case "map":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

// ---- running ----

type StepResult struct {
	ID      string         `json:"id"`
	Block   string         `json:"block"`
	Status  string         `json:"status"`         // ran | skipped | failed | planned
	Plan    string         `json:"plan,omitempty"` // dry run: what a real run would do, in words
	Note    string         `json:"note,omitempty"` // real run: what the block did, in words
	Error   string         `json:"error,omitempty"`
	Millis  int64          `json:"ms"`
	Outputs map[string]any `json:"outputs,omitempty"`
}

type Result struct {
	RunID   int64
	Status  string // ok | failed | auth-failed | paused | blocked
	Summary string
	Steps   []StepResult
}

// ErrAuth is returned by blocks (wrapping their connector's auth error) to pause the workflow.
type AuthError struct{ Err error }

func (e AuthError) Error() string { return e.Err.Error() }
func (e AuthError) Unwrap() error { return e.Err }

func Run(ctx context.Context, p *Package, o Options) (*Result, error) {
	w := p.Workflow
	st, dry, logf := o.Store, o.Dry, o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if errs := Validate(w); len(errs) > 0 {
		return nil, fmt.Errorf("invalid workflow:\n  %s", strings.Join(errs, "\n  "))
	}
	res := &Result{}
	if paused, why, err := st.Paused(w.Name); err != nil {
		return nil, err
	} else if paused && !dry {
		res.Status, res.Summary = "paused", "workflow is paused: "+why
		return res, nil
	}
	if !dry { // a dry run only reads, so it may overlap a real run
		unlock, got, lerr := st.LockWorkflow(w.Name)
		if lerr != nil {
			return nil, lerr
		}
		if !got {
			res.Status, res.Summary = "blocked", "another run of this workflow is in progress"
			return res, nil
		}
		defer unlock()
		if n, rerr := st.ReapCrashed(w.Name); rerr != nil {
			return nil, rerr
		} else if n > 0 {
			logf("recovered %d earlier run(s) that never finished", n)
		}
	}
	id, err := st.StartRun(w.Name, p.Hash, dry)
	if err != nil {
		return nil, err
	}
	res.RunID = id
	// Every note is also written to the run's log, as it happens.
	event := func(step, kind, text string) { _ = st.AddEvent(id, step, kind, text) }
	runLog := func(f string, a ...any) {
		logf(f, a...)
		event("", "note", fmt.Sprintf(f, a...))
	}
	env := &Env{Ctx: ctx, Workflow: w.Name, Store: st, StateDir: o.StateDir, Dry: dry, Services: o.Services, Log: runLog, Planned: map[string]any{}}
	kind := "run"
	if dry {
		kind = "dry run (nothing is sent or saved)"
	}
	event("", "run", fmt.Sprintf("started: %s, package %s", kind, p.Hash))
	scope := map[string]map[string]any{}
	finish := func(status, summary string) (*Result, error) {
		res.Status, res.Summary = status, summary
		event("", "run", strings.TrimSpace("finished: "+status+". "+summary))
		return res, st.EndRun(id, status, summary, res.Steps, 0)
	}
	var claimedKey string // the key of the write in flight, freed if the block panics
	defer func() {
		if r := recover(); r != nil {
			if claimedKey != "" {
				_ = st.ReleaseKey(w.Name, claimedKey)
			}
			_ = st.EndRun(id, "crashed", fmt.Sprint("panic: ", r), res.Steps, 0)
			panic(r)
		}
	}()
	for _, step := range w.Steps {
		b := registry[step.Block]
		sp := b.Spec()
		sr := StepResult{ID: step.ID, Block: step.Block}
		if step.When != "" {
			ok, werr := evalWhen(step.When, scope)
			if werr != nil {
				sr.Status, sr.Error = "failed", werr.Error()
				res.Steps = append(res.Steps, sr)
				event(step.ID, "error", "condition could not be evaluated: "+werr.Error())
				return finish("failed", step.ID+": "+werr.Error())
			}
			if !ok {
				sr.Status = "skipped"
				sr.Error = ""
				event(step.ID, "step", "skipped: condition is false ("+step.When+")")
				res.Steps = append(res.Steps, sr)
				scope[step.ID] = zeroOutputs(sp.Outputs) // later steps may still read it
				continue
			}
		}
		in, rerr := resolve(step.In, scope)
		if rerr != nil {
			sr.Status, sr.Error = "failed", rerr.Error()
			res.Steps = append(res.Steps, sr)
			event(step.ID, "error", rerr.Error())
			return finish("failed", step.ID+": "+rerr.Error())
		}
		claimed := false
		if sp.Effect == WriteExternal {
			key, kerr := interpolate(step.Key, scope)
			if kerr != nil {
				sr.Status, sr.Error = "failed", kerr.Error()
				res.Steps = append(res.Steps, sr)
				return finish("failed", step.ID+": "+kerr.Error())
			}
			if dry {
				if used, uerr := st.KeyUsed(w.Name, step.ID+"|"+key); uerr == nil && used {
					sr.Status = "skipped"
					sr.Error = "already done for this event (idempotency key used): " + key
					event(step.ID, "step", "skipped: "+sr.Error)
					res.Steps = append(res.Steps, sr)
					scope[step.ID] = zeroOutputs(sp.Outputs)
					continue
				}
			} else {
				first, cerr := st.ClaimKey(w.Name, step.ID+"|"+key, id)
				if cerr != nil {
					return finish("failed", cerr.Error())
				}
				if !first {
					sr.Status = "skipped"
					sr.Error = "idempotency key already used: " + key
					event(step.ID, "step", "skipped: already done for this event ("+key+")")
					res.Steps = append(res.Steps, sr)
					scope[step.ID] = zeroOutputs(sp.Outputs)
					continue
				}
				claimed = true
				claimedKey = step.ID + "|" + key
				env.Log("claimed key %s|%s", step.ID, key)
			}
			in["_idempotency_key"] = key
		}
		event(step.ID, "step", "started: "+step.Block)
		t0 := time.Now()
		out, runErr := b.Run(env, in)
		sr.Millis = time.Since(t0).Milliseconds()
		if runErr != nil {
			var amb AmbiguousError
			if claimed && errors.As(runErr, &amb) {
				_ = st.MarkDone(w.Name, claimedKey) // may have gone out: never repeat it blindly
			} else if claimed {
				_ = st.ReleaseKey(w.Name, claimedKey) // a failed write may be retried
			}
			claimedKey = ""
			sr.Status, sr.Error = "failed", runErr.Error()
			res.Steps = append(res.Steps, sr)
			event(step.ID, "error", fmt.Sprintf("failed after %d ms: %s", sr.Millis, runErr.Error()))
			var ae AuthError
			if asAuth(runErr, &ae) {
				if !dry {
					_ = st.SetPaused(w.Name, true, "authentication failed: "+runErr.Error())
					if o.OnAuthFailure != nil {
						o.OnAuthFailure(w.Name, runErr)
					}
				}
				return finish("auth-failed", step.ID+": "+runErr.Error())
			}
			return finish("failed", step.ID+": "+runErr.Error())
		}
		if skipped, _ := out["_skipped"].(bool); skipped && claimed {
			// The block did not act (e.g. an escalation is still pending): free the key so the
			// same event is tried again on a later run instead of being lost.
			_ = st.ReleaseKey(w.Name, claimedKey)
			delete(out, "_skipped")
		} else if claimed {
			_ = st.MarkDone(w.Name, claimedKey)
		}
		claimedKey = ""
		sr.Status = "ran"
		if dry && sp.Effect == WriteExternal {
			sr.Status = "planned"
		}
		if v, ok := out["_plan"].(string); ok {
			sr.Plan = v
		}
		if v, ok := out["_note"].(string); ok {
			sr.Note = v
		}
		for k := range out { // underscore keys are for the runner, not for later steps
			if strings.HasPrefix(k, "_") {
				delete(out, k)
			}
		}
		sr.Outputs = summarize(out)
		scope[step.ID] = out
		res.Steps = append(res.Steps, sr)
		detail := sr.Note
		if dry && sr.Plan != "" {
			detail = "would " + sr.Plan
		}
		event(step.ID, "step", strings.TrimSpace(fmt.Sprintf("%s in %d ms. %s", sr.Status, sr.Millis, detail)))
	}
	headline := ""
	if w.Summary != "" {
		headline, _ = interpolate(w.Summary, scope)
	}
	return finish("ok", headline)
}

// zeroOutputs gives every declared output its zero value. A skipped step's outputs are read as
// zero (0, "", false, empty list) instead of failing the steps that mention them, and the same
// shape types the `when` expressions at validation time.
func zeroOutputs(outs map[string]string) map[string]any {
	m := make(map[string]any, len(outs))
	for k, typ := range outs {
		switch typ {
		case "number":
			m[k] = float64(0)
		case "string":
			m[k] = ""
		case "bool":
			m[k] = false
		case "list":
			m[k] = []any{}
		case "map":
			m[k] = map[string]any{}
		default:
			m[k] = nil
		}
	}
	return m
}

// RunBlock runs one registered block directly. For tools and tests; workflows use Run.
func RunBlock(env *Env, name string, in map[string]any) (map[string]any, error) {
	b, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown block %q", name)
	}
	return b.Run(env, in)
}
