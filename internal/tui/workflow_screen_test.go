package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type shapeFake struct {
	fake
	steps, runs int
	graphErr    string
	noLog       bool
}

func (f *shapeFake) Detail(n string, id int64) (Detail, error) {
	d := Detail{Name: n, Every: "1h", GraphErr: f.graphErr}
	for i := 0; i < f.steps; i++ {
		s := Step{Order: i + 1, ID: fmt.Sprintf("step-%d-with-a-rather-long-identifier", i), Block: "Some.VeryLongBlockName", Effect: []string{"read", "write-internal", "write-external"}[i%3],
			Level: i / 3, Reads: []string{"a", "b", "c", "d"}, When: "changes.has_changes && x > 3", Status: []string{"ran", "skipped", "failed", "planned", ""}[i%5], Note: strings.Repeat("note ", 30)}
		d.Steps = append(d.Steps, s)
	}
	for i := 0; i < f.runs; i++ {
		d.Runs = append(d.Runs, RunInfo{ID: int64(f.runs - i), When: time.Now(), Status: "ok", Summary: strings.Repeat("summary ", 20)})
	}
	if f.runs > 0 {
		d.HasRun, d.RunID = true, d.Runs[0].ID
		if id > 0 {
			d.RunID = id
		}
		if !f.noLog {
			for i := 0; i < 80; i++ {
				d.Log = append(d.Log, LogLine{Offset: "0.00s", Step: "a-very-long-step-name", Kind: "error", Text: strings.Repeat("x", 300)})
			}
		}
	}
	return d, nil
}

func TestWorkflowScreenNeverPanicsAndNeverOverflows(t *testing.T) {
	keys := []string{"enter", "down", "down", "up", "l", "pgdown", "pgdown", "pgdown", "pgup", "G", "g", "l", "pgup", "pgup", "pgup", "?", "x", "esc", "d", "r", "n", "a", "p", "j", "k", "left", "esc"}
	sizes := [][2]int{{0, 0}, {1, 1}, {10, 5}, {40, 10}, {40, 3}, {20, 100}, {60, 12}, {100, 30}, {200, 8}}
	for _, steps := range []int{0, 1, 2, 30} {
		for _, runs := range []int{0, 1, 3, 60} {
			for _, gerr := range []string{"", "no file registered"} {
				for _, sz := range sizes {
					name := fmt.Sprintf("steps=%d runs=%d gerr=%q %dx%d", steps, runs, gerr, sz[0], sz[1])
					f := &shapeFake{steps: steps, runs: runs, graphErr: gerr}
					f.snap = rows()
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Errorf("%s: PANIC %v", name, r)
							}
						}()
						m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: sz[0], Height: sz[1]}, loadedMsg{snap: f.snap})
						check := func(after string) {
							v := m.View()
							lines := strings.Split(v, "\n")
							for i, l := range lines {
								if w := lipglossWidth(l); w > max(sz[0], 40) {
									t.Errorf("%s after %s: line %d is %d wide", name, after, i, w)
									return
								}
							}
							if len(lines) > max(sz[1], 10)+2 {
								t.Errorf("%s after %s: %d lines (limit %d)", name, after, len(lines), max(sz[1], 10))
							}
						}
						check("load")
						for _, k := range keys {
							m = drive(m, key(k))
							check(k)
						}
					}()
				}
			}
		}
	}
}

// With a short terminal the runs pane (the thing ↑↓ selects) must stay visible.
func TestRunsPaneVisibleOnShortTerminal(t *testing.T) {
	f := &shapeFake{steps: 3, runs: 5}
	f.snap = rows()
	m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 80, Height: 24}, loadedMsg{snap: f.snap}, key("enter"))
	if !strings.Contains(m.View(), "runs(alpha)") {
		t.Errorf("80x24: runs pane not visible:\n%s", m.View())
	}
	m = drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 40, Height: 10}, loadedMsg{snap: f.snap}, key("enter"))
	if !strings.Contains(m.View(), "runs(alpha)") {
		t.Errorf("40x10: runs pane not visible, user cannot see which run is selected:\n%s", m.View())
	}
}

// PgDn far past the end must not need as many PgUp presses to come back.
func TestScrollIsClamped(t *testing.T) {
	f := &shapeFake{steps: 12, runs: 2}
	f.snap = rows()
	m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 100, Height: 30}, loadedMsg{snap: f.snap}, key("enter"))
	for i := 0; i < 40; i++ {
		m = drive(m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	end := len(m.(Model).topLines()) - 1
	if got := m.(Model).scroll; got != end {
		t.Fatalf("scroll after paging past the end = %d, want the last line (%d)", got, end)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if got := m.(Model).scroll; got != end-5 {
		t.Fatalf("one PgUp from the end moved to %d, want %d (the offset must not run ahead of the content)", got, end-5)
	}
}

func TestStaleDetailOverwritesSelection(t *testing.T) {
	f := &shapeFake{steps: 2, runs: 5}
	f.snap = rows()
	m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 100, Height: 30}, loadedMsg{snap: f.snap}, key("enter"))
	m = drive(m, key("down"))
	m = drive(m, key("down"))
	mm := m.(Model)
	want := mm.runID
	t.Logf("runID=%d detail=%d", mm.runID, mm.detail.RunID)
	// a slow response for an older request (run 5, the latest) arrives afterwards
	old, _ := f.Detail("alpha", 0)
	m2, _ := m.Update(detailMsg{d: old})
	if got := m2.(Model).detail.RunID; want != 0 && got != want {
		t.Errorf("selected run %d but a late reply for run %d replaced the screen", want, got)
	}
}

func TestEmptyListKeysDoNothingBad(t *testing.T) {
	f := &shapeFake{}
	m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 50, Height: 12}, loadedMsg{snap: Snapshot{}})
	for _, k := range []string{"enter", "d", "r", "a", "p", "down", "G", "l", "pgdown", "esc"} {
		m = drive(m, key(k))
	}
	_ = m.View()
}

func TestWorkflowVanishesWhileOpen(t *testing.T) {
	f := &shapeFake{steps: 2, runs: 2}
	f.snap = rows()
	m := drive(tea.Model(New(f)), tea.WindowSizeMsg{Width: 100, Height: 30}, loadedMsg{snap: f.snap}, key("enter"))
	m, _ = m.Update(loadedMsg{snap: Snapshot{}})
	_ = m.View()
	m = drive(m, key("d"), key("esc"))
	_ = m.View()
}
