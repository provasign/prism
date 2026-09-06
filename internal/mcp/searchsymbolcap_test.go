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
