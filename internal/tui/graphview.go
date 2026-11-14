package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ---- data for the workflow screen ----

// Step is one node of the workflow graph, with what the selected run did to it.
type Step struct {
	Order  int
	ID     string
	Block  string
	Effect string // read | write-internal | write-external
	Level  int
	When   string
	Reads  []string
	Status string // ran | skipped | failed | planned | "" (not reached, or no run selected)
	Millis int64
	Note   string
}

type RunInfo struct {
	ID      int64
	When    time.Time
	Status  string
	Dry     bool
	Summary string
}

type LogLine struct {
	Offset string
	Step   string
	Kind   string
	Text   string
}

// Detail is everything the workflow screen shows: the graph, every run of the workflow
// (newest first) and the log of the selected run.
type Detail struct {
	Name     string
	Every    string
	Steps    []Step
	Runs     []RunInfo
	RunID    int64 // the run the overlay and log belong to; 0 when there is none
	HasRun   bool  // an overlay applies
	Log      []LogLine
	GraphErr string // the workflow file could not be read
}

// ---- drawing ----

func statusStyle(s string, overlay bool) lipgloss.Style {
	switch {
	case !overlay:
		return border
	case s == "ran":
		return green
	case s == "failed":
		return red
	case s == "planned":
		return yellow
	}
	return dim
}

func effectMark(e string) string {
	switch e {
	case "write-internal":
		return yellow.Render("◆")
	case "write-external":
		return red.Render("■")
	}
	return green.Render("◇")
}

func stepState(s Step, overlay bool) string {
	if !overlay {
		return ""
	}
	switch s.Status {
	case "":
		return "· not reached"
	case "failed":
		return "✗ FAILED " + millis(s.Millis)
	case "skipped":
		return "– skipped"
	case "planned":
		return "○ would run"
	}
	return strings.TrimSpace("✓ ran " + millis(s.Millis))
}

func millis(v int64) string {
	switch {
	case v <= 0:
		return ""
	case v >= 1000:
		return fmt.Sprintf("%.1fs", float64(v)/1000)
	}
	return fmt.Sprintf("%dms", v)
}

// stepBox draws one step as a bordered box, iw columns of text wide and 4 lines tall.
func stepBox(s Step, overlay bool, iw int) []string {
	st := statusStyle(s.Status, overlay)
	cond := ""
	if s.When != "" {
		cond = "? " + s.When
	}
	deps := ""
	if len(s.Reads) > 0 {
		deps = "← " + strings.Join(s.Reads, ", ")
	}
	last := strings.TrimSpace(deps + "  " + cond)
	t := fmt.Sprintf(" %d %s ", s.Order, trunc(s.ID, iw-6))
	top := st.Render("┌") + effectMark(s.Effect) + bold.Render(t)
	pad := iw + 1 - lipgloss.Width(top)
	if pad < 0 {
		pad = 0
	}
	top += st.Render(strings.Repeat("─", pad) + "┐")
	row := func(text string, style lipgloss.Style) string {
		return st.Render("│") + fit(style.Render(trunc(text, iw)), iw) + st.Render("│")
	}
	state := stepState(s, overlay)
	stStyle := dim
	if overlay {
		stStyle = statusStyle(s.Status, true)
	}
	if s.Note != "" && overlay {
		state += " " + s.Note
	}
	return []string{
		top,
		row(" "+s.Block, dim),
		row(" "+state, stStyle),
		row(" "+last, dim),
		st.Render("└" + strings.Repeat("─", iw) + "┘"),
	}
}

// graphLines draws the layered graph for a terminal width: one row of boxes per layer, steps
// that depend on earlier ones below them, with an arrow between layers.
func graphLines(d Detail, width int) []string {
	if len(d.Steps) == 0 {
		return []string{dim.Render("  (no steps)")}
	}
	maxLevel := 0
	byLevel := map[int][]Step{}
	for _, s := range d.Steps {
		byLevel[s.Level] = append(byLevel[s.Level], s)
		if s.Level > maxLevel {
			maxLevel = s.Level
		}
	}
	iw := 26
	cols := func(n int) int { return max(1, min(n, (width-2)/(iw+3))) }
	if width < iw+6 {
		iw = max(width-6, 12)
	}
	var out []string
	first := true
	for lv := 0; lv <= maxLevel; lv++ {
		steps := byLevel[lv]
		if len(steps) == 0 {
			continue
		}
		if !first {
			pad := strings.Repeat(" ", 2+(iw+2)/2)
			out = append(out, pad+border.Render("▼"))
		}
		first = false
		c := cols(len(steps))
		for i := 0; i < len(steps); i += c {
			end := min(i+c, len(steps))
			boxes := make([][]string, 0, c)
			for _, s := range steps[i:end] {
				boxes = append(boxes, stepBox(s, d.HasRun, iw))
			}
			for l := 0; l < 5; l++ {
				line := " "
				for j, b := range boxes {
					if j > 0 {
						line += "  "
					}
					line += b[l]
				}
				out = append(out, line)
			}
		}
	}
	return out
}

func logLines(d Detail, width int) []string {
	if len(d.Log) == 0 {
		return []string{dim.Render("  No log for this run.")}
	}
	var out []string
	for _, e := range d.Log {
		sty := dim
		if e.Kind == "error" {
			sty = red
		}
		out = append(out, " "+dim.Render(fmt.Sprintf("%7s", e.Offset))+" "+sty.Render(fmt.Sprintf("%-14s", trunc(e.Step, 14)))+" "+trunc(e.Text, max(width-26, 10)))
	}
	return out
}

func runLines(d Detail, width, rows int) []string {
	if len(d.Runs) == 0 {
		return []string{dim.Render("  No runs yet. Press d for a dry run or r to run.")}
	}
	out := []string{colHead.Render(trunc(fmt.Sprintf(" %-6s %-16s %-10s %s", "RUN", "STARTED", "STATUS", "SUMMARY"), width))}
	sel := 0
	for i, r := range d.Runs {
		if r.ID == d.RunID {
			sel = i
		}
	}
	start := max(0, min(sel-rows/2, len(d.Runs)-rows))
	end := min(len(d.Runs), start+max(rows, 1))
	for _, r := range d.Runs[start:end] {
		sty := green
		if r.Status != "ok" {
			sty = red
		}
		kind := ""
		if r.Dry {
			kind = " dry"
		}
		line := fmt.Sprintf(" %-6s %-16s %s %s", fmt.Sprintf("#%d", r.ID), r.When.Local().Format("Mon 02 Jan 15:04"),
			padTo(sty.Render(trunc(r.Status+kind, 10)), 10), dim.Render(trunc(strings.Join(strings.Fields(r.Summary), " "), max(width-40, 8))))
		if r.ID == d.RunID {
			line = selRow.Render(padTo(plain(line), width))
		}
		out = append(out, line)
	}
	return out
}
