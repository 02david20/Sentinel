package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The look follows k9s: an info/keys/logo header, one bordered table with a highlighted
// selection, a describe pane for the selected row, and a crumb bar at the bottom.

var (
	cBorder = lipgloss.Color("33")  // dodger blue
	cTitle  = lipgloss.Color("51")  // aqua
	cLabel  = lipgloss.Color("208") // orange
	cKey    = lipgloss.Color("205") // pink
	cOK     = lipgloss.Color("42")
	cWarn   = lipgloss.Color("214")
	cBad    = lipgloss.Color("196")
	cMuted  = lipgloss.Color("245")
	cText   = lipgloss.Color("252")

	bold    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	dim     = lipgloss.NewStyle().Foreground(cMuted)
	red     = lipgloss.NewStyle().Foreground(cBad).Bold(true)
	yellow  = lipgloss.NewStyle().Foreground(cWarn).Bold(true)
	green   = lipgloss.NewStyle().Foreground(cOK)
	label   = lipgloss.NewStyle().Foreground(cLabel).Bold(true)
	keyCap  = lipgloss.NewStyle().Foreground(cKey).Bold(true)
	title   = lipgloss.NewStyle().Foreground(cTitle).Bold(true)
	border  = lipgloss.NewStyle().Foreground(cBorder)
	colHead = lipgloss.NewStyle().Foreground(cLabel).Bold(true)
	selRow  = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cTitle).Bold(true)
	crumbA  = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cTitle).Bold(true)
	crumbB  = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cWarn).Bold(true)
	warnBar = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cWarn).Bold(true)
	logo    = lipgloss.NewStyle().Foreground(cLabel).Bold(true)
)

// State is the one-word state of a row, with its style.
func (r Row) State() (string, lipgloss.Style) {
	switch {
	case r.Paused:
		return "PAUSED", yellow
	case r.Pending:
		return "WAITING ON YOU", yellow
	case len(r.Runs) > 0 && !r.Runs[0].Dry && r.Runs[0].Status != "ok":
		return "last run " + r.Runs[0].Status, red
	case r.LastOK == nil:
		return "never checked", dim
	}
	return "ok", green
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	w := max(m.width, 40)
	h := max(m.height, 10)
	if m.mode == modeHelp {
		return m.helpView(w, h)
	}
	head := m.header(w)
	avail := h - len(strings.Split(head, "\n")) - 2 // crumb bar + message line
	var body string
	if m.screen == screenWorkflow {
		body = clip(m.workflowView(w, avail), w, avail)
		return head + "\n" + body + "\n" + m.crumbs(w) + "\n" + m.message(w)
	}
	if len(m.snap.Rows) == 0 {
		body = box("workflows", "\n  No workflows yet.\n\n  In another terminal:\n    sentinel run workflows/example.yaml --dry-run\n    sentinel install workflows/example.yaml", w, max(avail, 5))
	} else {
		tableH := min(len(m.snap.Rows)+3, max(avail/2, 5))
		descH := max(avail-tableH, 3)
		body = box(fmt.Sprintf("workflows(all)[%d]", len(m.snap.Rows)), m.table(w-2), w, tableH) + "\n" +
			box("describe("+m.sel+")", indent(m.detailView(w-4)), w, descH)
	}
	body = clip(body, w, avail)
	return head + "\n" + body + "\n" + m.crumbs(w) + "\n" + m.message(w)
}

// workflowView is one workflow: its graph (or the run's log) on top and every run below.
func (m Model) workflowView(w, avail int) string {
	d := m.detail
	runsH := min(len(d.Runs)+3, 9)
	runsH = max(runsH, 4)
	topH := max(avail-runsH, 5)
	var lines []string
	title := "graph(" + m.sel + ")"
	switch {
	case m.showLog:
		lines = logLines(d, w-4)
		title = fmt.Sprintf("log(%s #%d)", m.sel, d.RunID)
	case d.GraphErr != "":
		lines = []string{"", "  " + red.Render("Cannot draw the graph: ") + d.GraphErr}
	default:
		lines = graphLines(d, w-2)
		if d.HasRun {
			title = fmt.Sprintf("graph(%s #%d)", m.sel, d.RunID)
		}
		if d.Every != "" {
			title += " every " + d.Every
		}
	}
	inner := topH - 2
	scroll := max(0, min(m.scroll, len(lines)-inner))
	if len(lines) > inner {
		title += fmt.Sprintf(" [%d-%d/%d]", scroll+1, min(scroll+inner, len(lines)), len(lines))
	}
	if scroll < len(lines) {
		lines = lines[scroll:]
	}
	top := box(title, strings.Join(lines, "\n"), w, topH)
	runs := box(fmt.Sprintf("runs(%s)[%d]", m.sel, len(d.Runs)), strings.Join(runLines(d, w-4, runsH-3), "\n"), w, runsH)
	return top + "\n" + runs
}

