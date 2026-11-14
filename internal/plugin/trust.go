package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Approval pins the exact contents of a block that a person reviewed.
type Approval struct {
	Hash       string `json:"hash"`
	ApprovedAt string `json:"approved_at"`
}

// Approvals maps block name to its approval.
type Approvals map[string]Approval

// HashDir returns a digest of every file in a block folder (names and contents). Symlinks are
// refused because they could point outside the reviewed folder.
func HashDir(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" || d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a block folder may only hold plain files", p)
		}
		if d.Name() == ".DS_Store" || strings.HasSuffix(d.Name(), ".pyc") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func loadApprovals(path string) (Approvals, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Approvals{}, nil
	}
	if err != nil {
		return nil, err
	}
	a := Approvals{}
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", path, err)
	}
	return a, nil
}

func saveApprovals(path string, a Approvals) error {
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
