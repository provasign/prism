package mcp

import (
	"strings"
	"testing"
)

func TestOnceNotes_FixedAndLong(t *testing.T) {
	var o onceNotes
	first := "a.go:1: x\n// no matches — search completed (not truncated, not timed out)\n" +
		"symbols (2):\n  method Foo.Bar  a.go:1-2\n" + searchLocatorGuidance + "\n" +
		"// 176 more files with matches omitted — narrow the term, or exhaustive=true to list every file\n" +
		"// Scored (graph/x.go:10); 3 indexed caller site(s): Query graph/q.go:1 Query graph/q.go:2 SemanticSearch graph/s.go:3.\n"
	got1 := o.apply(first)
	if got1 != first {
		t.Fatalf("first sighting must be verbatim:\n%s\n---\n%s", first, got1)
	}
	got2 := o.apply(first)
	for _, want := range []string{"// no matches\n", "// 176 more files omitted (exhaustive=true lists them)", "// (as noted earlier) Scored (graph/x.go:10); 3 indexed caller site(s)"} {
		if !strings.Contains(got2, want) {
			t.Errorf("second sighting should carry the short form %q:\n%s", want, got2)
		}
	}
	if strings.Contains(got2, "locator result") {
		t.Errorf("the locator instruction should be dropped after the first time:\n%s", got2)
	}
	for _, payload := range []string{"a.go:1: x", "symbols (2):", "  method Foo.Bar  a.go:1-2"} {
		if !strings.Contains(got2, payload) {
			t.Errorf("payload line %q must never be touched:\n%s", payload, got2)
		}
	}
	// Within one response, the second term's boilerplate is already short.
	got3 := o.apply("── a ──\n// no matches — search completed (not truncated, not timed out)\n── b ──\n// no matches — search completed (not truncated, not timed out)\n")
	if strings.Count(got3, "// no matches\n") != 2 {
		t.Errorf("repeated boilerplate inside one response should be short too:\n%s", got3)
	}
}

// Measured 2026-09-30: change_impact repeated the evidence note under every
// caller and its boundary note on every call; reads repeated the tab note.
func TestOnceNotes_KnownNotesCollapseIndentedAndAcrossOps(t *testing.T) {
	var o onceNotes
	impact := "callers (2):\n  A  a.go:1\n      // " + callExpressionUnavailableNote + "\n  B  b.go:2\n      // " +
		callExpressionUnavailableNote + "\n// " + indexedScopeBoundary + "\n"
	got := o.applyKnown(impact)
	if strings.Count(got, callExpressionUnavailableNote) != 1 || !strings.Contains(got, "      // no call expression shown") {
		t.Fatalf("indented per-caller note must be said once, then short:\n%s", got)
	}
	got = o.applyKnown(impact)
	if strings.Contains(got, indexedScopeBoundary) || !strings.Contains(got, "not a global completeness proof") {
		t.Fatalf("repeated boundary must keep its meaning in short form:\n%s", got)
	}
	read := "// a.go lines 1-2 of 9\n1\t\tx := 1\n// lines 1-2 of 9 — this is a WINDOW, not the file\n// " + tabIndentNoteText + "\n"
	if got := o.applyKnown(read); got != read {
		t.Fatalf("first read keeps its notes:\n%s", got)
	}
	got = o.applyKnown(strings.ReplaceAll(read, "1-2", "3-4"))
	if strings.Contains(got, "WINDOW") || strings.Contains(got, "tab-indented") || !strings.Contains(got, "1\t\tx := 1") {
		t.Fatalf("repeat read notes must drop, source must stay:\n%s", got)
	}
	// Unregistered long lines are never touched outside search.
	src := "// a long unnumbered comment line that might be source code in an outline view, repeated verbatim\n"
	if got := o.applyKnown(src + src); got != src+src {
		t.Fatalf("applyKnown collapsed an unregistered line:\n%s", got)
	}
}