// header: info on the left, key hints in the middle, logo on the right (each part drops out
// when the terminal is too narrow for it).
func (m Model) header(w int) string {
	waiting, stopped := 0, 0
	for _, r := range m.snap.Rows {
		if r.Pending {
			waiting++
		}
		if r.Paused {
			stopped++
		}
	}
	at := m.snap.At
	if at.IsZero() {
		at = time.Now()
	}
	info := []string{
		label.Render("Workflows: ") + bold.Render(fmt.Sprint(len(m.snap.Rows))),
		label.Render("Waiting:   ") + countStyle(waiting, yellow).Render(fmt.Sprint(waiting)) + label.Render("  Paused: ") + countStyle(stopped, red).Render(fmt.Sprint(stopped)),
		label.Render("Refreshed: ") + dim.Render(at.Format("15:04:05")),
	}
	if m.loadErr != nil {
		info[2] = red.Render("state: " + m.loadErr.Error())
	}
	cols := []string{fitBlock(info, 36)}
	if w >= 70 {
		cols = append(cols, fitBlock(hintLines(), 62-14))
	}
	if w >= 100 {
		cols = append(cols, "", logoBlock())
	}
	lines := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, cols...), "\n")
	for i, l := range lines {
		lines[i] = " " + l
	}
	return clip(strings.Join(lines, "\n"), w, 4)
}

func countStyle(n int, s lipgloss.Style) lipgloss.Style {
	if n == 0 {
		return dim
	}
	return s
}

func hintLines() []string {
	hint := func(k, d string) string { return keyCap.Render("<"+k+">") + " " + dim.Render(d) }
	pad := func(s string) string { return padTo(s, 24) }
	return []string{
		pad(hint("d", "Dry run")) + hint("p", "Pause/resume"),
		pad(hint("r", "Run now")) + hint("?", "Help"),
		pad(hint("a", "Mark handled")) + hint("↑↓", "Select"),
		pad(hint("⏎", "Open graph/runs")) + hint("q", "Quit"),
	}
}

func logoBlock() string {
	return logo.Render("╦ ╦╔═╗\n║║║╠╣ \n╚╩╝╚  ")
}

func fitBlock(lines []string, w int) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = padTo(l, w)
	}
	return strings.Join(out, "\n")
}

