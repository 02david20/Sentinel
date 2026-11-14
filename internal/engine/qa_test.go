package engine_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/02david20/Sentinel/internal/engine"
	"github.com/02david20/Sentinel/internal/store"
)

type qaBlock struct {
	name   string
	effect engine.Effect
	fn     func(env *engine.Env, in map[string]any) (map[string]any, error)
}

func (b qaBlock) Spec() engine.Spec {
	return engine.Spec{Name: b.name, Effect: b.effect, Inputs: map[string]string{"text": "string?", "n": "number?"},
		Outputs: map[string]string{"count": "number", "token": "string", "ok": "bool"}}
}
func (b qaBlock) Run(env *engine.Env, in map[string]any) (map[string]any, error) {
	return b.fn(env, in)
}

var sent int
var gate = make(chan struct{})
var entered = make(chan struct{}, 4)

func init() {
	engine.Register(qaBlock{"QA.Read", engine.Read, func(*engine.Env, map[string]any) (map[string]any, error) {
		return map[string]any{"count": float64(1), "token": "t", "ok": true}, nil
	}})
	engine.Register(qaBlock{"QA.Ext", engine.WriteExternal, func(*engine.Env, map[string]any) (map[string]any, error) {
		sent++
		return map[string]any{"ok": true}, nil
	}})
	engine.Register(qaBlock{"QA.PanicExt", engine.WriteExternal, func(*engine.Env, map[string]any) (map[string]any, error) {
		panic("kill -9 equivalent between claim and write")
	}})
	engine.Register(qaBlock{"QA.Slow", engine.Read, func(*engine.Env, map[string]any) (map[string]any, error) {
		entered <- struct{}{}
		<-gate
		return map[string]any{"count": float64(1)}, nil
	}})
}

