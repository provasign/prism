package mcp

import (
	"context"
	"fmt"
	"path"
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
			if !moduleMatchesFile(c.File, mods[:k]) {
				continue
			}
			qn := strings.Join(append(append([]string{}, mods[k:]...), leaf), ".")
			t := target{file: c.File, qn: qn}
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
			break
		}
	}
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
