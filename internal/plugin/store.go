package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sentinel/internal/engine"
)

// States of a block folder.
const (
	StateApproved   = "approved"   // reviewed and unchanged: registered, usable in workflows
	StatePending    = "pending"    // proposed (in _pending): not usable until approved
	StateUnapproved = "unapproved" // installed but never approved: not usable
	StateModified   = "modified"   // changed after approval: not usable until approved again
	StateInvalid    = "invalid"    // manifest problem
	StateConflict   = "conflict"   // name already used by a built-in or another block
)

// Info describes one block folder.
type Info struct {
	Name     string
	Dir      string
	State    string
	Problem  string
	Manifest *Manifest
	Hash     string
}

// Store is a folder of blocks: <Root>/<Name>/ for installed blocks, <Root>/_pending/<Name>/ for
// proposals, and an approvals file.
type Store struct {
	Root          string
	ApprovalsPath string
}

// Open returns the store at root; approvals live in approvalsPath (default root/approvals.json).
func Open(root, approvalsPath string) *Store {
	if approvalsPath == "" {
		approvalsPath = filepath.Join(root, "approvals.json")
	}
	return &Store{Root: root, ApprovalsPath: approvalsPath}
}

func (s *Store) PendingDir() string { return filepath.Join(s.Root, "_pending") }

// List inspects every block folder without registering anything.
func (s *Store) List() ([]Info, error) {
	appr, err := loadApprovals(s.ApprovalsPath)
	if err != nil {
		return nil, err
	}
	var out []Info
	seen := map[string]bool{}
	for _, area := range []struct {
		dir     string
		pending bool
	}{{s.Root, false}, {s.PendingDir(), true}} {
		ents, err := os.ReadDir(area.dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() || (!area.pending && e.Name()[0] == '_') || e.Name()[0] == '.' {
				continue
			}
			dir := filepath.Join(area.dir, e.Name())
			info := Info{Name: e.Name(), Dir: dir}
			m, merr := ParseManifest(dir)
			if merr != nil {
				info.State, info.Problem = StateInvalid, merr.Error()
				out = append(out, info)
				continue
			}
			info.Manifest, info.Name = m, m.Name
			info.Hash, merr = HashDir(dir)
			if merr != nil {
				info.State, info.Problem = StateInvalid, merr.Error()
				out = append(out, info)
				continue
			}
			switch {
			case area.pending:
				info.State = StatePending
			case appr[m.Name].Hash == "":
				info.State = StateUnapproved
			case appr[m.Name].Hash != info.Hash:
				info.State = StateModified
			default:
				info.State = StateApproved
			}
			if !area.pending {
				if seen[m.Name] {
					info.State, info.Problem = StateConflict, "another installed block has this name"
				}
				seen[m.Name] = true
			}
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Dir < out[j].Dir
	})
	return out, nil
}

// Find returns the block named name, preferring a pending proposal (the one under review).
func (s *Store) Find(name string) (*Info, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var found *Info
	for i := range all {
		if all[i].Name == name || filepath.Base(all[i].Dir) == name {
			if found == nil || all[i].State == StatePending {
				found = &all[i]
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no block named %q in %s", name, s.Root)
	}
	return found, nil
}

// Register adds every approved, unchanged block to the engine. It returns the folders that were
// not loaded and why. Built-in blocks cannot be replaced.
func (s *Store) Register() ([]Info, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range all {
		in := &all[i]
		if in.State != StateApproved {
			continue
		}
		if engine.IsRegistered(in.Name) {
			in.State, in.Problem = StateConflict, "a built-in block already has this name"
			continue
		}
		engine.Register(extBlock{m: in.Manifest, dir: in.Dir})
	}
	return all, nil
}

// Approve pins the current contents of an installed block. A pending block is moved in first.
func (s *Store) Approve(name string) error {
	in, err := s.Find(name)
	if err != nil {
		return err
	}
	if in.Manifest == nil {
		return fmt.Errorf("%s: %s", name, in.Problem)
	}
	if in.State == StatePending {
		dest := filepath.Join(s.Root, in.Name)
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("%s is already installed; remove it first (sentinel block remove %s) to replace it", in.Name, in.Name)
		}
		if err := os.MkdirAll(s.Root, 0o700); err != nil {
			return err
		}
		if err := os.Rename(in.Dir, dest); err != nil {
			return err
		}
		in.Dir = dest
	}
	h, err := HashDir(in.Dir)
	if err != nil {
		return err
	}
	appr, err := loadApprovals(s.ApprovalsPath)
	if err != nil {
		return err
	}
	appr[in.Name] = Approval{Hash: h, ApprovedAt: now()}
	return saveApprovals(s.ApprovalsPath, appr)
}

// Revoke removes an approval; the folder stays but the block stops loading.
func (s *Store) Revoke(name string) error {
	appr, err := loadApprovals(s.ApprovalsPath)
	if err != nil {
		return err
	}
	if _, ok := appr[name]; !ok {
		return fmt.Errorf("%s has no approval", name)
	}
	delete(appr, name)
	return saveApprovals(s.ApprovalsPath, appr)
}

// Remove deletes a block folder (installed or pending) and its approval.
func (s *Store) Remove(name string) error {
	in, err := s.Find(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(in.Dir); err != nil {
		return err
	}
	if appr, err := loadApprovals(s.ApprovalsPath); err == nil {
		if _, ok := appr[in.Name]; ok && in.State != StatePending {
			delete(appr, in.Name)
			return saveApprovals(s.ApprovalsPath, appr)
		}
	}
	return nil
}
