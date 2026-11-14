// Package store keeps all runtime state in one SQLite file: keyed snapshots, idempotency keys,
// pending escalations and the run log. Transactions make a killed run safe to repeat.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

func DefaultPath() string {
	d := os.Getenv("XDG_STATE_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".local", "state")
	}
	return filepath.Join(d, "sentinel", "sentinel.db")
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700) // the run log holds step errors and task data
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS snapshot (workflow TEXT, name TEXT, key TEXT, value TEXT, PRIMARY KEY (workflow, name, key));
CREATE TABLE IF NOT EXISTS idem (workflow TEXT, key TEXT, at TEXT, run_id INTEGER DEFAULT 0, done INTEGER DEFAULT 0, PRIMARY KEY (workflow, key));
CREATE TABLE IF NOT EXISTS snapmeta (workflow TEXT, name TEXT, committed_at TEXT, generation INTEGER DEFAULT 0, PRIMARY KEY (workflow, name));
CREATE TABLE IF NOT EXISTS pending (workflow TEXT PRIMARY KEY, token TEXT, queued_at TEXT, status TEXT, task_file TEXT);
CREATE TABLE IF NOT EXISTS workflow (name TEXT PRIMARY KEY, path TEXT, registered TEXT);
CREATE TABLE IF NOT EXISTS flag (workflow TEXT PRIMARY KEY, paused INTEGER, reason TEXT);
CREATE TABLE IF NOT EXISTS run_event (run_id INTEGER, seq INTEGER, at TEXT, step TEXT, kind TEXT, msg TEXT, PRIMARY KEY (run_id, seq));
CREATE TABLE IF NOT EXISTS run (id INTEGER PRIMARY KEY AUTOINCREMENT, workflow TEXT, pkg_hash TEXT, started TEXT, ended TEXT,
  dry INTEGER, status TEXT, summary TEXT, steps TEXT, tokens INTEGER DEFAULT 0);`)
	return err
}

// ---- snapshots (keyed items per workflow and name) ----

// SnapshotMeta is when the snapshot was last committed with touch, and how many commits of any
// kind it has had. The generation changes on every commit, so an event that repeats (an item
// that fails, passes and fails again with the same signature) gets a new idempotency key.
func (s *Store) SnapshotMeta(workflow, name string) (at time.Time, ok bool, generation int64, err error) {
	var v string
	err = s.db.QueryRow(`SELECT committed_at, generation FROM snapmeta WHERE workflow=? AND name=?`, workflow, name).Scan(&v, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, 0, nil
	}
	if err != nil {
		return time.Time{}, false, 0, err
	}
	t, perr := time.Parse(time.RFC3339Nano, v)
	return t, perr == nil, generation, perr
}

func (s *Store) LoadSnapshot(workflow, name string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT key, value FROM snapshot WHERE workflow=? AND name=?`, workflow, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// ReplaceSnapshot swaps the whole snapshot in one transaction. With touch=false the
// committed_at stamp is kept (a removals-only commit must not reset the heartbeat clock); the
// generation always advances.
func (s *Store) ReplaceSnapshot(workflow, name string, items map[string]json.RawMessage, touch bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if !touch {
		var old string
		if err := tx.QueryRow(`SELECT committed_at FROM snapmeta WHERE workflow=? AND name=?`, workflow, name).Scan(&old); err == nil {
			stamp = old
		}
	}
	if _, err := tx.Exec(`DELETE FROM snapshot WHERE workflow=? AND name=?`, workflow, name); err != nil {
		return err
	}
	for k, v := range items {
		if _, err := tx.Exec(`INSERT INTO snapshot VALUES (?,?,?,?)`, workflow, name, k, string(v)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO snapmeta (workflow, name, committed_at, generation) VALUES (?,?,?,1)
		ON CONFLICT(workflow, name) DO UPDATE SET committed_at=excluded.committed_at, generation=generation+1`, workflow, name, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- idempotency ----

// ClaimKey returns true the first time a key is seen for the workflow, false afterwards.
func (s *Store) ClaimKey(workflow, key string, runID int64) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO idem (workflow, key, at, run_id, done) VALUES (?,?,?,?,0)`,
		workflow, key, time.Now().UTC().Format(time.RFC3339), runID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// KeyUsed reports whether a key is already claimed (used by dry runs, which claim nothing).
func (s *Store) KeyUsed(workflow, key string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM idem WHERE workflow=? AND key=?`, workflow, key).Scan(&n)
	return n > 0, err
}

// MarkDone confirms the write behind a claimed key finished, so a later crash cannot undo it.
func (s *Store) MarkDone(workflow, key string) error {
	_, err := s.db.Exec(`UPDATE idem SET done=1 WHERE workflow=? AND key=?`, workflow, key)
	return err
}

// ReleaseKey lets a failed external write be retried.
func (s *Store) ReleaseKey(workflow, key string) error {
	_, err := s.db.Exec(`DELETE FROM idem WHERE workflow=? AND key=?`, workflow, key)
	return err
}

// ---- pause flag (auth failure pauses a workflow until resumed) ----

func (s *Store) SetPaused(workflow string, paused bool, reason string) error {
	p := 0
	if paused {
		p = 1
	}
	_, err := s.db.Exec(`INSERT INTO flag VALUES (?,?,?) ON CONFLICT(workflow) DO UPDATE SET paused=excluded.paused, reason=excluded.reason`, workflow, p, reason)
	return err
}

func (s *Store) Paused(workflow string) (bool, string, error) {
	var p int
	var r sql.NullString
	err := s.db.QueryRow(`SELECT paused, reason FROM flag WHERE workflow=?`, workflow).Scan(&p, &r)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	return p == 1, r.String, err
}

// ---- pending escalation (one per workflow; never auto-cleared) ----

type Pending struct{ Token, QueuedAt, Status, TaskFile string }

func (s *Store) GetPending(workflow string) (*Pending, error) {
	var p Pending
	err := s.db.QueryRow(`SELECT token, queued_at, status, task_file FROM pending WHERE workflow=?`, workflow).
		Scan(&p.Token, &p.QueuedAt, &p.Status, &p.TaskFile)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

// SetPending records a pending escalation. It fails if one already exists.
func (s *Store) SetPending(workflow string, p Pending) error {
	_, err := s.db.Exec(`INSERT INTO pending VALUES (?,?,?,?,?)`, workflow, p.Token, p.QueuedAt, p.Status, p.TaskFile)
	if err != nil {
		return fmt.Errorf("an escalation is already pending for %s", workflow)
	}
	return nil
}

func (s *Store) UpdatePendingStatus(workflow, status string) error {
	_, err := s.db.Exec(`UPDATE pending SET status=? WHERE workflow=?`, status, workflow)
	return err
}

// Ack clears the pending escalation only when the token matches.
func (s *Store) Ack(workflow, token string) error {
	res, err := s.db.Exec(`DELETE FROM pending WHERE workflow=? AND token=?`, workflow, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("token does not match the pending escalation")
	}
	return nil
}

// ---- workflow registry (names that pause/resume/ack/status may refer to) ----

type Workflow struct{ Name, Path string }

func (s *Store) RegisterWorkflow(name, path string) error {
	_, err := s.db.Exec(`INSERT INTO workflow VALUES (?,?,?) ON CONFLICT(name) DO UPDATE SET path=excluded.path`,
		name, path, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) Workflows() ([]Workflow, error) {
	rows, err := s.db.Query(`SELECT name, path FROM workflow ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workflow
	for rows.Next() {
		var w Workflow
		if err := rows.Scan(&w.Name, &w.Path); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) WorkflowPath(name string) (string, bool, error) {
	var p string
	err := s.db.QueryRow(`SELECT path FROM workflow WHERE name=?`, name).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return p, err == nil, err
}

func (s *Store) UnregisterWorkflow(name string) error {
	_, err := s.db.Exec(`DELETE FROM workflow WHERE name=?`, name)
	return err
}

// LastOK is the most recent finished, non-dry run that ended ok (zero value if none).
func (s *Store) LastOK(workflow string) (RunRecord, bool, error) {
	var r RunRecord
	err := s.db.QueryRow(`SELECT id, workflow, pkg_hash, started, COALESCE(ended,''), 0, status, COALESCE(summary,''), '', tokens
		FROM run WHERE workflow=? AND status='ok' AND dry=0 ORDER BY id DESC LIMIT 1`, workflow).
		Scan(&r.ID, &r.Workflow, &r.PkgHash, &r.Started, &r.Ended, new(int), &r.Status, &r.Summary, &r.Steps, &r.Tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	return r, err == nil, err
}

// ---- run lock and crash recovery ----

// LockWorkflow takes the per-workflow run lock. It returns ok=false when another run holds it.
// The lock is an OS file lock, so a killed process releases it. The caller holds the lock for
// the whole run; ReapCrashed is safe to call right after taking it.
func (s *Store) LockWorkflow(workflow string) (unlock func(), ok bool, err error) {
	return lockFile(filepath.Join(filepath.Dir(s.path), "locks", sanitize(workflow)+".lock"))
}

func sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
}

// ReapCrashed marks runs of this workflow still 'running' as crashed (the caller holds the run
// lock, so none can be alive) and frees the keys they claimed but never finished: the write may
// or may not have happened, and for these workflows a repeat is safer than a lost event.
func (s *Store) ReapCrashed(workflow string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM idem WHERE workflow=? AND done=0 AND run_id IN (SELECT id FROM run WHERE workflow=? AND status='running')`, workflow, workflow); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE run SET status='crashed', ended=?, summary='the run never finished (process killed or crashed)' WHERE workflow=? AND status='running'`,
		time.Now().UTC().Format(time.RFC3339), workflow)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// ResumeAuthPaused clears the pause of every workflow that stopped because of a login failure.
func (s *Store) ResumeAuthPaused() ([]string, error) {
	rows, err := s.db.Query(`SELECT workflow FROM flag WHERE paused=1 AND reason LIKE 'authentication failed%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if err := s.SetPaused(n, false, ""); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// ---- run log ----

type RunRecord struct {
	ID                                int64
	Workflow, PkgHash, Started, Ended string
	Dry                               bool
	Status, Summary, Steps            string
	Tokens                            int
}

func (s *Store) StartRun(workflow, hash string, dry bool) (int64, error) {
	d := 0
	if dry {
		d = 1
	}
	res, err := s.db.Exec(`INSERT INTO run (workflow, pkg_hash, started, dry, status) VALUES (?,?,?,?, 'running')`,
		workflow, hash, time.Now().UTC().Format(time.RFC3339), d)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil { // keep the log bounded: the newest 500 runs per workflow
		_, _ = s.db.Exec(`DELETE FROM run WHERE workflow=? AND id NOT IN (SELECT id FROM run WHERE workflow=? ORDER BY id DESC LIMIT 500)`, workflow, workflow)
		_, _ = s.db.Exec(`DELETE FROM run_event WHERE run_id NOT IN (SELECT id FROM run)`)
	}
	return id, err
}

func (s *Store) EndRun(id int64, status, summary string, steps any, tokens int) error {
	b, _ := json.Marshal(steps)
	_, err := s.db.Exec(`UPDATE run SET ended=?, status=?, summary=?, steps=?, tokens=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), status, summary, string(b), tokens, id)
	return err
}

// Event is one line of a run's log, written while the run happens, so a killed run still shows
// how far it got.
type Event struct {
	Seq              int
	At               time.Time
	Step, Kind, Text string // kind: run | step | note | warn | error
}

func (s *Store) AddEvent(runID int64, step, kind, text string) error {
	_, err := s.db.Exec(`INSERT INTO run_event (run_id, seq, at, step, kind, msg)
		VALUES (?, COALESCE((SELECT MAX(seq)+1 FROM run_event WHERE run_id=?), 1), ?, ?, ?, ?)`,
		runID, runID, time.Now().UTC().Format(time.RFC3339Nano), step, kind, text)
	return err
}

func (s *Store) Events(runID int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, at, step, kind, msg FROM run_event WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.Seq, &at, &e.Step, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// RunByID returns one run; id 0 means the latest (a dry run counts).
func (s *Store) RunByID(workflow string, id int64) (RunRecord, bool, error) {
	q := `SELECT id, workflow, pkg_hash, started, COALESCE(ended,''), dry, status, COALESCE(summary,''), COALESCE(steps,''), tokens FROM run WHERE workflow=?`
	args := []any{workflow}
	if id > 0 {
		q += ` AND id=?`
		args = append(args, id)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	var r RunRecord
	var d int
	err := s.db.QueryRow(q, args...).Scan(&r.ID, &r.Workflow, &r.PkgHash, &r.Started, &r.Ended, &d, &r.Status, &r.Summary, &r.Steps, &r.Tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	r.Dry = d == 1
	return r, err == nil, err
}

func (s *Store) Runs(workflow string, limit int) ([]RunRecord, error) {
	rows, err := s.db.Query(`SELECT id, workflow, pkg_hash, started, COALESCE(ended,''), dry, status, COALESCE(summary,''), COALESCE(steps,''), tokens
		FROM run WHERE workflow=? ORDER BY id DESC LIMIT ?`, workflow, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		var r RunRecord
		var d int
		if err := rows.Scan(&r.ID, &r.Workflow, &r.PkgHash, &r.Started, &r.Ended, &d, &r.Status, &r.Summary, &r.Steps, &r.Tokens); err != nil {
			return nil, err
		}
		r.Dry = d == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
