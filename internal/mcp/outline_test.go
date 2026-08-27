package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func bigFile(lines int) string { return strings.Repeat("x\n", lines) }

func syms(n int) []grove.SymbolRecord {
	out := make([]grove.SymbolRecord, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, grove.SymbolRecord{
			Name: "m" + string(rune('a'+i%26)), QualifiedName: "T.m",
			Kind: "method", Signature: "void m()",
			Span: grove.SpanInfo{Start: i*10 + 1, End: i*10 + 9},
		})
	}
	return out
}

// The threshold is the whole design: below it a body is cheaper than a
// round trip, above it the body dominates every later turn.
func TestOutlineOnlyForLargeFiles(t *testing.T) {
	h := newTestHandler(t)
	if _, ok := h.fileOutline("a.java", bigFile(799), syms(5)); ok {
		t.Error("799 lines must be delivered whole")
	}
	if _, ok := h.fileOutline("a.java", bigFile(1200), syms(5)); !ok {
		t.Error("1200 lines must outline")
	}
	// nothing indexed -> caller delivers normally (guarded at the call site)
	if _, ok := h.fileOutline("a.java", bigFile(1200), nil); ok {
		t.Error("no symbols means no useful outline")
	}
}

func TestOutlineShapeAndCaps(t *testing.T) {
	h := newTestHandler(t)
	out, ok := h.fileOutline("Big.java", bigFile(2000), syms(100))
	if !ok {
		t.Fatal("expected outline")
	}
	c := out["content"].(string)
	for _, want := range []string{"OUTLINE", "2000 lines", "prism_lookup", "full=true"} {
		if !strings.Contains(c, want) {
			t.Errorf("outline missing %q", want)
		}
	}
	if out["strategy"] != "outline" || out["totalLines"] != 2000 {
		t.Errorf("envelope wrong: %v %v", out["strategy"], out["totalLines"])
	}
	// the map must not become the territory
	if n := strings.Count(c, "\n"); n > outlineSymbolCap+12 {
		t.Errorf("outline itself is too long: %d lines", n)
	}
	if !strings.Contains(c, "more symbols") {
		t.Error("elision past the cap must be stated, not silent")
	}
}
