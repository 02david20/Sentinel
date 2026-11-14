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

// hintBlocks explains "unknown block X" when X is a folder that is waiting for approval.
func hintBlocks(errs []string) {
	shown := map[string]bool{}
	for _, e := range errs {
		for _, in := range notLoaded {
			if strings.Contains(e, "unknown block "+in.Name) && !shown[in.Name] {
				shown[in.Name] = true
				fmt.Printf("  note: block %s exists but is %s. Review and approve it:  sentinel block check %s && sentinel block approve %s\n", in.Name, in.State, in.Name, in.Name)
			}
		}
	}
}

const blockUsage = `usage: sentinel block <command>

  list                    every block folder and its state
  new <Name> [--lang python|sh]   scaffold a working example into the pending folder
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
		name, frest := splitFirst(rest)
		if err := fs.Parse(frest); err != nil || name == "" {
			return usageErr("sentinel block new <Name> [--lang python|sh]")
		}
		dir, err := st.Scaffold(name, *lang)
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
		return exitErr{1, "approval needs a person at a terminal: run `sentinel block approve " + in.Name + "` yourself (an agent or script cannot approve its own block)"}
	}
	if in.Manifest == nil {
		return exitErr{1, fmt.Sprintf("%s: %s", in.Name, in.Problem)}
	}
	for _, r := range plugin.Check(context.Background(), in) {
		if !r.OK {
			return exitErr{1, fmt.Sprintf("%s does not pass its checks (%s: %s). Run `sentinel block check %s`.", in.Name, r.Name, r.Why, in.Name)}
		}
	}
	m := in.Manifest
	fmt.Printf("Approve %s?\n  effect:  %s\n  runs:    %s\n  folder:  %s\n", in.Name, effectWords(m.Effect), strings.Join(m.Run, " "), in.Dir)
	if len(m.Env) > 0 {
		fmt.Printf("  can read environment: %s\n", strings.Join(m.Env, ", "))
	}
	fmt.Printf("  (review the code first:  sentinel block show %s)\nApprove? [y/N] ", in.Name)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Println("Not approved.")
		return exitErr{1, ""}
	}
	if err := st.Approve(in.Name); err != nil {
		return err
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
