// Package workspace models the set of repositories linked to the one a
// command answers for. Each repository keeps its own index; the workspace
// only says which other roots may be consulted and why.
package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
)

// Root is one linked repository.
type Root struct {
	ID string
	// Path is the absolute, symlink-resolved directory. For a missing root it
	// is the cleaned absolute path that was declared.
	Path string
	// Evidence says where the link was declared, for `prism workspace`.
	Evidence string
	// Missing is true when the declared directory does not exist.
	Missing bool
	// Cache is true for a clone Prism fetched: reference source at a fixed
	// commit, not a checkout the user works in.
	Cache bool
}

// Workspace is the primary root plus the roots linked to it.
type Workspace struct {
	Primary string
	Roots   []Root
}

// Load builds the workspace for primary from two sources: the
// `workspace_repo:` lines of its own prism.yaml (cfg), and those of the
// nearest ancestor prism.yaml, which is how a parent folder holding several
// checkouts declares them once. It returns nil when nothing is linked, so a
// repository with no workspace behaves exactly as before.
func Load(primary string, cfg *config.Config) *Workspace {
	primary = resolve(primary)
	w := &Workspace{Primary: primary}
	seen := map[string]bool{primary: true}
	ids := map[string]bool{}
	add := func(base, file string, repos []config.WorkspaceRepo) {
		for _, r := range repos {
			p := r.Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(base, filepath.FromSlash(p))
			}
			p = resolve(p)
			if seen[p] {
				continue
			}
			seen[p] = true
			id := r.ID
			for n := 2; ids[id]; n++ {
				id = fmt.Sprintf("%s-%d", r.ID, n)
			}
			ids[id] = true
			_, err := os.Stat(p)
			w.Roots = append(w.Roots, Root{
				ID: id, Path: p, Missing: err != nil,
				Evidence: file + ": workspace_repo: " + r.ID + "=" + r.Path,
			})
		}
	}
	if cfg != nil {
		add(primary, "prism.yaml", cfg.WorkspaceRepos)
	}
	if dir, ok := ancestorConfigDir(primary); ok {
		if parent, err := config.LoadFromDir(dir); err == nil {
			add(dir, filepath.Join(dir, "prism.yaml"), parent.WorkspaceRepos)
		}
	}
	// Cache clones linked on this machine: dependencies Prism fetched
	// because they are not checked out here.
	for _, l := range CacheLinks(primary) {
		p := resolve(l.Path)
		if seen[p] {
			continue
		}
		seen[p] = true
		id := l.ID
		for n := 2; ids[id]; n++ {
			id = fmt.Sprintf("%s-%d", l.ID, n)
		}
		ids[id] = true
		_, err := os.Stat(p)
		ref := l.Ref
		if ref == "" {
			ref = "default branch"
		}
		sha := l.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		w.Roots = append(w.Roots, Root{
			ID: id, Path: p, Missing: err != nil, Cache: true,
			Evidence: fmt.Sprintf("cache clone of %s at %s (%s), read-only reference", l.URL, ref, sha),
		})
	}
	if len(w.Roots) == 0 {
		return nil
	}
	return w
}

// Linked returns the roots that exist on disk.
func (w *Workspace) Linked() []Root {
	if w == nil {
		return nil
	}
	var out []Root
	for _, r := range w.Roots {
		if !r.Missing {
			out = append(out, r)
		}
	}
	return out
}

// RelPath returns abs as a slash path relative to the primary root, which is
// the form answers print for a file in another repository ("../grove/x.go"):
// the agent's own file tools accept it from the primary root unchanged.
func (w *Workspace) RelPath(abs string) string {
	for _, r := range w.Roots {
		if r.Cache && (abs == r.Path || strings.HasPrefix(abs, r.Path+string(filepath.Separator))) {
			return filepath.ToSlash(abs)
		}
	}
	rel, err := filepath.Rel(w.Primary, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// DisplayPath is the path answers print for a linked root: relative to the
// primary for a checkout beside it, absolute for a cache clone, whose
// relative path would be a long climb out of the repository.
func (w *Workspace) DisplayPath(r Root) string {
	if r.Cache {
		return filepath.ToSlash(r.Path)
	}
	return w.RelPath(r.Path)
}

// Describe lists the linked roots as "id (relative path)", for one-line notes.
func (w *Workspace) Describe() string {
	var parts []string
	for _, r := range w.Linked() {
		parts = append(parts, r.ID+" ("+w.DisplayPath(r)+")")
	}
	return strings.Join(parts, ", ")
}

// Status is the state of one root on disk, for `prism workspace`.
type Status struct {
	Root
	Ref       string // branch name, or "" when detached or not a git work tree
	Head      string // short commit SHA
	Dirty     bool
	Indexed   bool
	IndexedAt time.Time
}

// Inspect reads a root's git state and whether it has an index. It changes
// nothing in the root.
func Inspect(r Root) Status {
	s := Status{Root: r}
	if r.Missing {
		return s
	}
	s.Head = git(r.Path, "rev-parse", "--short", "HEAD")
	if ref := git(r.Path, "symbolic-ref", "--quiet", "--short", "HEAD"); ref != "" {
		s.Ref = ref
	}
	s.Dirty = git(r.Path, "status", "--porcelain", "--untracked-files=no") != ""
	if fi, err := os.Stat(filepath.Join(r.Path, ".grove", "grove.db")); err == nil {
		s.Indexed, s.IndexedAt = true, fi.ModTime()
	}
	return s
}

func git(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ancestorConfigDir finds the nearest directory strictly above root that
// holds a prism.yaml.
func ancestorConfigDir(root string) (string, bool) {
	for d := filepath.Dir(root); ; {
		if _, err := os.Stat(filepath.Join(d, "prism.yaml")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return filepath.Clean(abs)
}

// TreeState is what is checked out in a repository right now.
type TreeState struct {
	Head string // full commit SHA; "" when not a git work tree
	Ref  string // branch; "" when detached
	// Changed lists files git reports as modified, added or untracked,
	// repository-relative.
	Changed []string
}

// CurrentTree reads a repository's checked-out commit and its uncommitted
// files. It changes nothing.
func CurrentTree(root string) TreeState {
	t := TreeState{Head: git(root, "rev-parse", "HEAD")}
	if t.Head == "" {
		return t
	}
	t.Ref = git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	for _, line := range strings.Split(git(root, "status", "--porcelain", "--untracked-files=all"), "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if i := strings.Index(p, " -> "); i >= 0 { // rename: the new path
			p = p[i+4:]
		}
		t.Changed = append(t.Changed, strings.Trim(p, `"`))
	}
	return t
}
