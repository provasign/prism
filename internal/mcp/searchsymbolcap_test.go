package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// ab_gate 2026-09-06 (grafana CheckHealth): scope="symbols", exhaustive=true
// returned the default 25 of 53+ methods with no marker; the agent answered
// from the sample and scored 0.73 recall. Symbol search must either honour
// exhaustive or say it is a sample.
func symbolCapFixture(t *testing.T, n int) *Handler {
	return symbolScopeFixture(t, n, 0)
}

// n symbols FooThingNN at the root, m symbols FooThingNN under sub/ — the
// root ones sort first, so a scoped search for sub/ sees its matches only
// after every root match in the ranked list.
func symbolScopeFixture(t *testing.T, n, m int) *Handler {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "func FooThing%02d() int { return %d }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if m > 0 {
		var s strings.Builder
		s.WriteString("package sub\n\n")
		for i := 0; i < m; i++ {
			fmt.Fprintf(&s, "func FooThingSub%02d() int { return %d }\n", i, i)
		}
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "sub.go"), []byte(s.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

func TestSearchSymbols_ExhaustiveLiftsTheCap(t *testing.T) {
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 {
		t.Fatalf("exhaustive symbol search returned %d of 40", n)
	}
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("exhaustive result must not be marked truncated")
	}
}

func TestSearchSymbols_NonPositiveLimitClampsToDefault(t *testing.T) {
	// Review 2026-09-06: limit=-1 reached syms[:-1] and panicked the handler.
	h := symbolCapFixture(t, 40)
	for _, lim := range []int{-1, 0} {
		out, err := h.Invoke("prism_search", map[string]any{
			"query": "FooThing", "scope": "symbols", "limit": lim})
		if err != nil {
			t.Fatalf("limit=%d: %v", lim, err)
		}
		m := out.(map[string]any)
		if n := len(anySlice(m["symbols"])); n != defaultSearchLimit {
			t.Errorf("limit=%d: want the default %d, got %d", lim, defaultSearchLimit, n)
		}
		if m["symbolsTruncated"] != true {
			t.Errorf("limit=%d: 40 matches under a %d cap must be flagged", lim, defaultSearchLimit)
		}
	}
}

func TestSearchSymbols_ExhaustiveIsBoundedAndSaysSo(t *testing.T) {
	// Exhaustive is uncapped up to exhaustiveSymbolCap, then bounded with a
	// warning that names the cap — never a transport-level cut with no marker.
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 || m["symbolsTruncated"] != nil {
		t.Fatalf("40 < cap: want all 40 unflagged, got %d flagged=%v", n, m["symbolsTruncated"])
	}
	// The over-cap branch needs >2000 matching symbols; the fixture stays
	// small and the warning it would emit is asserted through its helper.
	w := exhaustiveCapWarning(exhaustiveSymbolCap)
	if !strings.Contains(w, "MORE than 2000") || !strings.Contains(w, "path=/glob=") {
		t.Errorf("exhaustive cap warning must name the cap and the narrowing: %q", w)
	}
}

func TestSearchSymbols_ScopedSearchSeesPastOutOfScopeMatches(t *testing.T) {
	// Review 2026-09-06: a fixed fetch (limit*4+1, or cap+1) filtered by
	// path= afterwards could return an incomplete in-scope set unflagged
	// when out-of-scope matches ranked ahead of it. 60 root matches sort
	// before the 40 under sub/; limit=5 used to fetch 21, filter to 0.
	h := symbolScopeFixture(t, 60, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "path": "sub", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 5 {
		t.Fatalf("scoped limit=5: want 5 in-scope symbols, got %d", n)
	}
	if m["symbolsTruncated"] != true {
		t.Error("40 in-scope matches under limit=5 must be flagged truncated")
	}
	out, err = h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "path": "sub", "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 40 {
		t.Fatalf("scoped exhaustive: want all 40 in-scope symbols, got %d", n)
	}
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("a complete scoped exhaustive result must not be flagged")
	}
	for _, s := range anySlice(m["symbols"]) {
		if fp, _ := s.(map[string]any)["filePath"].(string); !strings.HasPrefix(fp, "sub/") {
			t.Errorf("out-of-scope symbol leaked into a path=sub result: %s", fp)
		}
	}
}

func TestSearchSymbols_CappedResultSaysSo(t *testing.T) {
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "symbols"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if n := len(anySlice(m["symbols"])); n != 25 {
		t.Fatalf("default cap should be 25, got %d", n)
	}
	if m["symbolsTruncated"] != true {
		t.Error("capped symbol list must be flagged")
	}
	txt, ok := renderSearchAsText(m)
	if !ok {
		t.Fatal("text renderer fell back to JSON on a truncated symbol result")
	}
	if !strings.Contains(txt, "a SAMPLE") || !strings.Contains(txt, "exhaustive=true") {
		t.Errorf("warning must reach the agent in the text form:\n%s", txt)
	}
	// Under the cap: no flag, no warning.
	out, _ = h.Invoke("prism_search", map[string]any{"query": "FooThing0", "scope": "symbols"})
	m = out.(map[string]any)
	if _, ok := m["symbolsTruncated"]; ok {
		t.Error("a result under the cap must not be flagged")
	}
}
