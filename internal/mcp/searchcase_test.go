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
	// Default: case-insensitive. Smart case regressed the A/B (brokenPipe
	// missed isBrokenPipe), so it is opt-in.
	_, camel := count(map[string]any{"query": "absolute"})
	if !strings.Contains(camel, "5: var mustAbsolute") {
		t.Fatalf("default search must ignore case:\n%s", camel)
	}
	_, upper := count(map[string]any{"query": "MUSTABS"})
	if !strings.Contains(upper, "3: func mustAbs") || !strings.Contains(upper, "case-insensitive") {
		t.Fatalf("default is case-insensitive:\n%s", upper)
	}
	_, sensitive := count(map[string]any{"query": "MUSTABS", "case": "sensitive"})
	if strings.Contains(sensitive, "3: func mustAbs") || !strings.Contains(sensitive, "exact matches") {
		t.Fatalf("case=sensitive:\n%s", sensitive)
	}
	_, smart := count(map[string]any{"query": "MUSTABS", "case": "smart"})
	if strings.Contains(smart, "3: func mustAbs") {
		t.Fatalf("case=smart with an uppercase term:\n%s", smart)
	}
	_, word := count(map[string]any{"query": "mustAbs", "word": true})
	if strings.Contains(word, "5: var mustAbsolute") || !strings.Contains(word, "3: func mustAbs") {
		t.Fatalf("word:\n%s", word)
	}
	if !strings.Contains(word, "not searched, has matches: .claude (1)") {
		t.Fatalf("excluded directory with a match not named:\n%s", word)
	}
	_, none := count(map[string]any{"query": "mustAbsolute"})
	if strings.Contains(none, "not searched") {
		t.Fatalf("excluded directory without matches must not be named:\n%s", none)
	}
	_, all := count(map[string]any{"query": "mustAbs", "no_default_excludes": true})
	if !strings.Contains(all, ".claude/notes.md") || strings.Contains(all, "not searched") {
		t.Fatalf("no_default_excludes:\n%s", all)
	}
}
