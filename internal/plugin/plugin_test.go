package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "sentinel/internal/blocks" // registers the built-ins, which plugins must not shadow
	"sentinel/internal/engine"
)

// writeBlock creates <root>/<where>/<name> with a manifest and a /bin/sh script body.
func writeBlock(t *testing.T, dir, name, extra, script string) string {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: " + name + "\ndoc: test block\neffect: read\ninputs: { text: string }\noutputs: { result: string }\nrun: [./b.sh]\n" + extra
	must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte(manifest), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "b.sh"), []byte("#!/bin/sh\n"+script), 0o755))
	return d
}

// approve pins exactly what is on disk, as `sentinel block approve` does after a review.
func approve(t *testing.T, s *Store, name string) {
	t.Helper()
	in, err := s.Find(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := HashDir(in.Dir)
	must(t, err)
	must(t, s.Approve(name, h))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newStore(t *testing.T) *Store {
	root := t.TempDir()
	return Open(root, filepath.Join(t.TempDir(), "approvals.json"))
}

func state(t *testing.T, s *Store, name string) string {
	t.Helper()
	in, err := s.Find(name)
	if err != nil {
		t.Fatal(err)
	}
	return in.State
}

func run(t *testing.T, name string, dry bool) (map[string]any, error) {
	return engine.RunBlock(&engine.Env{Ctx: context.Background(), Workflow: "w", Dry: dry, Log: func(string, ...any) {}}, name, map[string]any{"text": "hi"})
}

const okScript = `cat >/dev/null; printf '{"outputs":{"result":"done"},"note":"n"}'`

func TestPendingBlockIsNotRegisteredUntilApprovedAndModificationRevokesTrust(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.One", "", okScript)
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	if engine.IsRegistered("Plug.One") {
		t.Fatal("a pending block must not be registered")
	}
	approve(t, s, "Plug.One")
	if got := state(t, s, "Plug.One"); got != StateApproved {
		t.Fatalf("state after approve: %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.PendingDir(), "Plug.One")); !os.IsNotExist(err) {
		t.Fatal("approve must move the block out of _pending")
	}
	if _, err := s.Register(); err != nil || !engine.IsRegistered("Plug.One") {
		t.Fatalf("approved block not registered: %v", err)
	}
	out, err := run(t, "Plug.One", false)
	if err != nil || out["result"] != "done" || out["_note"] != "n" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	// Any change to the folder revokes trust.
	must(t, os.WriteFile(filepath.Join(s.Root, "Plug.One", "b.sh"), []byte("#!/bin/sh\nprintf '{}'\n"), 0o755))
	if got := state(t, s, "Plug.One"); got != StateModified {
		t.Fatalf("state after edit: %s", got)
	}
	// A second process (fresh registry view) must not load the modified block.
	infos, _ := s.List()
	for _, in := range infos {
		if in.Name == "Plug.One" && in.State == StateApproved {
			t.Fatal("modified block still approved")
		}
	}
}

func TestApprovalsFileIsSeparateFromTheBlockFolder(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Sep", "", okScript)
	approve(t, s, "Plug.Sep")
	if strings.HasPrefix(s.ApprovalsPath, s.Root) {
		t.Fatal("test setup: approvals should live elsewhere")
	}
	if _, err := os.Stat(s.ApprovalsPath); err != nil {
		t.Fatal("approvals file not written where configured")
	}
}

func TestDryRunNeverExecutesABlockThatChangesThings(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(t.TempDir(), "ran")
	writeBlock(t, s.PendingDir(), "Plug.Write", "", "") // placeholder to get a folder, rewritten below
	d := filepath.Join(s.PendingDir(), "Plug.Write")
	must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte("name: Plug.Write\ndoc: writes\neffect: write-external\ninputs: { text: string }\noutputs: { result: string }\nrun: [./b.sh]\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "b.sh"), []byte("#!/bin/sh\ncat >/dev/null; touch "+marker+"; printf '{\"outputs\":{\"result\":\"x\"}}'\n"), 0o755))
	approve(t, s, "Plug.Write")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "Plug.Write", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("dry run executed a write-external block")
	}
	if p, _ := out["_plan"].(string); !strings.Contains(p, "not executed") {
		t.Fatalf("no plan in dry run: %v", out)
	}
	if _, err := run(t, "Plug.Write", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("a real run did not execute the block")
	}
}

func TestEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("PLUGTEST_SECRET", "s3cret")
	t.Setenv("PLUGTEST_ALLOWED", "ok")
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Env", "env: [PLUGTEST_ALLOWED]\n",
		`cat >/dev/null; printf '{"outputs":{"result":"%s|%s"}}' "${PLUGTEST_SECRET:-none}" "${PLUGTEST_ALLOWED:-none}"`)
	approve(t, s, "Plug.Env")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "Plug.Env", false)
	if err != nil || out["result"] != "none|ok" {
		t.Fatalf("out=%v err=%v (undeclared secrets must not reach a block)", out, err)
	}
}

