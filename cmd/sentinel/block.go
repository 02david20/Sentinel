package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sentinel/internal/engine"
	"sentinel/internal/plugin"
)

func blocksDir() string {
	if d := os.Getenv("SENTINEL_BLOCKS_DIR"); d != "" {
		return d
	}
	return filepath.Join(stateDir(), "blocks.d")
}

func pluginStore() *plugin.Store {
	return plugin.Open(blocksDir(), os.Getenv("SENTINEL_APPROVALS_FILE"))
}

// notLoaded holds the block folders that exist but were not registered, so a workflow that uses
// one can be told what to do about it.
var notLoaded []plugin.Info

// loadPlugins registers every approved block. It never fails a command: a broken folder only
// means that block is unavailable.
func loadPlugins() {
	all, err := pluginStore().Register()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sentinel: could not read blocks:", err)
		return
	}
	for _, in := range all {
		if in.State != plugin.StateApproved {
			notLoaded = append(notLoaded, in)
		}
	}
}

// explain rewrites validation errors that are caused by a block waiting for approval: "unknown
// block X" becomes a sentence that says what to do, and the errors that only follow from it
// (references to that step) are dropped. needsApproval is true when every error was of that kind.
func explain(errs []string) (out []string, needsApproval bool) {
	hidden := map[string]bool{}
	for _, in := range notLoaded {
		hidden[in.Name] = true
	}
	approvalOnly := true
	for _, e := range errs {
		matched := false
		for _, in := range notLoaded {
			if strings.HasSuffix(e, "unknown block "+in.Name) {
				matched = true
				why := map[string]string{
					plugin.StatePending:    "was proposed but nobody approved it yet",
					plugin.StateUnapproved: "is installed but was never approved",
					plugin.StateModified:   "changed since you approved it",
					plugin.StateInvalid:    "has a problem: " + in.Problem,
					plugin.StateConflict:   "cannot be used: " + in.Problem,
				}[in.State]
				fix := fmt.Sprintf("review and approve it:  sentinel block show %s  then  sentinel block approve %s", in.Name, in.Name)
				if in.State == plugin.StateInvalid || in.State == plugin.StateConflict {
					fix = "fix it:  sentinel block check " + in.Name
				}
				out = append(out, fmt.Sprintf("%s: block %s %s. To fix, %s", strings.SplitN(e, ": unknown block", 2)[0], in.Name, why, fix))
				if in.State == plugin.StateInvalid || in.State == plugin.StateConflict {
					approvalOnly = false
				}
			}
		}
		if matched {
			continue
		}
		if strings.Contains(e, "refers to a step that does not run before it") && len(notLoaded) > 0 {
			continue // follows from the missing block above
		}
		approvalOnly = false
		out = append(out, e)
	}
	return out, approvalOnly && len(out) > 0
}

const blockUsage = `usage: sentinel block <command>

  list                    every block folder and its state
  new <Group.Action> [--lang python|sh] [--effect read|write-internal|write-external]
                          scaffold a working example (with tests) into the pending folder
  add <folder>            copy a block folder written elsewhere into the pending folder
  show <Name>             print the manifest and every file, to review before approving
  check <Name>            validate the manifest and run its tests
  approve <Name>          pin the reviewed contents so workflows can use the block (asks you)
  revoke <Name>           stop loading a block (keeps the files)
  remove <Name>           delete a block folder and its approval

A block is usable in workflows only when approved and unchanged since. Folder: ` + "`sentinel block list`" + ` shows it.
`

