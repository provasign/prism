package mcp

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/textsearch"
)

// Filter-miss diagnosis for path=/glob= scoped search and query.
//
// Measured (rerun 2026-09-25, requests pr7315 x2, jackson pr6019): an agent
// passed paths=["requests/models.py"] in a src/ layout. Every term came back
// "no matches — search completed … retry broader/shorter", and the query form
// said "check term spelling". Both blamed the terms; the filter had selected
// no file, so no term was ever tested. The agent then spent Glob + ls turns to
// discover src/requests/models.py. The fix names the filter as the cause and
// offers the real paths that share the requested base name.

// scopeWalkFileCap bounds the suggestion walk on very large trees. The walk
// only runs when a filter already failed, never on the happy path.
const scopeWalkFileCap = 200000

// scopeSuggestionCap is how many closest real paths are offered per miss.
const scopeSuggestionCap = 3

var scopeWalkSkipDirs = map[string]bool{
	".git": true, ".grove": true, ".shale": true, ".claude": true, ".cursor": true,
	".windsurf": true, ".kiro": true, ".devin": true, "node_modules": true,
	".venv": true, "__pycache__": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true,
}

type scopeFilterReport struct {
	// noFiles is true when the filters, taken together, select no file.
	noFiles bool
	note    string
}

// scopeFilterCheck reports path=/glob= entries that select nothing. Missing
// paths are always reported (an os.Stat each). Whether globs select any file
// is only checked when checkGlobs is set, because it needs a tree walk; the
// callers set it when the scoped result came back empty.
func scopeFilterCheck(root string, paths, globs []string, checkGlobs bool) scopeFilterReport {
	if len(paths) == 0 && len(globs) == 0 {
		return scopeFilterReport{}
	}
	var missing, present []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, filepath.FromSlash(p))
		}
		if _, err := os.Stat(abs); err != nil {
			missing = append(missing, p)
		} else {
			present = append(present, p)
		}
	}
	allPathsMissing := len(paths) > 0 && len(present) == 0 && len(missing) > 0
	globsMatchNothing := false
	var walked []string
	walk := func() []string {
		if walked == nil {
			walked = scopeWalkFiles(root)
		}
		return walked
	}
	if checkGlobs && len(globs) > 0 && !allPathsMissing {
		globsMatchNothing = true
		for _, rel := range walk() {
			if !scopeUnderPaths(rel, present) {
				continue
			}
			if textsearch.MatchAnyGlob(globs, rel) {
				globsMatchNothing = false
				break
			}
		}
	}
	if len(missing) == 0 && !globsMatchNothing {
		return scopeFilterReport{}
	}
	var parts []string
	for _, p := range missing {
		s := fmt.Sprintf("path %q does not exist under the project root", p)
		if near := closestPaths(walk(), p); len(near) > 0 {
			s += fmt.Sprintf(" (closest real paths: %s)", strings.Join(near, ", "))
		}
		parts = append(parts, s)
	}
	if globsMatchNothing {
		s := fmt.Sprintf("glob=%q matches no file", globs)
		if len(present) > 0 {
			s += fmt.Sprintf(" under path=%q", present)
		}
		var near []string
		for _, g := range globs {
			near = append(near, closestGlobPaths(walk(), g)...)
		}
		near = dedupeStrings(near)
		if len(near) > scopeSuggestionCap {
			near = near[:scopeSuggestionCap]
		}
		if len(near) > 0 {
			s += fmt.Sprintf(" (closest real paths: %s)", strings.Join(near, ", "))
		} else {
			s += " in this project (dependency or generated sources are not indexed)"
		}
		parts = append(parts, s)
	}
	report := scopeFilterReport{noFiles: allPathsMissing || globsMatchNothing}
	if report.noFiles {
		report.note = "FILTER MATCHED NO FILES: " + strings.Join(parts, "; ") +
			". No term was tested, so an empty result says nothing about the terms — correct paths/glob or drop the filter"
	} else {
		report.note = "FILTER PARTLY INVALID: " + strings.Join(parts, "; ") +
			fmt.Sprintf("; only %q was searched", present)
	}
	return report
}

func scopeUnderPaths(rel string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		p = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(p), "./"), "/")
		if p == "" || p == "." || rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// scopeWalkFiles lists repository-relative files and directories (directories
// end in "/") under root, skipping VCS, tool state, and dependency caches.
func scopeWalkFiles(root string) []string {
	out := []string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(out) >= scopeWalkFileCap {
			return filepath.SkipAll
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if scopeWalkSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			out = append(out, rel+"/")
			return nil
		}
		out = append(out, rel)
		return nil
	})
	return out
}

// closestPaths returns real paths sharing the missing path's base name,
// preferring the longest shared trailing path (src/requests/models.py for
// requests/models.py) and then the shortest path.
func closestPaths(files []string, missing string) []string {
	want := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(missing), "./"), "/")
	base := path.Base(want)
	if base == "" || base == "." || base == "/" {
		return nil
	}
	type cand struct {
		p      string
		suffix int
	}
	var cands []cand
	for _, f := range files {
		name := strings.TrimSuffix(f, "/")
		if path.Base(name) != base {
			continue
		}
		cands = append(cands, cand{f, sharedTrailingSegments(name, want)})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].suffix != cands[j].suffix {
			return cands[i].suffix > cands[j].suffix
		}
		return len(cands[i].p) < len(cands[j].p)
	})
	var out []string
	for _, c := range cands {
		out = append(out, c.p)
		if len(out) == scopeSuggestionCap {
			break
		}
	}
	return out
}

// closestGlobPaths suggests files whose base name matches the glob's last
// segment, for a path-shaped glob that selected nothing.
func closestGlobPaths(files []string, glob string) []string {
	g := strings.TrimSuffix(filepath.ToSlash(glob), "/")
	last := path.Base(g)
	if last == "**" || last == "*" || last == "." || !strings.Contains(g, "/") {
		return nil
	}
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "/") {
			continue
		}
		if ok, _ := path.Match(last, path.Base(f)); ok {
			out = append(out, f)
			if len(out) == scopeSuggestionCap {
				break
			}
		}
	}
	return out
}

func sharedTrailingSegments(a, b string) int {
	as := strings.Split(a, "/")
	bs := strings.Split(b, "/")
	n := 0
	for i, j := len(as)-1, len(bs)-1; i >= 0 && j >= 0 && as[i] == bs[j]; i, j = i-1, j-1 {
		n++
	}
	return n
}