func open(t *testing.T) *store.Store {
	s, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func load(t *testing.T, y string) *engine.Package {
	p, err := engine.Load([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// FR-6: one run per workflow at a time.
func TestQA_NoRunLock(t *testing.T) {
	s := open(t)
	p := load(t, "name: w\nsteps:\n  - {id: a, block: QA.Slow, in: {}}\n")
	var wg sync.WaitGroup
	var r1, r2 *engine.Result
	wg.Add(1)
	go func() { defer wg.Done(); r1, _ = engine.Run(context.Background(), p, engine.Options{Store: s}) }()
	<-entered
	done := make(chan struct{})
	go func() { r2, _ = engine.Run(context.Background(), p, engine.Options{Store: s}); close(done) }()
	select {
	case <-entered: // second run is inside the block while the first still runs
		t.Errorf("two runs of the same workflow executed concurrently (no lock, no 'blocked' status)")
	case <-time.After(500 * time.Millisecond):
	}
	close(gate)
	wg.Wait()
	<-done
	_ = r1
	_ = r2
}

// NFR-6: crash between ClaimKey and the write.
func TestQA_CrashAfterClaimLosesEventAndLeavesRunning(t *testing.T) {
	s := open(t)
	y := "name: w\nsteps:\n  - {id: a, block: QA.PanicExt, idempotency_key: ev1, in: {}}\n"
	func() {
		defer func() { recover() }()
		engine.Run(context.Background(), load(t, y), engine.Options{Store: s})
	}()
	runs, _ := s.Runs("w", 5)
	if len(runs) == 1 && runs[0].Status == "running" {
		t.Errorf("DEFECT: run row left 'running' forever (id %d); nothing reaps it", runs[0].ID)
	}
	// Re-run with a healthy block under the same step id and key: the write must happen.
	before := sent
	y2 := "name: w\nsteps:\n  - {id: a, block: QA.Ext, idempotency_key: ev1, in: {}}\n"
	engine.Run(context.Background(), load(t, y2), engine.Options{Store: s})
	if sent == before {
		t.Errorf("DEFECT: key claimed before a crash is never retried: event lost (at-most-once, no recovery)")
	}
}

// Validate passes but the run fails: when over the output of a skipped step.
func TestQA_WhenOnSkippedStepOutputFailsAtRuntime(t *testing.T) {
	y := "name: w\nsteps:\n  - {id: a, block: QA.Read, when: 'false', in: {}}\n  - {id: b, block: QA.Read, when: 'a.count > 0', in: {}}\n"
	p := load(t, y)
	if errs := engine.Validate(p.Workflow); len(errs) != 0 {
		t.Fatalf("unexpected validate errors %v", errs)
	}
	r, err := engine.Run(context.Background(), p, engine.Options{Store: open(t)})
	if err != nil || r.Status != "ok" {
		t.Errorf("validated workflow fails at run time: status=%v summary=%q err=%v", r.Status, r.Summary, err)
	}
}

// Idempotency key / input referencing a skipped step: Validate ok, run fails.
func TestQA_KeyReferencingSkippedStep(t *testing.T) {
	y := "name: w\nsteps:\n  - {id: a, block: QA.Read, when: 'false', in: {}}\n  - {id: b, block: QA.Ext, idempotency_key: 'k-${a.token}', in: {}}\n"
	p := load(t, y)
	if errs := engine.Validate(p.Workflow); len(errs) != 0 {
		t.Fatalf("validate errors %v", errs)
	}
	r, _ := engine.Run(context.Background(), p, engine.Options{Store: open(t)})
	if r.Status != "ok" {
		t.Errorf("run failed: %s", r.Summary)
	}
}

// A valid interpolation that starts the string is rejected as "bad reference".
func TestQA_ValidateRejectsLeadingInterpolation(t *testing.T) {
	y := "name: w\nsteps:\n  - {id: a, block: QA.Read, in: {}}\n  - {id: b, block: QA.Ext, idempotency_key: k, in: {text: '${a.count} failing'}}\n"
	p := load(t, y)
	if errs := engine.Validate(p.Workflow); len(errs) != 0 {
		t.Errorf("valid interpolation rejected: %v", errs)
	}
}

// Typo'd reference in the key is silently literal: constant key => the write happens once, ever.
func TestQA_TypoKeyIsConstantAndPassesValidate(t *testing.T) {
	y := "name: w\nsteps:\n  - {id: a, block: QA.Read, in: {}}\n  - {id: b, block: QA.Ext, idempotency_key: 'n-${a}', in: {}}\n"
	p := load(t, y)
	if errs := engine.Validate(p.Workflow); len(errs) == 0 {
		t.Errorf("malformed reference ${a} in idempotency_key accepted by Validate (becomes a literal, constant key)")
	}
}

// when returning a non-bool passes Validate.
func TestQA_WhenNonBoolPassesValidate(t *testing.T) {
	y := "name: w\nsteps:\n  - {id: a, block: QA.Read, in: {}}\n  - {id: b, block: QA.Read, when: 'a.count', in: {}}\n"
	p := load(t, y)
	if errs := engine.Validate(p.Workflow); len(errs) == 0 {
		r, _ := engine.Run(context.Background(), p, engine.Options{Store: open(t)})
		t.Errorf("non-bool when accepted by Validate; run: %s %s", r.Status, r.Summary)
	}
}

// Dry-run plans a write that a real run would skip as duplicate.
func TestQA_DryRunIgnoresUsedKey(t *testing.T) {
	s := open(t)
	_, _ = s.ClaimKey("w", "b|k", 0)
	y := "name: w\nsteps:\n  - {id: b, block: QA.Ext, idempotency_key: k, in: {}}\n"
	r, _ := engine.Run(context.Background(), load(t, y), engine.Options{Store: s, Dry: true})
	if r.Steps[0].Status == "planned" {
		t.Errorf("dry-run reports 'planned' for a write the real run would skip (key already used)")
	}
}

func TestQA_EmptyAndHostileYAML(t *testing.T) {
	for name, y := range map[string]string{"empty": "", "null": "null", "alias bomb": "a: &a [x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]\nf: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]\n"} {
		p, err := engine.Load([]byte(y))
		t.Logf("%s: loadErr=%v", name, err)
		if err == nil {
			t.Logf("   validate=%v", engine.Validate(p.Workflow))
		}
	}
}
