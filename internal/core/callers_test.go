package core

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

// GAP-04: missing-implementations for a member the interface does not
// declare yet lists every implementer, and which already have the method.
func TestMissingImplementationsPlannedMember(t *testing.T) {
	dir := t.TempDir()
	planWrite(t, dir, "go.mod", "module example.com/p\n\ngo 1.26\n")
	planWrite(t, dir, "s.go", "package p\n\ntype Shape interface{ Area() float64 }\n\ntype Sq struct{}\n\nfunc (Sq) Area() float64 { return 1 }\n\ntype Circle struct{}\n\nfunc (Circle) Area() float64 { return 3 }\n\nfunc (Circle) Perimeter() float64 { return 6 }\n\nvar _ Shape = Sq{}\nvar _ Shape = Circle{}\n")
	gc := grove.NewClient().WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_missing_implementations", map[string]any{"query": "Shape.Perimeter", "signature": "Perimeter() float64"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	missing := anyMaps(m["missing"])
	have := anyMaps(m["alreadyHave"])
	if m["planned"] != true || len(missing) != 1 || missing[0]["name"] != "Sq" || len(have) != 1 || have[0]["signatureMatches"] != true {
		t.Fatalf("planned member: %v", m)
	}
}
