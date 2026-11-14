package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type fake struct {
	mu         sync.Mutex
	snap       Snapshot
	runs       []string
	acks       []string
	pauses     []string
	runErr     error
	detailReqs []int64
}

func (f *fake) Load() (Snapshot, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.snap, nil }
func (f *fake) Run(n string, dry bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, map[bool]string{true: "dry:", false: "real:"}[dry]+n)
	return "ran " + n, f.runErr
}
func (f *fake) Ack(n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, n)
	return nil
}
func (f *fake) SetPaused(n string, p bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, n)
	return nil
}

func (f *fake) Detail(n string, id int64) (Detail, error) {
	d := Detail{Name: n, Every: "4h", HasRun: true, RunID: 3,
		Steps: []Step{
			{Order: 1, ID: "fetch", Block: "Src", Effect: "read", Status: "ran", Millis: 12},
			{Order: 2, ID: "diff", Block: "Diff", Effect: "read", Level: 1, Reads: []string{"fetch"}, Status: "ran"},
			{Order: 3, ID: "post", Block: "Webhook.Post", Effect: "write-external", Level: 2, Reads: []string{"diff"}, When: "x", Status: "failed", Note: "boom"},
		},
		Runs: []RunInfo{{ID: 3, Status: "failed", When: time.Now()}, {ID: 2, Status: "ok", When: time.Now()}, {ID: 1, Status: "ok", When: time.Now()}}}
	if id > 0 {
		d.RunID = id
	}
	f.mu.Lock()
	f.detailReqs = append(f.detailReqs, id)
	f.mu.Unlock()
	return d, nil
}

func rows() Snapshot {
	now := time.Now()
	return Snapshot{Rows: []Row{
		{Name: "alpha", Timer: "(timer on)", LastOK: &Run{Summary: "12 items, 0 new, 0 changed."}, LastOKAt: now.Add(-time.Hour)},
		{Name: "beta", Pending: true, PendingAt: now.Add(-2 * time.Hour), TaskFile: "/state/tasks/beta/x.json"},
		{Name: "gamma", Paused: true, PauseReason: "paused by you"},
	}}
}

// drive feeds messages and runs any command the model returns, like the Bubble Tea runtime.
func drive(m tea.Model, msgs ...tea.Msg) tea.Model {
	queue := append([]tea.Msg(nil), msgs...)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		if cmd == nil {
			continue
		}
		if out := cmd(); out != nil {
			switch o := out.(type) {
			case tea.BatchMsg:
				for _, c := range o {
					if c != nil {
						if r := c(); r != nil {
							if _, isTick := r.(tickMsg); !isTick {
								queue = append(queue, r)
							}
						}
					}
				}
			case tickMsg:
			default:
				queue = append(queue, out)
			}
		}
	}
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func start(t *testing.T, f *fake) tea.Model {
	m := tea.Model(New(f))
	return drive(m, tea.WindowSizeMsg{Width: 110, Height: 30}, loadedMsg{snap: f.snap})
}

func TestShowsEveryWorkflowWithItsState(t *testing.T) {
	f := &fake{snap: rows()}
	v := start(t, f).View()
	for _, want := range []string{"alpha", "beta", "gamma", "WAITING ON YOU", "PAUSED"} {
		if !strings.Contains(v, want) {
			t.Fatalf("screen is missing %q:\n%s", want, v)
		}
	}
}

func TestNavigationFollowsTheSelectionAcrossReloads(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	m = drive(m, key("down"))
	if m.(Model).sel != "beta" {
		t.Fatalf("selected %q", m.(Model).sel)
	}
	if v := m.View(); !strings.Contains(v, "Waiting on you") || !strings.Contains(v, "/state/tasks/beta/x.json") {
		t.Fatalf("detail pane does not show the escalation:\n%s", v)
	}
	// A reload keeps the selection even though the list is rebuilt.
	m = drive(m, loadedMsg{snap: rows()})
	if m.(Model).sel != "beta" {
		t.Fatalf("selection jumped to %q after a reload", m.(Model).sel)
	}
	m = drive(m, key("up"), key("up"), key("up"))
	if m.(Model).sel != "alpha" {
		t.Fatalf("up must stop at the first row, got %q", m.(Model).sel)
	}
}

// A real run can post to the webhook and start an escalation, so it must never start from one key press.
func TestRealRunAsksFirstAndDefaultsToNo(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	m = drive(m, key("r"))
	if !strings.Contains(m.View(), "for real") {
		t.Fatalf("no confirmation shown:\n%s", m.View())
	}
	m = drive(m, key("esc")) // anything but y cancels
	if len(f.runs) != 0 {
		t.Fatalf("ran without confirmation: %v", f.runs)
	}
	m = drive(m, key("r"), key("y"))
	if len(f.runs) != 1 || f.runs[0] != "real:alpha" {
		t.Fatalf("runs: %v", f.runs)
	}
	if !strings.Contains(m.View(), "ran alpha") {
		t.Fatalf("output of the run is not shown:\n%s", m.View())
	}
}

