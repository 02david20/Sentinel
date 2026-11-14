package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/02david20/Sentinel/internal/engine"
)

const (
	maxReply  = 4 << 20 // bytes of stdout a block may return
	maxStderr = 4 << 10
)

// extBlock adapts a manifest to engine.Block. hash is what the person approved; it is checked
// again right before every execution.
type extBlock struct {
	m    *Manifest
	dir  string
	hash string
}

func (b extBlock) Spec() engine.Spec { return b.m.Spec() }

// request is the single JSON document written to the block's stdin.
type request struct {
	Protocol int            `json:"protocol"`
	Block    string         `json:"block"`
	Workflow string         `json:"workflow"`
	Dry      bool           `json:"dry"`
	Inputs   map[string]any `json:"inputs"`
}

// reply is the single JSON document the block writes to stdout.
type reply struct {
	Outputs map[string]any `json:"outputs"`
	Plan    string         `json:"plan"`
	Note    string         `json:"note"`
	Skipped bool           `json:"skipped"`
	Error   string         `json:"error"`
}

func (b extBlock) Run(env *engine.Env, in map[string]any) (map[string]any, error) {
	m := b.m
	// A block that changes things is not executed in a dry run unless it says it can plan
	// without acting; otherwise a dry run could change the world.
	if env.Dry && m.Effect != engine.Read && !m.SupportsDry {
		out := engine.ZeroOutputs(m.Outputs)
		out["_plan"] = "run external block " + m.Name + " (not executed in a dry run)"
		return out, nil
	}
	// The folder is hashed at load time and again here, so an edit between the two cannot run.
	if b.hash != "" {
		if now, err := HashDir(b.dir); err != nil || now != b.hash {
			return nil, fmt.Errorf("%s changed since it was approved; review it and approve it again (sentinel block show %s)", m.Name, m.Name)
		}
	}
	rep, err := execute(env.Ctx, b.dir, m, request{ProtocolNumber, m.Name, env.Workflow, env.Dry, in}, true)
	if err != nil {
		var te timeoutError
		if m.Effect == engine.WriteExternal && (errors.As(err, &te) || errors.Is(err, context.Canceled)) {
			return nil, engine.AmbiguousError{Err: err} // it may have acted before it was stopped
		}
		return nil, err
	}
	out, err := shapeOutputs(m, rep.Outputs)
	if err != nil {
		return nil, err
	}
	if rep.Plan != "" {
		out["_plan"] = oneLine(rep.Plan, 500)
	}
	if rep.Note != "" {
		out["_note"] = oneLine(rep.Note, 500)
	}
	if rep.Skipped {
		out["_skipped"] = true
	}
	return out, nil
}

// shapeOutputs keeps only declared outputs, fills missing ones with zero values and rejects a
// value of the wrong type (a later step would otherwise fail with a confusing expression error).
func shapeOutputs(m *Manifest, got map[string]any) (map[string]any, error) {
	out := engine.ZeroOutputs(m.Outputs)
	for k, typ := range m.Outputs {
		v, ok := got[k]
		if !ok {
			continue
		}
		if !typeOK(typ, v) {
			return nil, fmt.Errorf("%s returned output %q as %s, but block.yaml declares %s", m.Name, k, jsonKind(v), typ)
		}
		out[k] = v
	}
	return out, nil
}

func typeOK(typ string, v any) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
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
	return true // any
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a bool"
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	}
	return fmt.Sprintf("%T", v)
}

// timeoutError marks a block that ran out of time.
type timeoutError struct{ msg string }

func (t timeoutError) Error() string { return t.msg }

// execute runs the block once and parses its reply. passEnv controls whether the manifest's
// env variables are given to it (`block check` does not, so unreviewed code never sees secrets).
func execute(ctx context.Context, dir string, m *Manifest, req request, passEnv bool) (*reply, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.timeout())*time.Second)
	defer cancel()
	argv := append([]string(nil), m.Run...)
	if strings.Contains(argv[0], "/") {
		argv[0] = filepath.Join(dir, argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv(m, passEnv)
	cmd.SysProcAttr = newGroup()
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	body, _ := json.Marshal(req)
	cmd.Stdin = bytes.NewReader(body)
	// Real pipes (not buffers): Wait then returns when the process exits, and the group is killed
	// right after, so a block cannot leave background processes behind or hold the output open.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, fmt.Errorf("%s could not start (%s): %w", m.Name, argv[0], err)
	}
	outW.Close()
	errW.Close()
	var stdout, stderr bytes.Buffer
	done := make(chan struct{}, 2)
	drain := func(r *os.File, w io.Writer) {
		_, _ = io.Copy(w, r)
		r.Close()
		done <- struct{}{}
	}
	outLimit := &limitWriter{w: &stdout, n: maxReply}
	go drain(outR, outLimit)
	go drain(errR, &limitWriter{w: &stderr, n: maxStderr})
	runErr := cmd.Wait()
	_ = killGroup(cmd) // anything the block left running dies with it
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, timeoutError{fmt.Sprintf("%s timed out after %d s", m.Name, m.timeout())}
		}
		return nil, fmt.Errorf("%s was cancelled: %w", m.Name, err)
	}
	if outLimit.overflow {
		return nil, fmt.Errorf("%s returned more than %d MB on stdout; return less data", m.Name, maxReply>>20)
	}
	var rep reply
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(stdout.Bytes())))
	parseErr := dec.Decode(&rep)
	if parseErr == nil && dec.More() {
		parseErr = errors.New("more than one JSON value")
	}
	if runErr != nil {
		msg := strings.TrimSpace(rep.Error)
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return nil, fmt.Errorf("%s failed: %s", m.Name, oneLine(msg, 400))
	}
	if parseErr != nil {
		return nil, fmt.Errorf("%s did not return one JSON object on stdout (%v): %q", m.Name, parseErr, oneLine(stdout.String(), 120))
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("%s failed: %s", m.Name, oneLine(rep.Error, 400))
	}
	return &rep, nil
}

// scrubbedEnv passes only what a block needs: a minimal PATH and locale, plus the variables its
// manifest lists. Secrets of other blocks (and of Sentinel) are not visible.
func scrubbedEnv(m *Manifest, passEnv bool) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=" + orDefault(os.Getenv("LANG"), "C.UTF-8"), "SENTINEL_BLOCK=" + m.Name}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	// Python would otherwise write bytecode and read the user's site-packages, neither of which
	// the approval covers.
	env = append(env, "PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1")
	if passEnv {
		for _, k := range m.Env {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	return env
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// clean removes control characters (escape sequences) so a block cannot write to the terminal
// through its notes, plans or error messages.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(clean(s)), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// limitWriter keeps the first n bytes and silently drops the rest, so a runaway block cannot
// fill memory. Returning len(p) keeps the child from failing on a closed pipe.
type limitWriter struct {
	w        io.Writer
	n        int
	overflow bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	k := min(len(p), l.n)
	if k > 0 {
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	if k < len(p) {
		l.overflow = true
	}
	return len(p), nil
}
