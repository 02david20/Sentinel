package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sentinel/internal/engine"
)

const (
	maxReply  = 4 << 20 // bytes of stdout a block may return
	maxStderr = 4 << 10
)

// extBlock adapts a manifest to engine.Block.
type extBlock struct {
	m   *Manifest
	dir string
}

func (b extBlock) Spec() engine.Spec { return b.m.Spec() }

// request is the single JSON document written to the block's stdin.
type request struct {
	Protocol int            `json:"protocol"`
	Block    string         `json:"block"`
	Workflow string         `json:"workflow"`
	Dry      bool           `json:"dry"`
	Inputs   map[string]any `json:"inputs"`
}

// reply is the single JSON document the block writes to stdout.
type reply struct {
	Outputs map[string]any `json:"outputs"`
	Plan    string         `json:"plan"`
	Note    string         `json:"note"`
	Skipped bool           `json:"skipped"`
	Error   string         `json:"error"`
}

func (b extBlock) Run(env *engine.Env, in map[string]any) (map[string]any, error) {
	m := b.m
	// A block that changes things is not executed in a dry run unless it says it can plan
	// without acting; otherwise a dry run could change the world.
	if env.Dry && m.Effect != engine.Read && !m.SupportsDry {
		out := engine.ZeroOutputs(m.Outputs)
		out["_plan"] = "run external block " + m.Name + " (not executed in a dry run)"
		return out, nil
	}
	rep, err := execute(env.Ctx, b.dir, m, request{ProtocolNumber, m.Name, env.Workflow, env.Dry, in})
	if err != nil {
		if m.Effect == engine.WriteExternal && errors.Is(err, context.DeadlineExceeded) {
			return nil, engine.AmbiguousError{Err: err} // it may have acted before the timeout
		}
		return nil, err
	}
	out := engine.ZeroOutputs(m.Outputs)
	for k := range m.Outputs {
		if v, ok := rep.Outputs[k]; ok {
			out[k] = v
		}
	}
	if rep.Plan != "" {
		out["_plan"] = rep.Plan
	}
	if rep.Note != "" {
		out["_note"] = rep.Note
	}
	if rep.Skipped {
		out["_skipped"] = true
	}
	return out, nil
}

// execute runs the block once and parses its reply.
func execute(ctx context.Context, dir string, m *Manifest, req request) (*reply, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.timeout())*time.Second)
	defer cancel()
	argv := append([]string(nil), m.Run...)
	if strings.Contains(argv[0], "/") && !filepath.IsAbs(argv[0]) {
		argv[0] = filepath.Join(dir, argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv(m)
	cmd.SysProcAttr = newGroup()
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	body, _ := json.Marshal(req)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: maxReply}
	cmd.Stderr = &limitWriter{w: &stderr, n: maxStderr}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %d s: %w", m.Name, m.timeout(), ctx.Err())
	}
	var rep reply
	parseErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &rep)
	if err != nil {
		msg := strings.TrimSpace(rep.Error)
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s failed: %s", m.Name, oneLine(msg, 400))
	}
	if parseErr != nil {
		return nil, fmt.Errorf("%s did not return one JSON object on stdout (%v): %q", m.Name, parseErr, oneLine(stdout.String(), 120))
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("%s failed: %s", m.Name, oneLine(rep.Error, 400))
	}
	return &rep, nil
}

// scrubbedEnv passes only what a block needs: a minimal PATH and locale, plus the variables its
// manifest lists. Secrets of other blocks (and of Sentinel) are not visible.
func scrubbedEnv(m *Manifest) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=" + orDefault(os.Getenv("LANG"), "C.UTF-8"), "SENTINEL_BLOCK=" + m.Name}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	for _, k := range m.Env {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// limitWriter keeps the first n bytes and silently drops the rest, so a runaway block cannot
// fill memory. Returning len(p) keeps the child from failing on a closed pipe.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
