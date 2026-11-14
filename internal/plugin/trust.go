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
	"time"
)

// Approval pins the exact contents of a block that a person reviewed.
type Approval struct {
	Hash       string `json:"hash"`
	ApprovedAt string `json:"approved_at"`
}

// Approvals maps block name to its approval.
type Approvals map[string]Approval

const (
	maxFileBytes  = 8 << 20
	maxTotalBytes = 32 << 20
)

// HashDir returns a digest of every file in a block folder: names, executable bits and contents.
// Nothing is skipped (a file that is not hashed could still be executed). Only plain files and
// folders are allowed: symbolic links could point outside the reviewed folder, and a FIFO or
// device would hang the reader.
func HashDir(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a block folder may only hold plain files", p)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a plain file (pipe, socket or device); a block folder may only hold plain files", p)
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	var total int64
	for _, f := range files {
		fi, err := os.Lstat(f)
		if err != nil {
			return "", err
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s changed while it was being read", f)
		}
		if fi.Size() > maxFileBytes {
			return "", fmt.Errorf("%s is larger than %d MB; a block folder holds source, not data", f, maxFileBytes>>20)
		}
		if total += fi.Size(); total > maxTotalBytes {
			return "", fmt.Errorf("block folder is larger than %d MB", maxTotalBytes>>20)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, f)
		exec := 0
		if fi.Mode().Perm()&0o111 != 0 {
			exec = 1
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(rel), exec, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func loadApprovals(path string) (Approvals, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link; refusing to use it", path)
	}
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
	if a == nil {
		a = Approvals{}
	}
	return a, nil
}

// saveApprovals writes through a private temporary file (never a predictable name that could be
// a planted symlink) and renames it into place.
func saveApprovals(path string, a Approvals) error {
	b, _ := json.MarshalIndent(a, "", "  ")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link; refusing to replace it", path)
	}
	f, err := os.CreateTemp(dir, ".approvals-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// updateApprovals does a locked read-modify-write so concurrent approvals are not lost.
func updateApprovals(path string, fn func(Approvals) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	a, err := loadApprovals(path)
	if err != nil {
		return err
	}
	if err := fn(a); err != nil {
		return err
	}
	return saveApprovals(path, a)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
