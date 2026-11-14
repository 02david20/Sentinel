package store

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestQA_MetaKeyCollision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.ReplaceSnapshot("w", "n", map[string]json.RawMessage{"__committed_at__": json.RawMessage(`{"id":"__committed_at__"}`)}, true)
	if err != nil {
		t.Fatalf("an item whose key is the meta key makes every commit fail: %v", err)
	}
}

// Two processes (two Store handles) hitting ReplaceSnapshot(touch=false), which reads then writes in a deferred tx.
func TestQA_ConcurrentReplaceNoTouch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for p := 0; p < 6; p++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 150; i++ {
				if err := s.ReplaceSnapshot("w", "n", map[string]json.RawMessage{"a": json.RawMessage(`{}`)}, false); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	n := 0
	var first error
	for e := range errs {
		if first == nil {
			first = e
		}
		n++
	}
	if n > 0 {
		t.Fatalf("%d failures, first: %v", n, first)
	}
}
