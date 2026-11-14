// Command sentinel runs workflows made of compiled-in blocks. A scheduled run uses no model: the
// agent is started only by the Agent.Escalate block.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"sentinel/internal/blocks"
	"sentinel/internal/engine"
	"sentinel/internal/store"
)

var version = "0.1.0"

const usage = `sentinel: run recurring checks on a timer. It starts an agent only when something needs a decision.

Get started:
  sentinel                                   with no arguments in a terminal: the interactive view
  sentinel doctor                            check your logins and settings
  sentinel run <name|file> --dry-run         see what a run would do; nothing is sent or saved
  sentinel install <file>                    schedule it
  sentinel status                            is it working? what did it do? what is next?

All commands:
  sentinel run <name|file> [--dry-run] [-v]  run once, right now
  sentinel graph <name|file> [--run [id]] [--mermaid] draw the workflow; --run shows what a run did
  sentinel logs <name> [id] [-n 10]          the log of a run (the latest by default), or the list of runs
  sentinel status [<name>]                   every workflow, or the details of one
  sentinel ack <name> [--token <t>]          mark an escalation as handled
  sentinel pause|resume <name>               stop or restart scheduled runs
  sentinel install <file> [--no-enable]      write and enable the timer
  sentinel uninstall <name>                  stop and remove the timer
  sentinel tui                               interactive view (the same as plain sentinel in a terminal)
  sentinel validate <file>                   check a workflow definition
  sentinel blocks [--json]                   list the steps a workflow can use
  sentinel block <list|new|add|show|check|approve|revoke|remove>   add your own steps (see docs/BLOCKS.md)
  sentinel version

Exit codes: 0 ok | 1 failed | 2 wrong usage | 3 paused | 4 a connector login failed
Settings (optional): SENTINEL_STATE_DIR, SENTINEL_WEBHOOK_URL (or ~/.config/sentinel/webhook), SENTINEL_AGENT_CMD, SENTINEL_BLOCKS_DIR, SENTINEL_APPROVALS_FILE
`

var commands = []string{"block", "graph", "logs", "tui", "run", "status", "ack", "pause", "resume", "install", "uninstall", "validate", "blocks", "doctor", "version", "help"}

func main() {
	if len(os.Args) < 2 {
		if isTerminal() {
			if err := cmdTUI(); err != nil {
				fmt.Fprintln(os.Stderr, "sentinel:", err)
				os.Exit(1)
			}
			return
		}
		fmt.Print(usage)
		return
	}
	loadPlugins()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "tui":
		err = cmdTUI()
	case "block":
		err = cmdBlock(args)
	case "graph":
		err = cmdGraph(args)
	case "logs":
		err = cmdLogs(args)
	case "blocks":
		err = cmdBlocks(args)
	case "validate":
		err = cmdValidate(args)
	case "run":
		err = cmdRun(ctx, args)
	case "status":
		err = cmdStatus(args)
	case "ack":
		err = cmdAck(args)
	case "pause", "resume":
		err = cmdPause(cmd, args)
	case "install":
		err = cmdInstall(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "doctor":
		err = cmdDoctor()
	case "version", "--version", "-v":
		fmt.Println("sentinel", version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "sentinel: unknown command %q%s\n\nRun `sentinel help` for the list of commands.\n", cmd, didYouMean(cmd))
		os.Exit(2)
	}
	if err != nil {
		var ee exitErr
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "sentinel:", err)
		os.Exit(1)
	}
}

type exitErr struct {
	code int
	msg  string
}

func (e exitErr) Error() string { return e.msg }

func usageErr(format string, a ...any) error {
	return exitErr{2, "usage: " + fmt.Sprintf(format, a...)}
}

