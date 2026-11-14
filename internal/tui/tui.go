// Package tui is the interactive terminal view of sentinel: every workflow at a glance, what is
// waiting on you, recent runs, and one-key actions. It only reads the same state the CLI reads
// and calls the same operations; nothing here changes how a scheduled run behaves.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---- data the screen shows ----

type Run struct {
	When    time.Time
	Status  string
	Dry     bool
	Summary string
}

type Row struct {
	Name        string
	Timer       string
	Paused      bool
	PauseReason string
	LastOK      *Run
	LastOKAt    time.Time
	Pending     bool
	PendingAt   time.Time
	TaskFile    string
	Runs        []Run
}

type Snapshot struct {
	Rows []Row
	At   time.Time
}

// Backend is everything the screen needs from the rest of sentinel. Tests use a fake.
type Backend interface {
	Load() (Snapshot, error)
	Run(name string, dry bool) (output string, err error)
	Ack(name string) error
	SetPaused(name string, paused bool) error
	// Detail returns the graph and the runs of one workflow; runID 0 selects the latest run.
	Detail(name string, runID int64) (Detail, error)
}

// ---- messages ----

type loadedMsg struct {
	snap Snapshot
	err  error
}
type detailMsg struct {
	d   Detail
	req int64 // the run id that was asked for (0 = latest)
	err error
}
type tickMsg time.Time
type actionDoneMsg struct {
	title  string
	output string
	err    error
}

type mode int

const (
	modeNormal mode = iota
	modeConfirm
	modeHelp
)

// screen is which page is showing: the list of workflows, or one workflow's graph and runs.
type screen int

const (
	screenList screen = iota
	screenWorkflow
)

// Model is the Bubble Tea model.
type Model struct {
	be       Backend
	snap     Snapshot
	loadErr  error
	sel      string // selected workflow name (stable across reloads)
	width    int
	height   int
	mode     mode
	confirm  confirmAction
	note     string // one-line message under the screen
	busy     string // what is running now, if anything
	outTitle string
	outText  string
	quitting bool

	screen  screen
	detail  Detail
	runID   int64 // selected run on the workflow screen; 0 = latest
	showLog bool  // the top pane shows the run's log instead of the graph
	scroll  int   // first visible line of the top pane
}

type confirmAction struct {
	kind   string // "run" | "ack"
	name   string
	prompt string
}

func New(be Backend) Model { return Model{be: be, width: 100, height: 30} }

func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tick()) }

func tick() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		s, err := m.be.Load()
		return loadedMsg{snap: s, err: err}
	}
}

func (m Model) loadDetail() tea.Cmd {
	be, name, id := m.be, m.sel, m.runID
	return func() tea.Msg {
		d, err := be.Detail(name, id)
		return detailMsg{d: d, req: id, err: err}
	}
}

// topLines is what the top pane of the workflow screen shows: the graph or the run's log.
func (m Model) topLines() []string {
	if m.showLog {
		return logLines(m.detail, max(m.width, 40)-4)
	}
	if m.detail.GraphErr != "" {
		return []string{"", "  Cannot draw the graph: " + m.detail.GraphErr}
	}
	return graphLines(m.detail, max(m.width, 40)-2)
}

// refresh reloads the list and, on the workflow screen, the open workflow too.
func (m Model) refresh() tea.Cmd {
	if m.screen == screenWorkflow {
		return tea.Batch(m.load(), m.loadDetail())
	}
	return m.load()
}

func (m Model) index() int {
	for i, r := range m.snap.Rows {
		if r.Name == m.sel {
			return i
		}
	}
	return 0
}

func (m Model) current() *Row {
	if len(m.snap.Rows) == 0 {
		return nil
	}
	return &m.snap.Rows[m.index()]
}

