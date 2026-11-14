package plugin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileInfo is one file of a block, for the review summary.
type FileInfo struct {
	Name  string
	Lines int
	Bytes int
}

// Review is what a person needs in order to decide whether to approve a block.
type Review struct {
	Hash     string
	Files    []FileInfo
	Hints    []string // grep-level hints about what the code appears to do; not an analysis
	Previous bool     // an approved version exists
	HaveCopy bool     // ... and a copy of it was kept, so Diff is meaningful
	Diff     string
}

var hintRules = []struct {
	label string
	re    *regexp.Regexp
}{
	{"uses the network", regexp.MustCompile(`(?i)urllib|requests\.|http\.client|\bsocket\b|\bcurl\b|\bwget\b|fetch\(|https?://`)},
	{"starts other programs", regexp.MustCompile(`subprocess|os\.system|os\.popen|\bPopen\b|\beval\(|\bexec\(|\bsh -c\b|child_process`)},
	{"touches credential-like files", regexp.MustCompile(`(?i)\.ssh|\.aws|id_rsa|\.netrc|/etc/passwd|keychain|\.gnupg|\.env\b`)},
	{"writes or deletes files", regexp.MustCompile(`rm -rf|os\.remove|shutil\.rmtree|unlink\(|open\([^)]*['"][wa]`)},
}

// Review summarises the block in in.Dir and, when an earlier approved copy exists, diffs against it.
func (s *Store) Review(in *Info) (*Review, error) {
	h, err := HashDir(in.Dir)
	if err != nil {
		return nil, err
	}
	r := &Review{Hash: h}
	hints := map[string]bool{}
	_ = filepath.Walk(in.Dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(in.Dir, p)
		r.Files = append(r.Files, FileInfo{rel, bytes.Count(b, []byte("\n")) + 1, len(b)})
		if rel != ManifestFile {
			for _, rule := range hintRules {
				if rule.re.Match(b) {
					hints[rule.label] = true
				}
			}
		}
		return nil
	})
	for l := range hints {
		r.Hints = append(r.Hints, l)
	}
	sort.Strings(r.Hints)
	appr, _ := loadApprovals(s.ApprovalsPath)
	if _, ok := appr[in.Name]; ok {
		r.Previous = true
		snap := s.SnapshotDir(in.Name)
		if _, err := os.Stat(snap); err == nil {
			r.HaveCopy = true
			out, _ := exec.Command("diff", "-ruN", snap, in.Dir).CombinedOutput()
			r.Diff = tidyDiff(string(out), snap, in.Dir)
		}
	}
	return r, nil
}

func tidyDiff(d, old, new string) string {
	d = strings.ReplaceAll(d, old+"/", "approved/")
	d = strings.ReplaceAll(d, new+"/", "now/")
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	var keep []string
	for _, l := range lines {
		if strings.HasPrefix(l, "diff -ruN") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > 120 {
		keep = append(keep[:120], fmt.Sprintf("... %d more lines", len(keep)-120))
	}
	return strings.Join(keep, "\n")
}