func TestFailureTimeoutAndBadReply(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Fail", "", `cat >/dev/null; echo "boom reason" >&2; exit 3`)
	writeBlock(t, s.PendingDir(), "Plug.Slow", "timeout: 1\n", `cat >/dev/null; sleep 30`)
	writeBlock(t, s.PendingDir(), "Plug.Junk", "", `cat >/dev/null; echo not-json`)
	writeBlock(t, s.PendingDir(), "Plug.Err", "", `cat >/dev/null; printf '{"error":"upstream said no"}'`)
	for _, n := range []string{"Plug.Fail", "Plug.Slow", "Plug.Junk", "Plug.Err"} {
		approve(t, s, n)
	}
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Plug.Fail": "boom reason", "Plug.Slow": "timed out", "Plug.Junk": "did not return one JSON object", "Plug.Err": "upstream said no"} {
		if _, err := run(t, name, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v, want %q", name, err, want)
		}
	}
}

func TestOutputsAreShapedByTheManifest(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Shape", "", `cat >/dev/null; printf '{"outputs":{"surprise":1}}'`)
	approve(t, s, "Plug.Shape")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "Plug.Shape", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["surprise"]; ok {
		t.Fatal("undeclared output leaked to later steps")
	}
	if v, ok := out["result"]; !ok || v != "" {
		t.Fatalf("declared output missing its zero value: %v", out)
	}
}

func TestBuiltInsCannotBeReplacedAndBadManifestsAreRejected(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.Root, "Snapshot.Commit", "", okScript)
	must(t, saveApprovals(s.ApprovalsPath, Approvals{"Snapshot.Commit": {Hash: mustHash(t, filepath.Join(s.Root, "Snapshot.Commit"))}}))
	infos, err := s.Register()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Name == "Snapshot.Commit" && in.State != StateConflict {
			t.Fatalf("a built-in was shadowed: %s", in.State)
		}
	}
	for _, bad := range []string{
		"name: x y\ndoc: d\neffect: read\nrun: [a]\n",
		"name: Bad.One\ndoc: d\neffect: nuke\nrun: [a]\n",
		"name: Bad.One\ndoc: d\neffect: read\n",
		"name: Bad.One\ndoc: d\neffect: read\nrun: [a]\ninputs: { a: strng }\n",
		"name: Bad.One\ndoc: d\neffect: write-external\nrun: [a]\ntests: [{ name: t }]\n",
		"name: Bad.One\ndoc: d\neffect: read\nrun: [a]\nunknown_field: 1\n",
	} {
		d := filepath.Join(t.TempDir(), "Bad.One")
		must(t, os.MkdirAll(d, 0o755))
		must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte(bad), 0o644))
		if _, err := ParseManifest(d); err == nil {
			t.Errorf("accepted bad manifest:\n%s", bad)
		}
	}
}

func mustHash(t *testing.T, d string) string {
	h, err := HashDir(d)
	must(t, err)
	return h
}

func TestSymlinksAndSpecialFilesAreRefused(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.PendingDir(), "Plug.Link", "", okScript)
	must(t, os.Symlink("/etc/hosts", filepath.Join(d, "evil")))
	if _, err := HashDir(d); err == nil {
		t.Fatal("hashed a block folder containing a symlink")
	}
	if err := s.Approve("Plug.Link", "anything"); err == nil {
		t.Fatal("approved a block folder containing a symlink")
	}
	os.Remove(filepath.Join(d, "evil"))
	must(t, syscall.Mkfifo(filepath.Join(d, "pipe"), 0o600))
	done := make(chan error, 1)
	go func() { _, err := HashDir(d); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hashed a folder containing a FIFO")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO in a block folder hangs the hasher")
	}
}

