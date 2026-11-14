package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/engine"
	"sentinel/internal/store"
)

func extBlockDir(t *testing.T, s *Store, name, effect, extraManifest, script string) {
	t.Helper()
	d := filepath.Join(s.PendingDir(), name)
	must(t, os.MkdirAll(d, 0o755))
	m := "name: " + name + "\ndoc: d\neffect: " + effect + "\ninputs: { text: \"string?\" }\noutputs: { result: string, n: number }\nrun: [./b.sh]\n" + extraManifest
	must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte(m), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "b.sh"), []byte("#!/bin/sh\n"+script), 0o755))
	approve(t, s, name)
	_, err := s.Register()
	must(t, err)
}

func openDB(t *testing.T) *store.Store {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	return st
}

func wf(name, block, extra string) *engine.Package {
	p, err := engine.Load([]byte("name: w1\nsteps:\n  - id: s\n    block: " + block + "\n    idempotency_key: \"k-1\"\n    in: { text: hi }\n" + extra))
	if err != nil {
		panic(err)
	}
	return p
}

// A cancelled (Ctrl-C / SIGTERM) real run while a write-external plugin is in flight: outcome is
// unknown, so the key must stay claimed. Timeout is handled that way; cancellation is not.
func TestCancelledExternalWriteKeepsKey(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(t.TempDir(), "sent")
	extBlockDir(t, s, "Qa.Cancel", "write-external", "", "cat >/dev/null; touch "+marker+"; sleep 5; printf '{}'")
	st := openDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { // cancel once the block is really running (starting it can be slow under -race)
		for i := 0; i < 200; i++ {
			if _, e := os.Stat(marker); e == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res, err := engine.Run(ctx, wf("w1", "Qa.Cancel", ""), engine.Options{Store: st, StateDir: t.TempDir()})
	must(t, err)
	t.Logf("status=%s summary=%s", res.Status, res.Summary)
	if _, e := os.Stat(marker); e != nil {
		t.Fatal("block never started")
	}
	used, _ := st.KeyUsed("w1", "s|k-1")
	if !used {
		t.Fatalf("key released after cancel although the external write already happened: a rerun repeats it (summary %q)", res.Summary)
	}
	if strings.Contains(res.Summary, "timed out") {
		t.Errorf("cancellation reported as timeout: %q", res.Summary)
	}
}

func TestTimeoutExternalWriteKeepsKey(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Timeout", "write-external", "timeout: 1\n", "cat >/dev/null; sleep 5")
	st := openDB(t)
	res, err := engine.Run(context.Background(), wf("w1", "Qa.Timeout", ""), engine.Options{Store: st, StateDir: t.TempDir()})
	must(t, err)
	if res.Status != "failed" {
		t.Fatal(res.Status)
	}
	if used, _ := st.KeyUsed("w1", "s|k-1"); !used {
		t.Fatal("timeout must keep key")
	}
}

func TestSupportsDryGetsDryAndNeverClaimsKey(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Dry", "write-external", "supports_dry: true\n",
		`in=$(cat); case "$in" in *'"dry":true'*) printf '{"plan":"would send","outputs":{"result":"p"}}';; *) printf '{"note":"sent","outputs":{"result":"r"}}';; esac`)
	st := openDB(t)
	o := engine.Options{Store: st, StateDir: t.TempDir()}
	o.Dry = true
	res, err := engine.Run(context.Background(), wf("w1", "Qa.Dry", ""), o)
	must(t, err)
	if res.Steps[0].Status != "planned" || res.Steps[0].Plan != "would send" {
		t.Fatalf("%+v", res.Steps[0])
	}
	if used, _ := st.KeyUsed("w1", "s|k-1"); used {
		t.Fatal("dry run claimed the key")
	}
	o.Dry = false
	r1, _ := engine.Run(context.Background(), wf("w1", "Qa.Dry", ""), o)
	r2, _ := engine.Run(context.Background(), wf("w1", "Qa.Dry", ""), o)
	if r1.Steps[0].Status != "ran" || r2.Steps[0].Status != "skipped" {
		t.Fatalf("idempotency: %s then %s", r1.Steps[0].Status, r2.Steps[0].Status)
	}
	// dry run after key used must say skipped
	o.Dry = true
	r3, _ := engine.Run(context.Background(), wf("w1", "Qa.Dry", ""), o)
	if r3.Steps[0].Status != "skipped" {
		t.Fatalf("dry after used: %s", r3.Steps[0].Status)
	}
}

func TestSkippedReplyFreesKey(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Skip", "write-external", "", `cat >/dev/null; printf '{"skipped":true}'`)
	st := openDB(t)
	res, err := engine.Run(context.Background(), wf("w1", "Qa.Skip", ""), engine.Options{Store: st, StateDir: t.TempDir()})
	must(t, err)
	if used, _ := st.KeyUsed("w1", "s|k-1"); used {
		t.Fatalf("skipped block must free key: %+v", res.Steps)
	}
}

func TestFailedExternalWriteFreesKey(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Fail", "write-external", "", `cat >/dev/null; echo nope >&2; exit 3`)
	st := openDB(t)
	res, _ := engine.Run(context.Background(), wf("w1", "Qa.Fail", ""), engine.Options{Store: st, StateDir: t.TempDir()})
	if res.Status != "failed" || !strings.Contains(res.Summary, "nope") {
		t.Fatal(res.Status, res.Summary)
	}
	if used, _ := st.KeyUsed("w1", "s|k-1"); used {
		t.Fatal("failed write must free key")
	}
}

// __pycache__ and *.pyc are excluded from the approval hash, yet python executes them.
func TestWrongOutputTypeFromBlock(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Type", "read", "", `cat >/dev/null; printf '{"outputs":{"result":5,"n":"abc"}}'`)
	st := openDB(t)
	p, err := engine.Load([]byte("name: w1\nsteps:\n  - id: s\n    block: Qa.Type\n    in: {}\n  - id: t\n    block: Qa.Type\n    when: \"s.n > 0\"\n    in: {}\n"))
	must(t, err)
	res, err := engine.Run(context.Background(), p, engine.Options{Store: st, StateDir: t.TempDir()})
	must(t, err)
	t.Logf("status=%s summary=%s out=%v", res.Status, res.Summary, res.Steps[0].Outputs)
	if res.Steps[0].Status == "ran" {
		t.Errorf("block returned number as string and string as number; engine accepted it: %v", res.Steps[0].Outputs)
	}
}

func TestFolderNameMismatchAndBadManifests(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.Root, "Qa.Real", "", okScript)
	must(t, os.Rename(d, filepath.Join(s.Root, "Other.Name")))
	infos, err := s.List()
	must(t, err)
	if len(infos) != 1 || infos[0].State != StateInvalid {
		t.Fatalf("%+v", infos)
	}
	// run argv with empty string, negative timeout etc.
	m := &Manifest{Name: "A.B", Effect: engine.Read, Doc: "x", Run: []string{""}}
	if len(m.Validate()) == 0 {
		t.Fatal("empty run accepted")
	}
}

func TestCorruptApprovalsFileDoesNotCrashAndBlocksNothingApproved(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.Root, "Qa.C", "", okScript)
	must(t, os.WriteFile(s.ApprovalsPath, []byte("{not json"), 0o600))
	if _, err := s.List(); err == nil {
		t.Fatal("expected error")
	}
	if err := s.Approve("Qa.C", "deadbeef"); err == nil {
		t.Fatal("approve over a corrupt approvals file must not silently succeed/overwrite")
	}
}

func TestOversizedAndGarbageReplies(t *testing.T) {
	s := newStore(t)
	extBlockDir(t, s, "Qa.Big", "read", "", `cat >/dev/null; head -c 5000000 /dev/zero | tr '\0' 'a'`)
	_, err := run(t, "Qa.Big", false)
	if err == nil || strings.Contains(err.Error(), strings.Repeat("a", 200)) {
		t.Fatalf("err=%v", err)
	}
	extBlockDir(t, s, "Qa.Two", "read", "", `cat >/dev/null; printf '{"outputs":{}} {"outputs":{}}'`)
	if _, err := run(t, "Qa.Two", false); err == nil {
		t.Error("two JSON documents accepted")
	}
}
