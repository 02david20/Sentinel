package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// The service exits 3 (paused) and 4 (needs login) on purpose; neither is a unit failure, so the
// journal is not filled with "failed" for a state the user was already told about by the webhook.
var serviceTmpl = template.Must(template.New("s").Parse(`[Unit]
Description=sentinel workflow {{.Name}}

[Service]
Type=oneshot
WorkingDirectory={{.Dir}}
ExecStart={{.Exec}}
SuccessExitStatus=3 4
TimeoutStartSec=30min
Environment=PATH={{.Path}}
{{- if .Agent}}
Environment=SENTINEL_AGENT_CMD={{.Agent}}
{{- end}}
`))

var timerTmpl = template.Must(template.New("t").Parse(`[Unit]
Description=Schedule for sentinel workflow {{.Name}}

[Timer]
OnCalendar={{.Calendar}}
Persistent=true
RandomizedDelaySec=60

[Install]
WantedBy=timers.target
`))

// unitQuote quotes one word of an ExecStart line for systemd: spaces and quotes are escaped and
// % is doubled (systemd expands %-specifiers in unit files).
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return `"` + s + `"`
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bin := fs.String("bin", "", "path of the sentinel binary (default: this program)")
	noEnable := fs.Bool("no-enable", false, "write the timer files but do not start the timer")
	target, rest := splitFirst(args)
	if err := fs.Parse(rest); err != nil || target == "" {
		return usageErr("sentinel install <file> [--no-enable] [--bin path]")
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
	if errs := validateWorkflow(p); len(errs) > 0 {
		fmt.Printf("%s has %d problem(s):\n", path, len(errs))
		for _, e := range errs {
			fmt.Println("  -", e)
		}
		return exitErr{1, ""}
	}
	calendar, phrase, err := p.Workflow.Calendar()
	if err != nil {
		return fmt.Errorf("%w; add `every: 4h` to the workflow file", err)
	}
	b := *bin
	if b == "" {
		b, _ = os.Executable()
	}
	b, _ = filepath.Abs(b)
	d, _ := os.UserConfigDir()
	unitDir := filepath.Join(d, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	agent := os.Getenv("SENTINEL_AGENT_CMD")
	data := map[string]string{
		"Name": p.Workflow.Name, "Dir": filepath.Dir(path), "Calendar": calendar, "Agent": agent,
		"Exec": unitQuote(b) + " run " + unitQuote(path),
		"Path": strings.ReplaceAll(os.Getenv("PATH"), "%", "%%"),
	}
	unit := "sentinel-" + p.Workflow.Name
	for ext, t := range map[string]*template.Template{".service": serviceTmpl, ".timer": timerTmpl} {
		f, err := os.OpenFile(filepath.Join(unitDir, unit+ext), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		err = t.Execute(f, data)
		f.Close()
		if err != nil {
			return err
		}
	}
	_ = st.RegisterWorkflow(p.Workflow.Name, path)
	if *noEnable {
		fmt.Printf("Wrote the timer for %s (%s) but did not start it.\n", p.Workflow.Name, phrase)
		return nil
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "--now", unit + ".timer"}} {
		out, err := exec.Command("systemctl", append([]string{"--user"}, c...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("could not start the timer (systemctl %s: %s). Run `sentinel doctor` to check systemd", strings.Join(c, " "), strings.TrimSpace(string(out)))
		}
	}
	fmt.Printf("Scheduled %s: %s", p.Workflow.Name, phrase)
	if out, err := exec.Command("systemctl", "--user", "show", unit+".timer", "-p", "NextElapseUSecRealtime", "--value").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		fmt.Printf(", next run %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf(".\n  Check:   sentinel status\n  Stop:    sentinel pause %s     (or remove it: sentinel uninstall %s)\n", p.Workflow.Name, p.Workflow.Name)
	return nil
}

func cmdUninstall(args []string) error {
	if len(args) != 1 {
		return usageErr("sentinel uninstall <name>")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if !knownWorkflow(st, args[0]) {
		return fmt.Errorf("no workflow named %q%s", args[0], knownNames(st))
	}
	unit := "sentinel-" + args[0]
	_ = exec.Command("systemctl", "--user", "disable", "--now", unit+".timer").Run()
	d, _ := os.UserConfigDir()
	for _, ext := range []string{".service", ".timer"} {
		_ = os.Remove(filepath.Join(d, "systemd", "user", unit+ext))
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Printf("Removed the timer for %s. Its history stays; the workflow file is untouched.\n", args[0])
	return nil
}