func cmdBlock(args []string) error {
	if len(args) == 0 {
		fmt.Print(blockUsage)
		return exitErr{2, ""}
	}
	st := pluginStore()
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return blockList(st)
	case "new":
		fs := flag.NewFlagSet("block new", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		lang := fs.String("lang", "python", "python or sh")
		effect := fs.String("effect", "read", "read, write-internal or write-external")
		newUsage := usageErr("sentinel block new <Group.Action> [--lang python|sh] [--effect read|write-internal|write-external]")
		// flags may come before or after the name
		if err := fs.Parse(rest); err != nil || fs.NArg() == 0 {
			return newUsage
		}
		name := fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() != 0 {
			return newUsage
		}
		dir, err := st.Scaffold(name, *lang, *effect)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s\nEdit block.yaml and the script, then:\n  sentinel block check %s\n  sentinel block approve %s\n", dir, name, name)
		return nil
	case "add":
		if len(rest) != 1 {
			return usageErr("sentinel block add <folder>")
		}
		dest, err := st.Add(rest[0])
		if err != nil {
			return err
		}
		name := filepath.Base(dest)
		fmt.Printf("Added %s as pending (not usable yet).\n  sentinel block check %s\n  sentinel block approve %s\n", name, name, name)
		return nil
	case "show", "check", "approve", "revoke", "remove":
		if len(rest) != 1 {
			return usageErr("sentinel block %s <Name>", verb)
		}
		return blockOne(st, verb, rest[0])
	}
	fmt.Print(blockUsage)
	return exitErr{2, ""}
}

func blockList(st *plugin.Store) error {
	all, err := st.List()
	if err != nil {
		return err
	}
	fmt.Println("Blocks folder:", st.Root)
	if len(all) == 0 {
		fmt.Println("No external blocks yet. Create one:  sentinel block new My.Block")
		return nil
	}
	fmt.Println()
	for _, in := range all {
		eff := ""
		if in.Manifest != nil {
			eff = effectWords(in.Manifest.Effect)
		}
		fmt.Printf("%-24s %-11s %s\n", in.Name, in.State, eff)
		if in.Problem != "" {
			fmt.Printf("    %s\n", in.Problem)
		}
	}
	return nil
}

func blockOne(st *plugin.Store, verb, name string) error {
	switch verb {
	case "revoke":
		if err := st.Revoke(name); err != nil {
			return err
		}
		fmt.Printf("Revoked %s. It no longer loads; the files are kept.\n", name)
		return nil
	case "remove":
		if err := st.Remove(name); err != nil {
			return err
		}
		fmt.Printf("Removed %s.\n", name)
		return nil
	}
	in, err := st.Find(name)
	if err != nil {
		return err
	}
	switch verb {
	case "show":
		return showBlock(in)
	case "check":
		return checkBlock(in)
	}
	return approveBlock(st, in)
}

func showBlock(in *plugin.Info) error {
	fmt.Printf("%s   [%s]   %s\n\n", in.Name, in.State, in.Dir)
	var files []string
	_ = filepath.Walk(in.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && !strings.HasSuffix(p, ".pyc") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(in.Dir, f)
		b, _ := os.ReadFile(f)
		fmt.Printf("----- %s -----\n%s\n", rel, strings.TrimRight(string(b), "\n"))
	}
	return nil
}

func checkBlock(in *plugin.Info) error {
	if in.Manifest == nil {
		return exitErr{1, fmt.Sprintf("%s: %s", in.Name, in.Problem)}
	}
	m := in.Manifest
	fmt.Printf("Checking %s (%s)\n", in.Name, effectWords(m.Effect))
	fmt.Println("  note: this runs the block's own code on this machine (without its env secrets). Read it first if you did not write it:  sentinel block show " + in.Name)
	bad := 0
	for _, r := range plugin.Check(context.Background(), in) {
		switch {
		case !r.OK:
			bad++
			fmt.Printf("  FAIL  %s: %s\n", r.Name, r.Why)
		case r.Why != "":
			fmt.Printf("  ok    %s (%s)\n", r.Name, r.Why)
		default:
			fmt.Printf("  ok    %s\n", r.Name)
		}
	}
	if bad > 0 {
		return exitErr{1, fmt.Sprintf("\n%d test(s) failed. Fix the block, then check again.", bad)}
	}
	fmt.Printf("\n%s passed. Review it with `sentinel block show %s`, then `sentinel block approve %s`.\n", in.Name, in.Name, in.Name)
	return nil
}

