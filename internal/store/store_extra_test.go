package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A database written by an older build (tables without the later columns) must still open and work.
func TestOldSchemaIsUpgraded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE run (id INTEGER PRIMARY KEY AUTOINCREMENT, workflow TEXT, started TEXT, ended TEXT, status TEXT, summary TEXT);
CREATE TABLE idem (workflow TEXT, key TEXT, at TEXT, PRIMARY KEY (workflow, key));`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.StartRun("w", "h", true); err != nil {
		t.Fatalf("no migration for added columns (dry/pkg_hash/steps/tokens): %v", err)
	}
	if _, err := s.ClaimKey("w", "k", 1); err != nil {
		t.Fatalf("idem: %v", err)
	}
}

func TestLockIsExclusiveAndReleased(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.db"))
	defer s.Close()
	un, ok, err := s.LockWorkflow("w")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	_, ok2, _ := s.LockWorkflow("w")
	if ok2 {
		t.Fatal("second lock granted")
	}
	_, ok3, _ := s.LockWorkflow("other")
	if !ok3 {
		t.Fatal("locks must be per workflow")
	}
	un()
	un2, ok4, _ := s.LockWorkflow("w")
	if !ok4 {
		t.Fatal("not released")
	}
	un2()
}

func TestRunsRetentionAndRunByID(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.db"))
	defer s.Close()
	for i := 0; i < 505; i++ {
		id, _ := s.StartRun("w", "h", false)
		_ = s.AddEvent(id, "", "run", "x")
		_ = s.EndRun(id, "ok", "s", nil, 0)
	}
	rs, _ := s.Runs("w", 1000)
	if len(rs) != 500 {
		t.Fatalf("kept %d", len(rs))
	}
	if _, ok, _ := s.RunByID("w", 1); ok {
		t.Fatal("oldest run should be pruned")
	}
	if _, ok, _ := s.RunByID("other", rs[0].ID); ok {
		t.Fatal("RunByID leaks across workflows")
	}
	if ev, _ := s.Events(1); len(ev) != 0 {
		t.Fatal("events of pruned run kept")
	}
}

func TestReapDoesNotKillLiveDryRun(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.db"))
	defer s.Close()
	dry, _ := s.StartRun("w", "h", true) // a dry run in flight takes no lock
	n, _ := s.ReapCrashed("w")           // a real run starting reaps "crashed" runs
	r, _, _ := s.RunByID("w", dry)
	if n > 0 || r.Status == "crashed" {
		t.Errorf("ReapCrashed marked a live dry run as crashed (status=%s)", r.Status)
	}
}