// ---- update ----

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case detailMsg:
		if msg.err != nil {
			m.note = "Could not load " + m.sel + ": " + msg.err.Error()
			return m, nil
		}
		// a reply for a run that is no longer the selected one (a slow refresh) is dropped
		if msg.d.Name == m.sel && m.screen == screenWorkflow && msg.req == m.runID {
			m.detail = msg.d
		}
		return m, nil
	case loadedMsg:
		m.loadErr = msg.err
		if msg.err == nil {
			m.snap = msg.snap
			if len(m.snap.Rows) > 0 && m.sel == "" {
				m.sel = m.snap.Rows[0].Name
			}
			if m.current() != nil {
				m.sel = m.current().Name // fall back to the first row if the old one vanished
			}
		}
		return m, nil
	case actionDoneMsg:
		m.busy = ""
		m.outTitle, m.outText = msg.title, msg.output
		if msg.err != nil && msg.output == "" {
			m.outText = msg.err.Error()
		}
		m.note = ""
		return m, m.refresh()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	switch m.mode {
	case modeHelp:
		m.mode = modeNormal
		return m, nil
	case modeConfirm:
		m.mode = modeNormal
		c := m.confirm
		if s != "y" && s != "Y" {
			m.note = "Cancelled."
			return m, nil
		}
		switch c.kind {
		case "run":
			m.busy = "Running " + c.name + "…"
			return m, m.runCmd(c.name, false)
		case "ack":
			be := m.be
			return m, func() tea.Msg {
				err := be.Ack(c.name)
				if err != nil {
					return actionDoneMsg{title: "Acknowledge " + c.name, err: err}
				}
				return actionDoneMsg{title: "Acknowledge " + c.name, output: "Marked as handled. The next run can start a new escalation."}
			}
		}
		return m, nil
	}

	if m.busy != "" && s != "q" {
		m.note = "Wait for " + strings.TrimSuffix(m.busy, "…") + " to finish."
		return m, nil
	}
	m.note = ""
	cur := m.current()
	switch s {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "esc", "backspace", "left", "h":
		if m.screen == screenWorkflow {
			m.screen, m.detail, m.runID, m.showLog, m.scroll = screenList, Detail{}, 0, false, 0
			return m, nil
		}
	case "enter", "right":
		if m.screen == screenList && cur != nil {
			m.screen, m.runID, m.showLog, m.scroll = screenWorkflow, 0, false, 0
			return m, m.loadDetail()
		}
	case "?":
		m.mode = modeHelp
	case "l":
		if m.screen == screenWorkflow {
			m.showLog, m.scroll = !m.showLog, 0
		}
	case "pgdown", "ctrl+d":
		if m.screen == screenWorkflow {
			m.scroll = min(m.scroll+5, max(0, len(m.topLines())-1))
		}
	case "pgup", "ctrl+u":
		if m.screen == screenWorkflow {
			m.scroll = max(0, m.scroll-5)
		}
	case "up", "k":
		if m.screen == screenWorkflow {
			return m.moveRun(-1)
		}
		m.move(-1)
	case "down", "j":
		if m.screen == screenWorkflow {
			return m.moveRun(1)
		}
		m.move(1)
	case "g", "home":
		if m.screen == screenWorkflow {
			return m.pickRun(0)
		}
		if len(m.snap.Rows) > 0 {
			m.sel = m.snap.Rows[0].Name
		}
	case "G", "end":
		if m.screen == screenWorkflow {
			return m.pickRun(len(m.detail.Runs) - 1)
		}
		if n := len(m.snap.Rows); n > 0 {
			m.sel = m.snap.Rows[n-1].Name
		}
	case "d":
		if cur == nil {
			return m, nil
		}
		m.busy = "Dry run of " + cur.Name + "…"
		return m, m.runCmd(cur.Name, true)
	case "r":
		if cur == nil {
			return m, nil
		}
		m.mode, m.confirm = modeConfirm, confirmAction{kind: "run", name: cur.Name,
			prompt: "Run " + cur.Name + " for real now? It may post to the webhook and start an escalation. [y/N]"}
	case "a":
		if cur == nil {
			return m, nil
		}
		if !cur.Pending {
			m.note = "Nothing is waiting on you for " + cur.Name + "."
			return m, nil
		}
		m.mode, m.confirm = modeConfirm, confirmAction{kind: "ack", name: cur.Name,
			prompt: "Mark the escalation for " + cur.Name + " as handled? Held-back changes are then sent at the next run. [y/N]"}
	case "p":
		if cur == nil {
			return m, nil
		}
		name, pause := cur.Name, !cur.Paused
		be := m.be
		return m, func() tea.Msg {
			if err := be.SetPaused(name, pause); err != nil {
				return actionDoneMsg{title: "Pause " + name, err: err}
			}
			verb := "Resumed " + name + "."
			if pause {
				verb = "Paused " + name + ". Scheduled runs do nothing until you press p again."
			}
			return actionDoneMsg{title: "Pause/resume", output: verb}
		}
	}
	return m, nil
}

// moveRun selects an older (+1) or newer (-1) run of the open workflow.
func (m Model) moveRun(d int) (tea.Model, tea.Cmd) {
	i := 0
	for j, r := range m.detail.Runs {
		if r.ID == m.detail.RunID {
			i = j
		}
	}
	return m.pickRun(i + d)
}

func (m Model) pickRun(i int) (tea.Model, tea.Cmd) {
	rs := m.detail.Runs
	if len(rs) == 0 {
		return m, nil
	}
	i = max(0, min(i, len(rs)-1))
	m.runID, m.scroll = rs[i].ID, 0
	if i == 0 {
		m.runID = 0 // follow the latest run as new ones arrive
	}
	return m, m.loadDetail()
}

func (m *Model) move(d int) {
	n := len(m.snap.Rows)
	if n == 0 {
		return
	}
	i := m.index() + d
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	m.sel = m.snap.Rows[i].Name
}

func (m Model) runCmd(name string, dry bool) tea.Cmd {
	be := m.be
	title := "Run " + name
	if dry {
		title = "Dry run of " + name
	}
	return func() tea.Msg {
		out, err := be.Run(name, dry)
		return actionDoneMsg{title: title, output: out, err: err}
	}
}

// ---- small helpers ----

func plain(s string) string { return ansiStrip(s) }

func padTo(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return trunc(s, w)
}

func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(ansiStrip(s))
	if len(r) <= n {
		if lipgloss.Width(s) <= n {
			return s
		}
	}
	if len(r) > n {
		return string(r[:max(n-1, 0)]) + "…"
	}
	return string(r)
}

func orDash(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("Mon 02 Jan 15:04")
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func wrap(s string, w int) string {
	if w < 20 {
		w = 20
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > w {
			out = append(out, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		out = append(out, line)
	}
	return strings.Join(out, "\n  ")
}

// clip cuts every line to width and the whole to height lines, so a small terminal never wraps
// the layout apart.
func clip(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

func ansiStrip(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
