package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"sentinel/internal/engine"
	"sentinel/internal/store"
)

// StoreBackend reads the same SQLite state the CLI reads and performs actions by calling the
// same operations, or by running this very binary (so a run from the screen is exactly a
// `sentinel run`, with the same locks, keys and alerts).
type StoreBackend struct {
	Store *store.Store
	Self  string // path of the sentinel binary
	Timer func(name string) string
}

func (b *StoreBackend) Load() (Snapshot, error) {
	ws, err := b.Store.Workflows()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{At: time.Now()}
	for _, w := range ws {
		r := Row{Name: w.Name}
		if b.Timer != nil {
			r.Timer = b.Timer(w.Name)
		}
		paused, why, _ := b.Store.Paused(w.Name)
		r.Paused, r.PauseReason = paused, why
		if ok, found, _ := b.Store.LastOK(w.Name); found {
			t := parse(ok.Started)
			r.LastOK = &Run{When: t, Status: ok.Status, Summary: ok.Summary}
			r.LastOKAt = t
		}
		if p, _ := b.Store.GetPending(w.Name); p != nil {
			r.Pending, r.PendingAt, r.TaskFile = true, parse(p.QueuedAt), p.TaskFile
		}
		runs, _ := b.Store.Runs(w.Name, 8)
		for _, x := range runs {
			r.Runs = append(r.Runs, Run{When: parse(x.Started), Status: x.Status, Dry: x.Dry, Summary: x.Summary})
		}
		snap.Rows = append(snap.Rows, r)
	}
	return snap, nil
}

func (b *StoreBackend) Run(name string, dry bool) (string, error) {
	args := []string{"run", name}
	if dry {
		args = append(args, "--dry-run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, b.Self, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return text, fmt.Errorf("finished with %s", exitWords(ee.ExitCode()))
	}
	return text, err
}

func exitWords(code int) string {
	switch code {
	case 1:
		return "an error"
	case 3:
		return "the workflow paused"
	case 4:
		return "a connector login failure"
	}
	return fmt.Sprintf("exit code %d", code)
}

func (b *StoreBackend) Ack(name string) error {
	p, err := b.Store.GetPending(name)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("nothing is waiting on you any more")
	}
	return b.Store.Ack(name, p.Token)
}

func (b *StoreBackend) SetPaused(name string, paused bool) error {
	return b.Store.SetPaused(name, paused, "paused by you in the terminal view")
}

func parse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Detail builds the workflow screen: the graph from the workflow file, every run from the store
// and, for the chosen run (0 = latest), what each step did plus its log.
func (b *StoreBackend) Detail(name string, runID int64) (Detail, error) {
	d := Detail{Name: name}
	runs, err := b.Store.Runs(name, 50)
	if err != nil {
		return d, err
	}
	for _, r := range runs {
		d.Runs = append(d.Runs, RunInfo{ID: r.ID, When: parse(r.Started), Status: r.Status, Dry: r.Dry, Summary: r.Summary})
	}
	rec, hasRun, err := b.Store.RunByID(name, runID)
	if err != nil {
		return d, err
	}
	overlay := map[string]engine.StepResult{}
	if hasRun {
		d.RunID, d.HasRun = rec.ID, true
		var steps []engine.StepResult
		_ = json.Unmarshal([]byte(rec.Steps), &steps)
		for _, s := range steps {
			overlay[s.ID] = s
		}
		if evs, _ := b.Store.Events(rec.ID); len(evs) > 0 {
			t0 := evs[0].At
			for _, e := range evs {
				d.Log = append(d.Log, LogLine{Offset: fmt.Sprintf("%.2fs", e.At.Sub(t0).Seconds()), Step: e.Step, Kind: e.Kind, Text: e.Text})
			}
		}
	}
	path, ok, _ := b.Store.WorkflowPath(name)
	if !ok {
		d.GraphErr = "the workflow file is not registered"
		return d, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		d.GraphErr = err.Error()
		return d, nil
	}
	pkg, err := engine.Load(raw)
	if err != nil {
		d.GraphErr = err.Error()
		return d, nil
	}
	g := engine.BuildGraph(pkg.Workflow)
	d.Every = g.Every
	for _, n := range g.Nodes {
		st := Step{Order: n.Order, ID: n.ID, Block: n.Block, Effect: string(n.Effect), Level: n.Level, When: n.When, Reads: n.Reads}
		if r, ok := overlay[n.ID]; ok {
			st.Status, st.Millis = r.Status, r.Millis
			switch {
			case r.Status == "failed":
				st.Note = r.Error
			case r.Plan != "":
				st.Note = r.Plan
			default:
				st.Note = r.Note
			}
			st.Note = strings.Join(strings.Fields(st.Note), " ")
		}
		d.Steps = append(d.Steps, st)
	}
	return d, nil
}
