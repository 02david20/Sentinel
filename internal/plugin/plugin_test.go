package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	must(t, s.Approve("Plug.One"))
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
	must(t, s.Approve("Plug.Sep"))
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
	must(t, s.Approve("Plug.Write"))
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
	t.Setenv("SENTINEL_TEST_SECRET", "s3cret")
	t.Setenv("SENTINEL_TEST_ALLOWED", "ok")
	s := newStore(t)
	writeBlock(t, s.PendingDir(), "Plug.Env", "env: [SENTINEL_TEST_ALLOWED]\n",
		`cat >/dev/null; printf '{"outputs":{"result":"%s|%s"}}' "${SENTINEL_TEST_SECRET:-none}" "${SENTINEL_TEST_ALLOWED:-none}"`)
	must(t, s.Approve("Plug.Env"))
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
		must(t, s.Approve(n))
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
	must(t, s.Approve("Plug.Shape"))
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
	writeBlock(t, s.Root, "Diff", "", okScript)
	must(t, saveApprovals(s.ApprovalsPath, Approvals{"Diff": {Hash: mustHash(t, filepath.Join(s.Root, "Diff"))}}))
	infos, err := s.Register()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Name == "Diff" && in.State != StateConflict {
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

func TestSymlinksAreRefused(t *testing.T) {
	s := newStore(t)
	d := writeBlock(t, s.PendingDir(), "Plug.Link", "", okScript)
	must(t, os.Symlink("/etc/hosts", filepath.Join(d, "evil")))
	if err := s.Approve("Plug.Link"); err == nil {
		t.Fatal("approved a block folder containing a symlink")
	}
}

func TestScaffoldPassesItsOwnCheckAndCheckCatchesBrokenBlocks(t *testing.T) {
	s := newStore(t)
	dir, err := s.Scaffold("Plug.New", "sh")
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
