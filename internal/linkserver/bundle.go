// Package linkserver holds what a shared Prism link server stores and
// answers: per repository and commit, where that repository references
// symbols other repositories declare, and what it declares itself.
//
// A bundle carries locations, one line of source per reference, and
// declaration signatures. It carries no source bodies: answering "who uses
// this" for repositories that are not on a machine needs no more, and a
// server that never holds bodies is a smaller thing to protect.
package linkserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// Bundle is one repository's link data at one commit.
type Bundle struct {
	Repo     string                     `json:"repo"` // stable name: host/org/repo, or the directory name
	SHA      string                     `json:"sha"`
	Ref      string                     `json:"ref,omitempty"` // branch it was exported from
	Dirty    bool                       `json:"dirty,omitempty"`
	Created  string                     `json:"created"`
	Packages []groveeng.ManifestPackage `json:"packages"`
	Requires []groveeng.ManifestRequire `json:"requires"`
	Refs     []Site                     `json:"refs"`
	Defs     []Def                      `json:"defs"`
}

// Site is one place a repository references another's symbol.
type Site struct {
	Key  string `json:"key"`
	File string `json:"file"`
	From string `json:"from"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
	Text string `json:"text,omitempty"`
	Test bool   `json:"test,omitempty"`
}

// Def is one symbol a repository declares that others can reference.
type Def struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Signature string `json:"signature,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// Build reads the existing index and manifests under root into a bundle.
// repo and ref override what git reports; both may be empty. It fails when
// root has no index.
func Build(ctx context.Context, root, repo, ref string) (Bundle, error) {
	r, err := groveeng.OpenReader(root)
	if err != nil {
		return Bundle{}, err
	}
	defer r.Close()
	manifests := groveeng.ScanManifests(root)
	b := Bundle{
		Repo: repo, Ref: ref, Created: time.Now().UTC().Format(time.RFC3339),
		Packages: manifests.Packages, Requires: manifests.Requires,
	}
	b.SHA = gitOut(root, "rev-parse", "HEAD")
	if b.Ref == "" {
		b.Ref = gitOut(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	}
	b.Dirty = gitOut(root, "status", "--porcelain", "--untracked-files=no") != ""
	if b.Repo == "" {
		b.Repo = RepoName(gitOut(root, "config", "--get", "remote.origin.url"), root)
	}
	refs, err := r.AllExternalRefs(ctx)
	if err != nil {
		return Bundle{}, err
	}
	files := map[string][]string{}
	for _, ref := range refs {
		src, ok := files[ref.File]
		if !ok {
			if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref.File))); err == nil {
				src = strings.Split(string(data), "\n")
			}
			files[ref.File] = src
		}
		s := Site{Key: ref.Key, File: ref.File, From: ref.From, Line: ref.Line, Col: ref.Col, Test: isTestPath(ref.File)}
		if ref.Line >= 1 && ref.Line <= len(src) {
			s.Text = strings.TrimSpace(strings.TrimRight(src[ref.Line-1], "\r"))
		}
		b.Refs = append(b.Refs, s)
	}
	syms, err := r.AllSymbols(ctx)
	if err != nil {
		return Bundle{}, err
	}
	for _, s := range syms {
		if key, ok := groveeng.SymbolKey(root, s); ok {
			b.Defs = append(b.Defs, Def{Key: key, Kind: string(s.Kind), Name: s.QualifiedName,
				Signature: s.Signature, File: s.FilePath, Line: s.Span.Start})
		}
	}
	sort.SliceStable(b.Defs, func(i, j int) bool { return b.Defs[i].Key < b.Defs[j].Key })
	return b, nil
}

// RepoName turns a git remote URL into host/org/repo, falling back to the
// directory name for a repository with no remote.
func RepoName(remote, root string) string {
	s := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if s == "" {
		return filepath.Base(root)
	}
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		return strings.Replace(rest, ":", "/", 1)
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if at := strings.IndexByte(s, '@'); at >= 0 && at < strings.IndexByte(s+"/", '/') {
			s = s[at+1:] // drop user:password@ — never store a credential
		}
	}
	return strings.Trim(s, "/")
}

func isTestPath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_") || strings.Contains(filepath.ToSlash(p), "/test/") || strings.Contains(filepath.ToSlash(p), "/tests/")
}

func gitOut(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