// table renders the header row and one row per workflow, sized to width.
func (m Model) table(width int) string {
	const nameW, stateW, lastW = 20, 16, 10
	sumW := width - nameW - stateW - lastW - 5
	wide := sumW >= 12
	var b strings.Builder
	head := fmt.Sprintf(" %-*s %-*s %-*s", nameW, "NAME", stateW, "STATE", lastW, "LAST OK")
	if wide {
		head += " SUMMARY"
	}
	b.WriteString(colHead.Render(trunc(head, width)) + "\n")
	for _, r := range m.snap.Rows {
		st, style := r.State()
		last := "never"
		sum := ""
		if r.LastOK != nil {
			last = ago(r.LastOKAt)
			last = strings.Replace(strings.Replace(last, " min", "m", 1), " h", "h", 1)
			sum = strings.Join(strings.Fields(r.LastOK.Summary), " ")
		}
		cells := fmt.Sprintf(" %s %s %s", padTo(trunc(r.Name, nameW), nameW), padTo(style.Render(trunc(st, stateW)), stateW), padTo(dim.Render(trunc(last, lastW)), lastW))
		if wide {
			cells += " " + dim.Render(trunc(sum, sumW))
		}
		if r.Name == m.sel {
			cells = selRow.Render(padTo(plain(cells), width))
		}
		b.WriteString(cells + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) detailView(width int) string {
	r := m.current()
	if r == nil {
		return ""
	}
	var b strings.Builder
	st, style := r.State()
	b.WriteString(label.Render("Name:    ") + bold.Render(r.Name) + "   " + style.Render(st) + "\n")
	b.WriteString(label.Render("Timer:   ") + dim.Render(orDash(r.Timer, "not installed")) + "\n")
	if r.LastOK != nil {
		b.WriteString(label.Render("Checked: ") + fmt.Sprintf("%s (%s ago)\n", stamp(r.LastOKAt), ago(r.LastOKAt)))
		b.WriteString(label.Render("Result:  ") + strings.ReplaceAll(wrap(orDash(r.LastOK.Summary, "ok"), width-10), "\n  ", "\n         ") + "\n")
	} else {
		b.WriteString(label.Render("Checked: ") + "never\n")
	}
	switch {
	case r.Paused:
		b.WriteString("\n" + yellow.Render("Paused.") + " Press p to resume.\n")
	}
	if r.Pending {
		b.WriteString("\n" + yellow.Render("Waiting on you") + fmt.Sprintf(": an escalation from %s (%s ago)\n", stamp(r.PendingAt), ago(r.PendingAt)))
		b.WriteString("  Task: " + r.TaskFile + "\n")
		b.WriteString("  Handle it, then press a. Later changes are held back until then.\n")
	}
	if len(r.Runs) > 0 {
		b.WriteString("\n" + label.Render("Recent runs") + "\n")
		for i, run := range r.Runs {
			if i == 6 {
				break
			}
			kind := ""
			if run.Dry {
				kind = " (dry run)"
			}
			sty := green
			if run.Status != "ok" {
				sty = red
			}
			b.WriteString("  " + dim.Render(stamp(run.When)) + "  " + sty.Render(fmt.Sprintf("%-10s", run.Status)) + dim.Render(kind) + " " + trunc(run.Summary, max(width-34, 10)) + "\n")
		}
	}
	if m.outText != "" {
		b.WriteString("\n" + label.Render(m.outTitle) + "\n")
		lines := strings.Split(strings.TrimSpace(m.outText), "\n")
		if len(lines) > 12 {
			lines = append(lines[:12], dim.Render(fmt.Sprintf("… %d more lines", len(lines)-12)))
		}
		for _, l := range lines {
			b.WriteString("  " + trunc(l, width-2) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// crumbs is the k9s-style breadcrumb bar at the bottom.
func (m Model) crumbs(w int) string {
	bar := crumbA.Render(" workflows ")
	if m.sel != "" {
		bar += crumbB.Render(" " + trunc(m.sel, 24) + " ")
	}
	if m.screen == screenWorkflow {
		bar += crumbA.Render(" runs ")
		if m.detail.RunID > 0 {
			bar += crumbB.Render(fmt.Sprintf(" #%d ", m.detail.RunID))
		}
	}
	if w < 70 {
		bar += " " + dim.Render(trunc("d dry · r run · a handled · p pause · ? help", max(w-lipgloss.Width(bar)-2, 0)))
	}
	return bar
}

// message is the line under the crumbs: a confirmation question, what is running, or a note.
func (m Model) message(w int) string {
	switch {
	case m.mode == modeConfirm:
		return warnBar.Render(padTo(" "+m.confirm.prompt, w))
	case m.busy != "":
		return yellow.Render(trunc(m.busy, w))
	case m.note != "":
		return trunc(m.note, w)
	}
	if m.screen == screenWorkflow {
		return dim.Render(trunc(" ↑↓ pick run · l log/graph · PgUp/PgDn scroll · d dry · r run · p pause · esc back · ? help", w))
	}
	return dim.Render(trunc(" ↑↓ select · enter open · d dry run · r run · a handled · p pause/resume · ? help · q quit", w))
}

func (m Model) helpView(w, h int) string {
	k := func(keys, d string) string { return "  " + keyCap.Render(padTo(keys, 14)) + d }
	lines := []string{
		"",
		k("↑ ↓  or  j k", "choose a workflow"),
		k("g  G", "first / last"),
		k("enter", "open a workflow: its graph and every run"),
		k("  in a workflow", "↑↓ pick a run (the graph shows what that run did), l log, PgUp/PgDn scroll, esc back"),
		k("d", "dry run: reads, sends and saves nothing"),
		k("r", "run for real (asks first; may post to the webhook and start an escalation)"),
		k("a", "mark the waiting escalation as handled (asks first)"),
		k("p", "pause or resume scheduled runs"),
		k("q", "quit (esc goes back, or quits from the list)"),
		"",
		"  The screen refreshes every 3 seconds. It shows the same state as `sentinel status`.",
		"  Closing it never changes how scheduled runs behave.",
		"",
		"  " + dim.Render("press any key"),
	}
	return clip(box("help", strings.Join(lines, "\n"), w, min(len(lines)+2, h)), w, h)
}

// box draws a bordered pane with a centred title, exactly w wide and h tall.
func box(titleText, content string, w, h int) string {
	iw := max(w-2, 4)
	t := " " + titleText + " "
	if lipgloss.Width(t) > iw {
		t = trunc(t, iw)
	}
	left := (iw - lipgloss.Width(t)) / 2
	right := iw - lipgloss.Width(t) - left
	var b strings.Builder
	b.WriteString(border.Render("┌"+strings.Repeat("─", left)) + title.Render(t) + border.Render(strings.Repeat("─", right)+"┐") + "\n")
	lines := strings.Split(content, "\n")
	rows := max(h-2, 1)
	for i := 0; i < rows; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(border.Render("│") + fit(l, iw) + border.Render("│") + "\n")
	}
	b.WriteString(border.Render("└" + strings.Repeat("─", iw) + "┘"))
	return b.String()
}

// fit pads or cuts a (possibly coloured) line to exactly w columns.
func fit(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	if n := lipgloss.Width(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// indent gives pane content a one-space margin from the border.
func indent(s string) string {
	return " " + strings.ReplaceAll(s, "\n", "\n ")
}
