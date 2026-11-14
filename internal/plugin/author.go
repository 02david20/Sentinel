package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"sentinel/internal/engine"
)

const pyTemplate = `#!/usr/bin/env python3
"""%s: one JSON request on stdin, one JSON reply on stdout.

request: {"protocol": 1, "block": "...", "workflow": "...", "dry": false, "inputs": {...}}
reply:   {"outputs": {...}, "note": "what happened", "plan": "what a dry run would do"}
To fail, print a message to stderr and exit 1 (or reply {"error": "..."}).
"""
import json
import sys

req = json.load(sys.stdin)
inputs = req["inputs"]

# TODO: do the work here. Use only the inputs declared in block.yaml.
text = str(inputs.get("text", ""))
if not text:
    print("text is empty", file=sys.stderr)
    sys.exit(1)
%s
json.dump({"outputs": {"result": text.upper()}, "note": "converted %%d characters" %% len(text)}, sys.stdout)
`

const pyDry = `
if req["dry"]:  # a dry run must not change anything: only say what would happen
    json.dump({"outputs": {"result": ""}, "plan": "convert %d characters" % len(text)}, sys.stdout)
    sys.exit(0)
`

const shTemplate = `#!/bin/sh
# %s: one JSON request on stdin, one JSON reply on stdout. jq is used to read and write JSON.
# To fail, print a message to stderr and exit 1.
set -eu
req=$(cat)
text=$(printf '%%s' "$req" | jq -r '.inputs.text // ""')
if [ -z "$text" ]; then echo "text is empty" >&2; exit 1; fi
%s
jq -n --arg r "$(printf '%%s' "$text" | tr '[:lower:]' '[:upper:]')" '{outputs: {result: $r}, note: "converted"}'
`

const shDry = `if [ "$(printf '%s' "$req" | jq -r '.dry')" = "true" ]; then
  jq -n '{outputs: {result: ""}, plan: "convert the text"}'; exit 0
fi`

const manifestTemplate = `# A Sentinel block. Docs: docs/BLOCKS.md
name: %s
doc: >-
  Say what this block does in a sentence or two. Anyone writing a workflow reads this.
effect: %s            # read | write-internal | write-external (external needs an idempotency_key in workflows)
inputs:
  text: string          # name: type  (string, number, bool, list, map, any; add ? when optional)
outputs:
  result: string
run: [%s]
timeout: 30             # seconds
# env: [MY_API_TOKEN]   # environment variables the block may read in real runs (not passed to "block check")
%stests:                  # run by "sentinel block check". Cover the happy path AND a failure.
%s`

const readTests = `  - name: shouts
    in: { text: hello }
    out: { result: HELLO }
  - name: refuses empty text
    in: { text: "" }
    error: empty
`

const writeTests = `  - name: dry run plans, changes nothing   # blocks that change things are only tested with dry: true
    dry: true
    in: { text: hello }
    out: { result: "" }
  - name: refuses empty text
    dry: true
    in: { text: "" }
    error: empty
`

// Scaffold writes a working example block into the pending folder. effect is read (default),
// write-internal or write-external; the latter two get a dry-run branch and dry-run tests.
func (s *Store) Scaffold(name, lang, effect string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("block name %q must look like Http.Get: capitalised words joined by dots (Group.Action)", name)
	}
	switch effect {
	case "", "read":
		effect = "read"
	case "write-internal", "write-external":
	default:
		return "", fmt.Errorf("--effect must be read, write-internal or write-external")
	}
	if engine.IsRegistered(name) {
		return "", fmt.Errorf("a block named %s already exists", name)
	}
	dir := filepath.Join(s.PendingDir(), name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	}
	writes := effect != "read"
	var file, code, runCmd string
	switch lang {
	case "", "python":
		dry := ""
		if writes {
			dry = pyDry
		}
		file, code, runCmd = "block.py", fmt.Sprintf(pyTemplate, name, dry), "python3, block.py"
	case "sh":
		dry := ""
		if writes {
			dry = shDry
		}
		file, code, runCmd = "block.sh", fmt.Sprintf(shTemplate, name, dry), "./block.sh"
	default:
		return "", fmt.Errorf("--lang must be python or sh")
	}
	extra, tests := "", readTests
	if writes {
		extra, tests = "supports_dry: true      # the block can plan without acting (it receives dry: true)\n", writeTests
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(fmt.Sprintf(manifestTemplate, name, effect, runCmd, extra, tests)), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(code), 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Add copies a block folder written elsewhere into the pending folder.
func (s *Store) Add(src string) (string, error) {
	m, err := ParseManifest(src)
	if err != nil {
		return "", err
	}
	if engine.IsRegistered(m.Name) {
		return "", fmt.Errorf("a block named %s already exists", m.Name)
	}
	dest := filepath.Join(s.PendingDir(), m.Name)
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s is already pending; remove it first (sentinel block remove %s)", m.Name, m.Name)
	}
	if _, err := HashDir(src); err != nil { // refuses symbolic links
		return "", err
	}
	if err := copyDir(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			if fi.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, fi.Mode().Perm())
	})
}