func approveBlock(st *plugin.Store, in *plugin.Info) error {
	if !isTerminal() {
		return exitErr{1, "approval needs a person at a terminal: run `sentinel block approve " + in.Name + "` yourself. (This stops a script from approving by accident; it is not a security boundary, so keep agents away from the approvals file.)"}
	}
	if in.Manifest == nil {
		return exitErr{1, fmt.Sprintf("%s: %s", in.Name, in.Problem)}
	}
	m := in.Manifest
	rv, err := st.Review(in)
	if err != nil {
		return exitErr{1, fmt.Sprintf("%s cannot be reviewed: %v", in.Name, err)}
	}
	// Show everything first. Nothing of the block has been run at this point.
	fmt.Printf("Review %s  (%s)\n", in.Name, effectWords(m.Effect))
	fmt.Printf("  runs:      %s\n", strings.Join(m.Run, " "))
	fmt.Printf("  files:     ")
	total := 0
	var parts []string
	for _, f := range rv.Files {
		total += f.Lines
		parts = append(parts, fmt.Sprintf("%s (%d lines)", f.Name, f.Lines))
	}
	fmt.Printf("%s\n  sha256:    %s\n", strings.Join(parts, ", "), rv.Hash[:16])
	if len(m.Env) > 0 {
		fmt.Printf("  can read:  %s   <- environment variables, including secrets, in real runs\n", strings.Join(m.Env, ", "))
	} else {
		fmt.Println("  can read:  no environment variables beyond PATH, HOME and LANG")
	}
	if m.SupportsDry {
		fmt.Println("  dry runs:  the block is started in dry runs and must only plan (its own promise)")
	}
	if len(rv.Hints) > 0 {
		fmt.Printf("  code seems to: %s   (text search only, not an analysis)\n", strings.Join(rv.Hints, "; "))
	}
	fmt.Println("  The effect above is the author's own claim. The block is an ordinary process: it can read files")
	fmt.Println("  and use the network as you. Read the code:  sentinel block show " + in.Name)
	switch {
	case rv.Previous && rv.HaveCopy && strings.TrimSpace(rv.Diff) != "":
		fmt.Printf("\nChanged since you last approved it:\n%s\n", rv.Diff)
	case rv.Previous && rv.HaveCopy:
		fmt.Println("\nIdentical to the version you approved before.")
	case rv.Previous:
		fmt.Println("\nYou approved an earlier version, but no copy of it was kept, so no diff is available. Review all of it.")
	default:
		fmt.Println("\nFirst approval of this block.")
	}
	strict := m.Effect == engine.WriteExternal || len(m.Env) > 0
	reader := bufio.NewReader(os.Stdin)
	if strict {
		fmt.Printf("\nThis block changes things outside or reads secrets. Type its name (%s) to approve: ", in.Name)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) != in.Name {
			fmt.Println("Not approved.")
			return exitErr{1, ""}
		}
	} else {
		fmt.Print("\nApprove? [y/N] ")
		line, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("Not approved.")
			return exitErr{1, ""}
		}
	}
	// Only now is the block's code run, for its tests. The hash shown above must still match.
	fmt.Println("Running its tests ...")
	for _, r := range plugin.Check(context.Background(), in) {
		if !r.OK {
			return exitErr{1, fmt.Sprintf("%s does not pass its tests (%s: %s), so it was not approved. Fix it and run `sentinel block check %s`.", in.Name, r.Name, r.Why, in.Name)}
		}
	}
	if err := st.Approve(in.Name, rv.Hash); err != nil {
		return exitErr{1, err.Error()}
	}
	fmt.Printf("Approved %s. Workflows can use it now. If its files change it must be approved again.\n", in.Name)
	return nil
}

// blockSource marks external blocks in `sentinel blocks`.
func blockSource(s engine.Spec) string {
	if s.Source == "external" {
		return " [external]"
	}
	return ""
}