func didYouMean(cmd string) string {
	best, bestD := "", 3
	for _, c := range commands {
		if d := editDistance(cmd, c); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return ` (did you mean "` + best + `"?)`
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func stateDir() string {
	if d := os.Getenv("SENTINEL_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Dir(store.DefaultPath())
}

func openStore() (*store.Store, error) { return store.Open(filepath.Join(stateDir(), "sentinel.db")) }

func loadWorkflow(path string) (*engine.Package, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no such file: %s", path)
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return engine.Load(b)
}

// resolve turns a workflow name or a file path into a path: an existing file wins, then a
// registered name, then workflows/<name>.yaml next to the current directory.
func resolve(st *store.Store, arg string) (string, error) {
	if fi, err := os.Stat(arg); err == nil && !fi.IsDir() {
		return filepath.Abs(arg)
	}
	if st != nil {
		if p, ok, _ := st.WorkflowPath(arg); ok {
			return p, nil
		}
	}
	for _, c := range []string{filepath.Join("workflows", arg+".yaml"), arg + ".yaml"} {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("no workflow file or name %q%s", arg, knownNames(st))
}

func knownNames(st *store.Store) string {
	if st == nil {
		return ""
	}
	ws, _ := st.Workflows()
	if len(ws) == 0 {
		return " (no workflows are registered yet: run or install one by its file path first)"
	}
	var n []string
	for _, w := range ws {
		n = append(n, w.Name)
	}
	return " (known: " + strings.Join(n, ", ") + ")"
}

// knownWorkflow is true for a registered workflow or one with state (pause flag, pending task).
func knownWorkflow(st *store.Store, name string) bool {
	if _, ok, _ := st.WorkflowPath(name); ok {
		return true
	}
	if p, _ := st.GetPending(name); p != nil {
		return true
	}
	return false
}

func cmdBlocks(args []string) error {
	if len(args) > 0 && args[0] == "--json" {
		b, _ := json.MarshalIndent(engine.Blocks(), "", "  ")
		fmt.Println(string(b))
		return nil
	}
	for _, s := range engine.Blocks() {
		fmt.Printf("%s%s   (%s)\n    %s\n    needs:    %s\n    provides: %s\n\n", s.Name, blockSource(s), effectWords(s.Effect), firstSentence(s.Doc), fmtPorts(s.Inputs), fmtPorts(s.Outputs))
	}
	return nil
}

func effectWords(e engine.Effect) string {
	switch e {
	case engine.Read:
		return "only reads"
	case engine.WriteInternal:
		return "saves state on this machine"
	}
	return "changes something outside: needs an idempotency_key"
}

func fmtPorts(m map[string]string) string {
	var out []string
	for k, v := range m {
		out = append(out, k+":"+v)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, "  ")
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

func cmdValidate(args []string) error {
	if len(args) != 1 {
		return usageErr("sentinel validate <file>")
	}
	p, err := loadWorkflow(args[0])
	if err != nil {
		return err
	}
	if errs := engine.Validate(p.Workflow); len(errs) > 0 {
		fmt.Printf("%s has %d problem(s):\n", args[0], len(errs))
		for _, e := range errs {
			fmt.Println("  -", e)
		}
		hintBlocks(errs)
		return exitErr{1, ""}
	}
	fmt.Printf("ok: %s (%d steps)\n", p.Workflow.Name, len(p.Workflow.Steps))
	return nil
}

// ---- connectors ----

func services() map[string]any {
	svc := map[string]any{}
	if w, err := blocks.LoadWebhook(); err == nil {
		svc["webhook"] = w
	}
	return svc
}

// ---- run ----

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dry := fs.Bool("dry-run", false, "see what would happen; nothing is sent or saved")
	verbose := fs.Bool("v", false, "show every step")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	target, rest := splitFirst(args)
	if err := fs.Parse(rest); err != nil || target == "" {
		return usageErr("sentinel run <name|file> [--dry-run] [-v] [--json]")
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
	return runOne(ctx, st, path, *dry, *verbose, *asJSON)
}

func runOne(ctx context.Context, st *store.Store, path string, dry, verbose, asJSON bool) error {
	p, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	if errs := engine.Validate(p.Workflow); len(errs) > 0 {
		fmt.Printf("%s has %d problem(s):\n", path, len(errs))
		for _, e := range errs {
			fmt.Println("  -", e)
		}
		hintBlocks(errs)
		return exitErr{1, ""}
	}
	_ = st.RegisterWorkflow(p.Workflow.Name, path)
	svc := services()
	name := p.Workflow.Name
	res, err := engine.Run(ctx, p, engine.Options{Store: st, StateDir: stateDir(), Services: svc, Dry: dry,
		Log: func(f string, a ...any) {
			if verbose {
				fmt.Fprintf(os.Stderr, "  "+f+"\n", a...)
			}
		}})
	if err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		printRun(name, res, dry, verbose)
	}
	switch res.Status {
	case "ok":
		return nil
	case "blocked":
		return exitErr{1, ""}
	case "paused":
		return exitErr{3, ""}
	case "auth-failed":
		return exitErr{4, ""}
	}
	return exitErr{1, ""}
}

func printRun(name string, res *engine.Result, dry, verbose bool) {
	switch res.Status {
	case "paused":
		fmt.Printf("%s is paused (%s).\n", name, strings.TrimPrefix(res.Summary, "workflow is paused: "))
		fmt.Printf("Fix:  sentinel resume %s\n", name)
		return
	case "blocked":
		fmt.Printf("%s: another run is in progress; nothing was done.\n", name)
		return
	case "auth-failed":
		fmt.Printf("%s: a connector's credentials are missing or expired. Fix them, then: sentinel resume %s\n", name, name)
		return
	case "failed":
		fmt.Printf("%s: failed. %s\n", name, humanError(res.Summary))
		if !dry {
			fmt.Println("It will try again at the next scheduled run. Details: sentinel status", name)
		}
		return
	}
	if dry {
		fmt.Printf("Dry run of %s (nothing is sent or saved):\n", name)
		any := false
		for _, s := range res.Steps {
			if s.Plan != "" && s.Status != "skipped" {
				fmt.Println("  would:", s.Plan)
				any = true
			}
		}
		if !any {
			fmt.Println("  nothing to do.")
		}
	}
	if res.Summary != "" {
		fmt.Printf("%s: %s\n", name, res.Summary)
	} else if !dry {
		fmt.Printf("%s: ok.\n", name)
	}
	if !dry {
		for _, s := range res.Steps {
			if s.Note != "" && s.Status == "ran" {
				fmt.Println("  -", s.Note)
			}
		}
	}
	if verbose {
		fmt.Println()
		for _, s := range res.Steps {
			line := fmt.Sprintf("  %-8s %-16s %-18s %4dms", s.Status, s.ID, s.Block, s.Millis)
			if s.Error != "" {
				line += "  " + s.Error
			}
			fmt.Println(line)
		}
	}
}

// humanError removes engine vocabulary from a failure line.
func humanError(s string) string {
	r := strings.NewReplacer("idempotency key", "event marker", "idempotency_key", "event marker")
	s = r.Replace(s)
	return strings.TrimPrefix(s, "failed: ")
}

// splitFirst takes the first non-flag argument as the target, so flags may come before or after.
func splitFirst(args []string) (string, []string) {
	var rest []string
	target := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if target == "" && !strings.HasPrefix(a, "-") {
			target = a
			continue
		}
		rest = append(rest, a)
		if a == "--token" || a == "-token" || a == "--bin" || a == "-bin" { // flags that take a value
			if i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		}
	}
	return target, rest
}

// ---- status ----

func cmdStatus(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if len(args) == 1 {
		if !knownWorkflow(st, args[0]) {
			return fmt.Errorf("no workflow named %q%s", args[0], knownNames(st))
		}
		return statusDetail(st, args[0])
	}
	ws, _ := st.Workflows()
	if len(ws) == 0 {
		fmt.Println("No workflows yet. Create a YAML file in workflows/ and try:  sentinel run <file> --dry-run")
		return nil
	}
	for _, w := range ws {
		statusLine(st, w.Name)
	}
	return nil
}

func statusLine(st *store.Store, name string) {
	paused, why, _ := st.Paused(name)
	timer := timerState(name)
	fmt.Printf("%s   %s\n", name, timer)
	if paused {
		fmt.Printf("  PAUSED (%s). Resume: sentinel resume %s\n", why, name)
	}
	if r, ok, _ := st.LastOK(name); ok {
		fmt.Printf("  last check %s (%s ago): %s\n", localTime(r.Started), ago(r.Started), orDefault(r.Summary, "ok"))
	} else {
		fmt.Println("  last check: never")
	}
	if p, _ := st.GetPending(name); p != nil {
		fmt.Printf("  WAITING ON YOU: an escalation from %s (%s ago)\n    task: %s\n    when handled: sentinel ack %s\n", localTime(p.QueuedAt), ago(p.QueuedAt), p.TaskFile, name)
	}
	if runs, _ := st.Runs(name, 1); len(runs) > 0 && runs[0].Status != "ok" && runs[0].Status != "auth-failed" && !runs[0].Dry {
		fmt.Printf("  latest run %s: %s\n", runs[0].Status, humanError(runs[0].Summary))
	}
}

func statusDetail(st *store.Store, name string) error {
	statusLine(st, name)
	runs, _ := st.Runs(name, 8)
	if len(runs) == 0 {
		return nil
	}
	fmt.Println("\nRecent runs:")
	for _, r := range runs {
		kind := ""
		if r.Dry {
			kind = " (dry run)"
		}
		fmt.Printf("  %s  %-10s%s  %s\n", localTime(r.Started), r.Status, kind, humanError(r.Summary))
	}
	return nil
}

func timerState(name string) string {
	out, err := exec.Command("systemctl", "--user", "show", "sentinel-"+name+".timer", "-p", "ActiveState", "-p", "NextElapseUSecRealtime").Output()
	if err != nil {
		return "(no timer installed: run `sentinel install` to schedule it)"
	}
	var active, next string
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "ActiveState="):
			active = strings.TrimPrefix(l, "ActiveState=")
		case strings.HasPrefix(l, "NextElapseUSecRealtime="):
			next = strings.TrimPrefix(l, "NextElapseUSecRealtime=")
		}
	}
	if active != "active" {
		return "(timer not running: sentinel install " + name + ")"
	}
	if next == "" {
		return "(timer on)"
	}
	return "(timer on, next run " + next + ")"
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func localTime(s string) string {
	if t := parseTS(s); !t.IsZero() {
		return t.Local().Format("Mon 02 Jan 15:04")
	}
	return s
}

func ago(s string) string {
	t := parseTS(s)
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

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---- ack, pause, resume ----

func cmdAck(args []string) error {
	fs := flag.NewFlagSet("ack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tok := fs.String("token", "", "token of the escalation (optional when only one is waiting)")
	name, rest := splitFirst(args)
	if err := fs.Parse(rest); err != nil || name == "" {
		return usageErr("sentinel ack <name> [--token <token>]")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if !knownWorkflow(st, name) {
		return fmt.Errorf("no workflow named %q%s", name, knownNames(st))
	}
	p, _ := st.GetPending(name)
	if p == nil {
		fmt.Println("Nothing is waiting on you for", name+".")
		return nil
	}
	t := *tok
	if t == "" {
		t = p.Token
	}
	if err := st.Ack(name, t); err != nil {
		return fmt.Errorf("that token is not the pending escalation of %s", name)
	}
	fmt.Println("Marked as handled. The next run can start a new escalation.")
	return nil
}

func cmdPause(verb string, args []string) error {
	if len(args) != 1 {
		return usageErr("sentinel %s <name>", verb)
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if !knownWorkflow(st, args[0]) {
		return fmt.Errorf("no workflow named %q%s", args[0], knownNames(st))
	}
	if err := st.SetPaused(args[0], verb == "pause", "paused by `sentinel pause`"); err != nil {
		return err
	}
	if verb == "pause" {
		fmt.Printf("Paused %s. Scheduled runs do nothing until: sentinel resume %s\n", args[0], args[0])
	} else {
		fmt.Printf("Resumed %s. It runs at the next scheduled time (or now: sentinel run %s).\n", args[0], args[0])
	}
	return nil
}

// ---- doctor ----

func cmdDoctor() error {
	bad := 0
	row := func(ok bool, what, detail, fix string) {
		mark := "ok     "
		if !ok {
			mark = "MISSING"
			bad++
		}
		fmt.Printf("%-8s %-14s %s\n", mark, what, detail)
		if !ok && fix != "" {
			fmt.Printf("                        -> %s\n", fix)
		}
	}
	if _, err := blocks.LoadWebhook(); err == nil {
		row(true, "Webhook", "found", "")
	} else {
		row(false, "Webhook", "not set (alerts cannot be delivered)", "put the webhook URL in ~/.config/sentinel/webhook, or set SENTINEL_WEBHOOK_URL")
	}
	if agent := os.Getenv("SENTINEL_AGENT_CMD"); agent == "" {
		row(true, "Agent command", "not set (only needed for delivery: command)", "")
	} else if p, err := exec.LookPath(agent); err == nil {
		row(true, "Agent command", p, "")
	} else {
		row(false, "Agent command", "not found: "+agent, "set SENTINEL_AGENT_CMD to the executable's full path")
	}
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err == nil {
		row(true, "systemd (user)", "available", "")
	} else {
		row(false, "systemd (user)", "not available (needed for the timer)", "systemd user services are required (Linux)")
	}
	if st, err := openStore(); err == nil {
		st.Close()
		row(true, "State folder", stateDir(), "")
	} else {
		row(false, "State folder", err.Error(), "check that "+stateDir()+" is writable")
	}
	if bad > 0 {
		return exitErr{1, fmt.Sprintf("\n%d thing(s) to fix.", bad)}
	}
	fmt.Println("\nAll good.")
	return nil
}

func validateWorkflow(p *engine.Package) []string { return engine.Validate(p.Workflow) }
