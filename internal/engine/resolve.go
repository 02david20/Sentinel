package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
)

// lookup walks "a.b.c" through nested maps and returns the value.
func lookup(scope map[string]map[string]any, step, path string) (any, error) {
	root, ok := scope[step]
	if !ok {
		return nil, fmt.Errorf("step %q has not run", step)
	}
	var cur any = root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.%s: not a map at %q", step, path, part)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("%s.%s: no field %q", step, path, part)
		}
	}
	return cur, nil
}

// resolve replaces whole-value references "${step.output}" with the referenced value.
func resolve(in map[string]any, scope map[string]map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		rv, err := resolveValue(v, scope)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

func resolveValue(v any, scope map[string]map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if m := refRe.FindStringSubmatch(x); m != nil {
			return lookup(scope, m[1], m[2])
		}
		return interpolate(x, scope)
	case map[string]any:
		return resolve(x, scope)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := resolveValue(e, scope)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

var anyRef = regexp.MustCompile(`\$\{[^}]*\}`)

var interpRe = regexp.MustCompile(`\$\{([a-zA-Z0-9_-]+)\.([a-zA-Z0-9_.-]+)\}`)

// interpolate substitutes references embedded in a longer string.
func interpolate(s string, scope map[string]map[string]any) (string, error) {
	var firstErr error
	out := interpRe.ReplaceAllStringFunc(s, func(ref string) string {
		m := interpRe.FindStringSubmatch(ref)
		v, err := lookup(scope, m[1], m[2])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return ""
		}
		if str, ok := v.(string); ok {
			return str
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
	return out, firstErr
}

func mustInterp(s string, scope map[string]map[string]any) string {
	out, _ := interpolate(s, scope)
	return out
}

// evalWhen evaluates a boolean expression over the step outputs, e.g. `len(found.items) > 0`.
func evalWhen(expression string, scope map[string]map[string]any) (bool, error) {
	env := map[string]any{}
	for k, v := range scope {
		env[k] = v
	}
	prog, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return false, fmt.Errorf("when %q: %w", expression, err)
	}
	r, err := expr.Run(prog, env)
	if err != nil {
		return false, fmt.Errorf("when %q: %w", expression, err)
	}
	switch b := r.(type) {
	case bool:
		return b, nil
	case nil:
		return false, nil // a field of a skipped step is absent: treat as false
	}
	return false, fmt.Errorf("when %q: result is %T, want bool", expression, r)
}

// checkRefs walks a value and reports every ${step.field} that does not point to an output of an
// earlier step. outputs holds the steps seen so far (the current step is added by the caller
// only after its inputs are checked, so a step cannot refer to itself).
func checkRefs(v any, at string, outputs map[string]map[string]string, self string) []string {
	var errs []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			for _, tok := range anyRef.FindAllString(t, -1) {
				if !interpRe.MatchString(tok) {
					errs = append(errs, fmt.Sprintf("%s: malformed reference %q (write ${step.output})", at, tok))
				}
			}
			for _, m := range interpRe.FindAllStringSubmatch(t, -1) {
				outs, prior := outputs[m[1]]
				if !prior || m[1] == self {
					errs = append(errs, fmt.Sprintf("%s: %q refers to a step that does not run before it", at, m[0]))
					continue
				}
				if top := strings.SplitN(m[2], ".", 2)[0]; outs != nil {
					if _, has := outs[top]; !has {
						errs = append(errs, fmt.Sprintf("%s: step %s has no output %q", at, m[1], top))
					}
				}
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return errs
}

// summarize keeps run records small: big lists and long strings are cut for the log.
func summarize(out map[string]any) map[string]any {
	s := make(map[string]any, len(out))
	for k, v := range out {
		switch x := v.(type) {
		case []any:
			s[k] = fmt.Sprintf("[%d items]", len(x))
		case []map[string]any:
			s[k] = fmt.Sprintf("[%d items]", len(x))
		case string:
			if len(x) > 200 {
				x = x[:200] + "…"
			}
			s[k] = x
		default:
			s[k] = v
		}
	}
	return s
}

func asAuth(err error, target *AuthError) bool { return errors.As(err, target) }
