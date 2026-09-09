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
		"// Scored (graph/x.go:10); 3 caller(s): Query graph/q.go:1 Query graph/q.go:2 SemanticSearch graph/s.go:3. A contract change here touches that whole set — prism_change_impact for the closed, line-precise list.\n"
	got1 := o.apply(first)
	if got1 != first {
		t.Fatalf("first sighting must be verbatim:\n%s\n---\n%s", first, got1)
	}
	got2 := o.apply(first)
	for _, want := range []string{"// no matches\n", "// 176 more files omitted (exhaustive=true lists them)", "// (as noted earlier) Scored (graph/x.go:10); 3 caller(s)"} {
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
