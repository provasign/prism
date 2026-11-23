package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// GAP-03: transitive callers walk resolved incoming calls level by level,
// and path finds the chain between two symbols.
func TestCallersAndPath(t *testing.T) {
	dir := t.TempDir()
	planWrite(t, dir, "go.mod", "module example.com/p\n\ngo 1.26\n")
	planWrite(t, dir, "a.go", "package p\n\nfunc Leaf() {}\n\nfunc Mid() { Leaf() }\n\nfunc Top() { Mid() }\n\nfunc Other() {}\n")
	gc := grove.NewClient().WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_callers", map[string]any{"name": "Leaf"})
	if err != nil {
		t.Fatal(err)
	}
	text := renderCallersText(out.(map[string]any))
	if !strings.Contains(text, "<- Mid") || !strings.Contains(text, "   <- Top") {
		t.Fatalf("callers tree:\n%s", text)
	}
	out, err = h.Invoke("prism_callers", map[string]any{"name": "Leaf", "depth": 1})
	if err != nil || strings.Contains(renderCallersText(out.(map[string]any)), "Top") {
		t.Fatalf("depth 1 went deeper: %v", err)
	}
	out, err = h.Invoke("prism_call_path", map[string]any{"from": "Top", "to": "Leaf"})
	if err != nil {
		t.Fatal(err)
	}
	if p := renderCallPathText(out.(map[string]any)); !strings.Contains(p, "2 hop(s)") || !strings.Contains(p, "-> Mid") {
		t.Fatalf("path:\n%s", p)
	}
	out, _ = h.Invoke("prism_call_path", map[string]any{"from": "Other", "to": "Leaf"})
	if p := renderCallPathText(out.(map[string]any)); !strings.Contains(p, "no resolved call path") {
		t.Fatalf("no-path:\n%s", p)
	}
}
