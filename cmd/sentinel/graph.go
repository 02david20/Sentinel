package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/02david20/Sentinel/internal/engine"
	"github.com/02david20/Sentinel/internal/store"
)

func cmdGraph(args []string) error {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showRun := fs.Bool("run", false, "overlay what a run did (the latest, or the id that follows)")
	mermaid := fs.Bool("mermaid", false, "print a Mermaid flowchart instead of text")
	htmlOut := fs.String("html", "", "write a picture as one HTML page to this file (open it in a browser)")
	target, rest := splitFirst(args)
	// `--run 12`: the id is a bare number after the flag.
	var runID int64
	for i := 0; i < len(rest); i++ {
		if (rest[i] == "--run" || rest[i] == "-run") && i+1 < len(rest) {
			if n, err := strconv.ParseInt(rest[i+1], 10, 64); err == nil {
				runID = n
				rest = append(rest[:i+1], rest[i+2:]...)
			}
		}
	}
	if err := fs.Parse(rest); err != nil || target == "" {
		return usageErr("sentinel graph <name|file> [--run [id]] [--mermaid]")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	path, err := resolve(st, target)
	if err != nil {
		return err
	}
	p, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	g := engine.BuildGraph(p.Workflow)
	if *mermaid {
		fmt.Print(engine.RenderMermaid(g))
		return nil
	}
	var overlay map[string]engine.StepState
	var page *engine.PageRun
	header := ""
	if *showRun {
		rec, ok, err := st.RunByID(p.Workflow.Name, runID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no run %s yet for %s. Run it first: sentinel run %s", orDefault(strconv.FormatInt(runID, 10), "latest"), p.Workflow.Name, p.Workflow.Name)
		}
		overlay = overlayOf(rec)
		kind := ""
		if rec.Dry {
			kind = " (dry run)"
		}
		header = fmt.Sprintf("Run #%d%s, %s, %s\n%s\n\n", rec.ID, kind, localTime(rec.Started), rec.Status, orDefault(humanError(rec.Summary), ""))
		page = &engine.PageRun{Title: fmt.Sprintf("Run #%d%s, %s: %s. %s", rec.ID, kind, localTime(rec.Started), rec.Status, humanError(rec.Summary)), State: overlay}
		if evs, _ := st.Events(rec.ID); len(evs) > 0 {
			t0 := evs[0].At
			for _, e := range evs {
				page.Log = append(page.Log, engine.LogLine{Offset: fmt.Sprintf("%.2fs", e.At.Sub(t0).Seconds()), Step: e.Step, Kind: e.Kind, Text: humanError(e.Text)})
			}
		}
	}
	if *htmlOut != "" {
		if err := os.WriteFile(*htmlOut, []byte(engine.RenderHTML(g, page)), 0o644); err != nil {
			return err
		}
		abs, _ := filepath.Abs(*htmlOut)
		fmt.Println("Wrote", abs)
		fmt.Println("Open it in a browser.")
		return nil
	}
	fmt.Print(header + engine.RenderText(g, overlay, colorOK()))
	return nil
}

func overlayOf(rec store.RunRecord) map[string]engine.StepState {
	var steps []engine.StepResult
	_ = json.Unmarshal([]byte(rec.Steps), &steps)
	out := map[string]engine.StepState{}
	for _, s := range steps {
		note := s.Note
		if s.Plan != "" {
			note = s.Plan
		}
		if s.Status == "failed" {
			note = humanError(s.Error)
		}
		out[s.ID] = engine.StepState{Status: s.Status, Millis: s.Millis, Note: oneLineShort(note, 70)}
	}
	return out
}

func oneLineShort(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func colorOK() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	n := fs.Int("n", 10, "how many runs to list")
	name, rest := splitFirst(args)
	var runID int64
	for i := 0; i < len(rest); i++ { // a bare number is a run id
		if v, err := strconv.ParseInt(rest[i], 10, 64); err == nil && !strings.HasPrefix(rest[i], "-") && (i == 0 || !strings.HasPrefix(rest[i-1], "-")) {
			runID = v
			rest = append(rest[:i], rest[i+1:]...)
			break
		}
	}
	if err := fs.Parse(rest); err != nil || name == "" {
		return usageErr("sentinel logs <name> [run id] [-n 10]")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if !knownWorkflow(st, name) {
		return fmt.Errorf("no workflow named %q%s", name, knownNames(st))
	}
	if runID == 0 && len(os.Args) > 3 && fs.NArg() == 0 && !hasFlag(args, "-n") {
		// `sentinel logs name` alone shows the latest run in full, because that is what people want.
	}
	if runID == 0 && !hasFlag(args, "-n") {
		rec, ok, _ := st.RunByID(name, 0)
		if !ok {
			fmt.Println("No runs yet for", name+".")
			return nil
		}
		return printRunLog(st, rec)
	}
	if runID > 0 {
		rec, ok, _ := st.RunByID(name, runID)
		if !ok {
			return fmt.Errorf("no run #%d for %s", runID, name)
		}
		return printRunLog(st, rec)
	}
	runs, _ := st.Runs(name, *n)
	if len(runs) == 0 {
		fmt.Println("No runs yet for", name+".")
		return nil
	}
	fmt.Printf("Runs of %s (newest first). Show one with: sentinel logs %s <id>\n\n", name, name)
	for _, r := range runs {
		kind := ""
		if r.Dry {
			kind = " dry"
		}
		fmt.Printf("  #%-4d %s  %-10s%-4s %s\n", r.ID, localTime(r.Started), r.Status, kind, oneLineShort(humanError(r.Summary), 70))
	}
	return nil
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f || strings.HasPrefix(a, f+"=") {
			return true
		}
	}
	return false
}

func printRunLog(st *store.Store, rec store.RunRecord) error {
	kind := ""
	if rec.Dry {
		kind = ", dry run"
	}
	fmt.Printf("Run #%d of %s%s: %s\n", rec.ID, rec.Workflow, kind, rec.Status)
	if rec.Summary != "" {
		fmt.Println(humanError(rec.Summary))
	}
	evs, err := st.Events(rec.ID)
	if err != nil {
		return err
	}
	fmt.Println()
	if len(evs) == 0 {
		fmt.Println("(this run has no log lines; it ran before logging was added)")
		return nil
	}
	var t0 time.Time
	for _, e := range evs {
		if t0.IsZero() {
			t0 = e.At
		}
		who := e.Step
		if who == "" {
			who = "-"
		}
		mark := " "
		switch e.Kind {
		case "error":
			mark = "✗"
		case "warn":
			mark = "!"
		}
		fmt.Printf("%s +%6.2fs  %-16s %s\n", mark, e.At.Sub(t0).Seconds(), who, humanError(e.Text))
	}
	return nil
}