func TestEveryFileIsCoveredByTheHash(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.PendingDir(), "Plug.All", "", okScript)
	h0 := mustHash(t, d)
	for _, name := range []string{".DS_Store", "x.pyc", "__pycache__/m.cpython-312.pyc", ".git/config"} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755))
		must(t, os.WriteFile(filepath.Join(d, name), []byte("x"), 0o644))
		h1 := mustHash(t, d)
		if h1 == h0 {
			t.Fatalf("adding %s did not change the hash: it could be executed without being reviewed", name)
		}
		h0 = h1
	}
	must(t, os.Chmod(filepath.Join(d, "b.sh"), 0o644))
	if mustHash(t, d) == h0 {
		t.Fatal("the executable bit is not part of the hash")
	}
}

func TestRunCannotLeaveTheBlockFolder(t *testing.T) {
	for _, run := range []string{"[../../x.sh]", "[/bin/sh, -c, id]", "[python3, /tmp/x.py]", "[sh, ../x]", `[""]`} {
		d := filepath.Join(t.TempDir(), "Bad.Run")
		must(t, os.MkdirAll(d, 0o755))
		must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte("name: Bad.Run\ndoc: d\neffect: read\nrun: "+run+"\n"), 0o644))
		if _, err := ParseManifest(d); err == nil {
			t.Errorf("accepted run: %s", run)
		}
	}
}

func TestApproveRefusesAFolderThatChangedAfterTheReview(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.PendingDir(), "Plug.Race", "", okScript)
	reviewed := mustHash(t, d)
	must(t, os.WriteFile(filepath.Join(d, "b.sh"), []byte("#!/bin/sh\necho changed\n"), 0o755))
	if err := s.Approve("Plug.Race", reviewed); err == nil {
		t.Fatal("pinned a folder that differs from the one that was reviewed")
	}
	if got := state(t, s, "Plug.Race"); got == StateApproved {
		t.Fatalf("state %s", got)
	}
}

func TestEditAfterLoadIsRefusedAtExecution(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Late", "", okScript)
	approve(t, s, "Plug.Late")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(s.Root, "Plug.Late", "b.sh"), []byte("#!/bin/sh\ncat >/dev/null; printf '{\"outputs\":{\"result\":\"EVIL\"}}'\n"), 0o755))
	out, err := run(t, "Plug.Late", false)
	if err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("a block edited after load still ran: out=%v err=%v", out, err)
	}
}

func TestBackgroundProcessesDieWithTheBlock(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(t.TempDir(), "alive")
	writeBlock(t, s.PendingDir(), "Plug.Bg", "", "cat >/dev/null; (sleep 2; touch "+marker+") & printf '{\"outputs\":{\"result\":\"x\"}}'")
	approve(t, s, "Plug.Bg")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "Plug.Bg", false)
	if err != nil || out["result"] != "x" {
		t.Fatalf("a block that exits 0 with a valid reply must succeed even with a child holding the pipe: out=%v err=%v", out, err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a background process outlived the block")
	}
}

func TestWrongOutputTypeIsRejectedAndTerminalEscapesAreStripped(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.PendingDir(), "Plug.Type", "", `cat >/dev/null; printf '{"outputs":{"result":7}}'`)
	approve(t, s, "Plug.Type")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "Plug.Type", false); err == nil || !strings.Contains(err.Error(), "declares string") {
		t.Fatalf("wrong output type accepted: %v", err)
	}
	_ = d
	writeBlock(t, s.PendingDir(), "Plug.Esc", "", `cat >/dev/null; printf '{"outputs":{"result":"ok"},"note":"a\u001b[31mred\u001b]0;pwn\u0007"}'`)
	approve(t, s, "Plug.Esc")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "Plug.Esc", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := out["_note"].(string); strings.ContainsRune(n, 0x1b) || strings.ContainsRune(n, 7) {
		t.Fatalf("escape sequences reached the log: %q", n)
	}
}

func TestOversizedReplyHasAClearError(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Big", "", `cat >/dev/null; head -c 5000000 /dev/zero | tr '\0' a`)
	approve(t, s, "Plug.Big")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "Plug.Big", false); err == nil || !strings.Contains(err.Error(), "more than 4 MB") {
		t.Fatalf("err=%v", err)
	}
}