func TestDryRunNeedsNoConfirmation(t *testing.T) {
	f := &fake{snap: rows()}
	drive(start(t, f), key("d"))
	if len(f.runs) != 1 || f.runs[0] != "dry:alpha" {
		t.Fatalf("runs: %v", f.runs)
	}
}

func TestAckOnlyWhenSomethingWaitsAndAsksFirst(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	m = drive(m, key("a")) // alpha has nothing pending
	if len(f.acks) != 0 || !strings.Contains(m.View(), "Nothing is waiting") {
		t.Fatalf("acked with nothing pending or no message:\n%s", m.View())
	}
	m = drive(m, key("down"), key("a"))
	if len(f.acks) != 0 {
		t.Fatal("acknowledged without confirmation")
	}
	drive(m, key("y"))
	if len(f.acks) != 1 || f.acks[0] != "beta" {
		t.Fatalf("acks: %v", f.acks)
	}
}

func TestPauseToggle(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	drive(m, key("p"))
	if len(f.pauses) != 1 || f.pauses[0] != "alpha" {
		t.Fatalf("pauses: %v", f.pauses)
	}
}

func TestFailedRunShowsTheReasonNotJustAnError(t *testing.T) {
	f := &fake{snap: rows(), runErr: errors.New("finished with a connector login failure")}
	m := start(t, f)
	m = drive(m, key("d"))
	if !strings.Contains(m.View(), "ran alpha") {
		t.Fatalf("run output lost on error:\n%s", m.View())
	}
}

func TestEmptyStateTellsYouWhatToDo(t *testing.T) {
	f := &fake{}
	v := start(t, f).View()
	if !strings.Contains(v, "sentinel install") {
		t.Fatalf("empty state gives no next step:\n%s", v)
	}
}

func TestHelpAndQuit(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	m = drive(m, key("?"))
	if !strings.Contains(m.View(), "dry run") || !strings.Contains(m.View(), "press any key") {
		t.Fatalf("help not shown:\n%s", m.View())
	}
	m = drive(m, key("x"))
	if strings.Contains(m.View(), "press any key") {
		t.Fatal("help did not close")
	}
	m, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if m.View() != "" {
		t.Fatal("a quitting model must draw nothing, so the shell is left clean")
	}
}

// No line may be wider than the terminal, or the layout wraps and breaks apart.
func TestNarrowAndShortTerminals(t *testing.T) {
	f := &fake{snap: rows()}
	for _, sz := range []struct{ w, h int }{{50, 12}, {70, 20}, {89, 25}, {90, 25}, {140, 40}, {30, 8}} {
		m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: sz.w, Height: sz.h}, loadedMsg{snap: f.snap}, key("down"))
		for i, line := range strings.Split(m.View(), "\n") {
			if w := lipglossWidth(line); w > max(sz.w, 40) {
				t.Fatalf("%dx%d: line %d is %d wide: %q", sz.w, sz.h, i, w, line)
			}
		}
		if n := len(strings.Split(m.View(), "\n")); n > max(sz.h, 10)+2 {
			t.Fatalf("%dx%d: %d lines", sz.w, sz.h, n)
		}
	}
}

func TestKeysAreIgnoredWhileBusyExceptQuit(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	mm := m.(Model)
	mm.busy = "Running alpha…"
	m2, _ := mm.Update(key("r"))
	if m2.(Model).mode != modeNormal || len(f.runs) != 0 {
		t.Fatal("a second action started while one was running")
	}
	if !strings.Contains(m2.(Model).note, "Wait") {
		t.Fatalf("no explanation: %q", m2.(Model).note)
	}
}

func TestEnterShowsGraphAndRunsAndPickingARunReloadsIt(t *testing.T) {
	f := &fake{snap: rows()}
	m := start(t, f)
	m = drive(m, tea.WindowSizeMsg{Width: 110, Height: 45}, key("enter"))
	v := m.View()
	for _, want := range []string{"graph(alpha", "runs(alpha)[3]", "fetch", "Webhook.Post", "✗ FAILED", "← diff", "#3"} {
		if !strings.Contains(v, want) {
			t.Fatalf("workflow screen is missing %q:\n%s", want, v)
		}
	}
	m = drive(m, key("down"))
	if got := f.detailReqs[len(f.detailReqs)-1]; got != 2 {
		t.Fatalf("picked run id %d, want 2 (requests %v)", got, f.detailReqs)
	}
	m = drive(m, key("l"))
	if !strings.Contains(m.View(), "log(alpha") {
		t.Fatalf("no log pane:\n%s", m.View())
	}
	m = drive(m, key("esc"))
	if m.(Model).screen != screenList || !strings.Contains(m.View(), "workflows(all)") {
		t.Fatalf("esc did not go back:\n%s", m.View())
	}
}
