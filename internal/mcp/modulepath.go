package mcp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// modulePathImpact answers a module-qualified symbol path such as
// werkzeug.utils.redirect or werkzeug.wrappers.Response.get_data. Grove reads
// the segment before the leaf as a type, so a module path fails with
// "no type named utils" (Codex agent, werkzeug, 2026-10-17). The leading
// segments must match the declaring file's path (src/werkzeug/utils.py,
// werkzeug/utils/__init__.py); whatever remains is the symbol's qualified
// name inside that file. One matching file answers; several, or none, fall
// through to the existing error paths. Nothing is guessed.
func (h *Handler) modulePathImpact(ctx context.Context, query string) (*grove.ChangeImpactResult, string, bool) {
	segs := strings.Split(strings.TrimSpace(query), ".")
	if len(segs) < 2 {
		return nil, "", false
	}
	leaf := segs[len(segs)-1]
	mods := segs[:len(segs)-1]
	candidates, err := h.Grove.Resolve(ctx, leaf)
	if err != nil || len(candidates) == 0 {
		return nil, "", false
	}
	type target struct{ file, qn string }
	seen := map[target]bool{}
	var targets []target
	for _, c := range candidates {
		if c.TestDouble {
			continue
		}
		// Longest module prefix that matches the file; the rest qualifies
		// the symbol within it (Type.method), or nothing (free function).
		for k := len(mods); k >= 1; k-- {
			qn := strings.Join(append(append([]string{}, mods[k:]...), leaf), ".")
			if h.goPackageMatches(c.File, mods[:k]) {
				// A Go package holds many files: the rest of the query must
				// be the symbol's own qualified name there, so memfs.New is
				// the function New, not the method storage.New beside it.
				if c.Name != qn {
					continue
				}
			} else if !moduleMatchesFile(c.File, mods[:k]) {
				continue
			}
			t := target{file: c.File, qn: qn}
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
			break
		}
	}
	targets2 := targets[:0:0]
	for _, t := range targets {
		// One Go package declaring the name in build-tag variants
		// (osfs/os.go and osfs/os_js.go) is one symbol: keep the file
		// with the shortest name, the untagged one where there is one.
		merged := false
		for i, u := range targets2 {
			if path.Ext(t.file) == ".go" && path.Ext(u.file) == ".go" && path.Dir(t.file) == path.Dir(u.file) && t.qn == u.qn {
				if len(t.file) < len(u.file) {
					targets2[i] = t
				}
				merged = true
				break
			}
		}
		if !merged {
			targets2 = append(targets2, t)
		}
	}
	targets = targets2
	if len(targets) != 1 {
		return nil, "", false
	}
	r, err := h.Grove.ChangeImpactScoped(ctx, targets[0].qn, targets[0].file)
	if err != nil {
		return nil, "", false
	}
	note := fmt.Sprintf("%s is a module path: resolved to %s in %s", query, targets[0].qn, targets[0].file)
	return r, note, true
}

// goPackageMatches reports whether a Go file's package is named by the
// segments: a Go qualifier names the package (its directory, or its package
// clause for a module-root package such as pflag), never the file. memfs.New
// is declared in memfs/memory.go; pflag.NewFlagSet in flag.go at the root.
func (h *Handler) goPackageMatches(file string, mods []string) bool {
	p := strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), "./")
	if path.Ext(p) != ".go" {
		return false
	}
	dir := path.Dir(p)
	want := strings.Join(mods, "/")
	if dir == want || strings.HasSuffix(dir, "/"+want) {
		return true
	}
	return len(mods) == 1 && goPackageClause(filepath.Join(h.Root, filepath.FromSlash(p))) == mods[0]
}

// goPackageClause returns the package name a Go file declares, or "".
func goPackageClause(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	m := goPackageClauseRE.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

var goPackageClauseRE = regexp.MustCompile(`(?m)^package\s+(\w+)`)

// moduleMatchesFile reports whether a file path ends with the module
// segments: pkg/mod.py, pkg/mod/__init__.py, pkg/mod.go, pkg/mod/index.ts.
func moduleMatchesFile(file string, mods []string) bool {
	p := strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), "./")
	p = strings.TrimSuffix(p, path.Ext(p))
	for _, index := range []string{"/__init__", "/index", "/mod"} {
		p = strings.TrimSuffix(p, index)
	}
	want := strings.Join(mods, "/")
	return p == want || strings.HasSuffix(p, "/"+want)
}