func TestConcurrentApprovalsAreNotLost(t *testing.T) {
	s := newStore(t)
	const n = 8
	hashes := map[string]string{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Plug.C%d", i)
		d := writeBlock(t, s.PendingDir(), name, "", okScript)
		hashes[name] = mustHash(t, d)
	}
	var wg sync.WaitGroup
	for name, h := range hashes {
		wg.Add(1)
		go func(name, h string) { defer wg.Done(); _ = s.Approve(name, h) }(name, h)
	}
	wg.Wait()
	a, err := loadApprovals(s.ApprovalsPath)
	must(t, err)
	if len(a) != n {
		t.Fatalf("%d of %d approvals survived", len(a), n)
	}
}

func TestApprovalsFileThroughASymlinkIsRefused(t *testing.T) {
	s := newStore(t)
	victim := filepath.Join(t.TempDir(), "victim")
	must(t, os.WriteFile(victim, []byte("precious"), 0o644))
	must(t, os.MkdirAll(filepath.Dir(s.ApprovalsPath), 0o755))
	must(t, os.Symlink(victim, s.ApprovalsPath))
	writeBlock(t, s.PendingDir(), "Plug.Sym", "", okScript)
	// Everything fails closed: the symlinked approvals file is neither read nor written.
	if in, err := s.Find("Plug.Sym"); err == nil {
		h, _ := HashDir(in.Dir)
		if err := s.Approve("Plug.Sym", h); err == nil {
			t.Fatal("wrote approvals through a symlink")
		}
	}
	if err := saveApprovals(s.ApprovalsPath, Approvals{}); err == nil {
		t.Fatal("saveApprovals replaced a symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("victim file was overwritten: %q", b)
	}
}

func TestCancelledWriteExternalIsAmbiguous(t *testing.T) {
	s := newStore(t)
	d := filepath.Join(s.PendingDir(), "Plug.Cancel")
	must(t, os.MkdirAll(d, 0o755))
	must(t, os.WriteFile(filepath.Join(d, "block.yaml"), []byte("name: Plug.Cancel\ndoc: w\neffect: write-external\ninputs: { text: string }\noutputs: { result: string }\nrun: [./b.sh]\nsupports_dry: true\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "b.sh"), []byte("#!/bin/sh\ncat >/dev/null; sleep 20\n"), 0o755))
	approve(t, s, "Plug.Cancel")
	if _, err := s.Register(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_, err := engine.RunBlock(&engine.Env{Ctx: ctx, Workflow: "w", Log: func(string, ...any) {}}, "Plug.Cancel", map[string]any{"text": "hi"})
	var amb engine.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("a cancelled external write must be ambiguous (key stays claimed), got %v", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("cancel reported as timeout: %v", err)
	}
}

func TestScaffoldPassesItsOwnCheckAndCheckCatchesBrokenBlocks(t *testing.T) {
	s := newStore(t)
	dir, err := s.Scaffold("Plug.New", "sh", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; scaffold --lang sh needs it")
	}
	in, _ := s.Find("Plug.New")
	for _, r := range Check(context.Background(), in) {
		if !r.OK {
			t.Fatalf("scaffolded block fails its own test: %s: %s", r.Name, r.Why)
		}
	}
	// Break the script: check must fail.
	must(t, os.WriteFile(filepath.Join(dir, "block.sh"), []byte("#!/bin/sh\ncat >/dev/null; printf '{\"outputs\":{\"result\":\"wrong\"}}'\n"), 0o755))
	in, _ = s.Find("Plug.New")
	failed := false
	for _, r := range Check(context.Background(), in) {
		failed = failed || !r.OK
	}
	if !failed {
		t.Fatal("check passed a block that returns the wrong output")
	}
}

func TestCheckRefusesABlockWithNoTests(t *testing.T) {
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.NoTests", "", okScript)
	in, _ := s.Find("Plug.NoTests")
	if r := Check(context.Background(), in); len(r) != 1 || r[0].OK {
		t.Fatalf("a block without tests must not pass: %+v", r)
	}
}

func TestCheckDoesNotHandSecretsToUnreviewedCode(t *testing.T) {
	t.Setenv("PLUGTEST_SECRET2", "topsecret")
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Leak", "env: [PLUGTEST_SECRET2]\ntests:\n  - { name: no secret, in: { text: x }, out: { result: none } }\n",
		`cat >/dev/null; printf '{"outputs":{"result":"%s"}}' "${PLUGTEST_SECRET2:-none}"`)
	in, err := s.Find("Plug.Leak")
	must(t, err)
	for _, r := range Check(context.Background(), in) {
		if !r.OK {
			t.Fatalf("check saw the secret (or failed): %s: %s", r.Name, r.Why)
		}
	}
}