// TestResult is the outcome of one fixture.
type TestResult struct {
	Name string
	OK   bool
	Why  string
}

// Check runs the block's tests (and refuses to pass a block that has none). This executes the
// block's code, so the manifest's env variables are NOT passed (unreviewed code never sees
// secrets). Blocks that change things are only run with dry: true, enforced here as well as in
// the manifest check.
func Check(ctx context.Context, in *Info) []TestResult {
	m := in.Manifest
	if m == nil {
		return []TestResult{{Name: "manifest", Why: in.Problem}}
	}
	if len(m.Tests) == 0 {
		return []TestResult{{Name: "tests", Why: "the manifest has no tests; add at least one fixture under tests:"}}
	}
	var res []TestResult
	for i, t := range m.Tests {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("test %d", i+1)
		}
		tr := TestResult{Name: name}
		inputs := t.In
		if inputs == nil {
			inputs = map[string]any{}
		}
		if verr := checkInputs(m, inputs); verr != "" {
			tr.Why = verr
			res = append(res, tr)
			continue
		}
		dry := t.Dry
		if m.Effect != engine.Read {
			dry = true
		}
		if dry && m.Effect != engine.Read && !m.SupportsDry {
			tr.OK, tr.Why = true, "skipped: block changes things and has no supports_dry, so it cannot be run in a test"
			res = append(res, tr)
			continue
		}
		rep, err := execute(ctx, in.Dir, m, request{ProtocolNumber, m.Name, "check", dry, inputs}, false)
		switch {
		case t.Error != "":
			if err == nil {
				tr.Why = "expected an error containing " + fmt.Sprintf("%q", t.Error) + " but the block succeeded"
			} else if !strings.Contains(err.Error(), t.Error) {
				tr.Why = fmt.Sprintf("expected an error containing %q, got: %s", t.Error, err)
			} else {
				tr.OK = true
			}
		case err != nil:
			tr.Why = err.Error()
		default:
			if _, serr := shapeOutputs(m, rep.Outputs); serr != nil {
				tr.Why = serr.Error()
			} else {
				tr.OK, tr.Why = compare(m, rep.Outputs, t.Out)
			}
		}
		res = append(res, tr)
	}
	return res
}

// checkInputs mirrors what the validator enforces for a workflow step.
func checkInputs(m *Manifest, in map[string]any) string {
	for k, typ := range m.Inputs {
		if _, ok := in[k]; !ok && !strings.HasSuffix(typ, "?") {
			return fmt.Sprintf("missing input %q (declared in inputs)", k)
		}
	}
	for k := range in {
		if _, ok := m.Inputs[k]; !ok {
			return fmt.Sprintf("input %q is not declared in inputs", k)
		}
	}
	return ""
}

func compare(m *Manifest, got, want map[string]any) (bool, string) {
	for k := range m.Outputs {
		if _, ok := got[k]; !ok {
			return false, fmt.Sprintf("the reply is missing declared output %q", k)
		}
	}
	for k, w := range want {
		if _, declared := m.Outputs[k]; !declared {
			return false, fmt.Sprintf("test expects %q, which is not a declared output", k)
		}
		if !jsonEqual(got[k], w) {
			return false, fmt.Sprintf("output %q is %v, expected %v", k, got[k], w)
		}
	}
	return true, ""
}

// jsonEqual compares a decoded JSON value with a YAML value (numbers differ in Go type).
func jsonEqual(a, b any) bool {
	return reflect.DeepEqual(norm(a), norm(b))
}

func norm(v any) any {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case map[string]any:
		o := map[string]any{}
		for k, x := range t {
			o[k] = norm(x)
		}
		return o
	case map[any]any:
		o := map[string]any{}
		for k, x := range t {
			o[fmt.Sprint(k)] = norm(x)
		}
		return o
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = norm(x)
		}
		return o
	}
	return v
}
