// Package blocks holds the compiled-in block library. Importing the package registers every
// block with the engine.
package blocks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Webhook posts a text message. The real one calls an HTTP webhook; tests use a recorder.
type Webhook interface {
	Post(ctx context.Context, text string) error
}

// toAny converts any value to its generic JSON form (maps, lists, strings, float64 numbers) so
// downstream references, expressions and snapshots all see one shape.
func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func str(in map[string]any, k string) string {
	s, _ := in[k].(string)
	return s
}

func num(in map[string]any, k string, def float64) float64 {
	switch n := in[k].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return def
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func strList(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			out = append(out, fmt.Sprint(e))
		}
	case []string:
		out = append(out, t...)
	}
	return out
}

func boolOr(in map[string]any, k string, def bool) bool {
	if b, ok := in[k].(bool); ok {
		return b
	}
	return def
}
