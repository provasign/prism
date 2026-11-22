package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BUG-10: smart case, explicit case modes, whole words, and the line
// naming skipped agent-state directories.
func TestSearchCaseWordAndExcludes(t *testing.T) {
	h := newTextSearchHandler(t)
	mustWrite(t, h.Root, "case.go", "package x\n\nfunc mustAbs() {}\nvar MUSTABS = 1\nvar mustAbsolute = 2\n")
	if err := os.MkdirAll(filepath.Join(h.Root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, h.Root, ".claude/notes.md", "mustAbs\n")
	count := func(args map[string]any) (int, string) {
		args["scope"] = "text"
		out, err := h.Invoke("prism_search", args)
		if err != nil {
			t.Fatal(err)
		}
		txt, _ := renderSearchAsText(out.(map[string]any))
		return strings.Count(txt, "case.go") + strings.Count(txt, "\n  "), txt
	}
	_, upper := count(map[string]any{"query": "MUSTABS"})
	if strings.Contains(upper, "3: func mustAbs") || !strings.Contains(upper, "exact matches") {
		t.Fatalf("uppercase term is case-sensitive:\n%s", upper)
	}
	_, lower := count(map[string]any{"query": "mustabs"})
	if !strings.Contains(lower, "4: var MUSTABS") || !strings.Contains(lower, "case-insensitive") {
		t.Fatalf("lowercase term is case-insensitive:\n%s", lower)
	}
	_, forced := count(map[string]any{"query": "MUSTABS", "case": "insensitive"})
	if !strings.Contains(forced, "3: func mustAbs") {
		t.Fatalf("case=insensitive:\n%s", forced)
	}
	_, word := count(map[string]any{"query": "mustAbs", "word": true})
	if strings.Contains(word, "5: var mustAbsolute") || !strings.Contains(word, "3: func mustAbs") {
		t.Fatalf("word:\n%s", word)
	}
	if !strings.Contains(word, "not searched: .claude") {
		t.Fatalf("excluded directory not named:\n%s", word)
	}
	_, all := count(map[string]any{"query": "mustAbs", "no_default_excludes": true})
	if !strings.Contains(all, ".claude/notes.md") || strings.Contains(all, "not searched") {
		t.Fatalf("no_default_excludes:\n%s", all)
	}
}
