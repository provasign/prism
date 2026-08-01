package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodeFixture builds a tiny indexed project: helper.go defines Helper, and
// main.go calls it — so Helper has a caller and helper.go has a dependent.
func nodeFixture(t *testing.T) *Handler {
	t.Helper()
	h := newH(t)
	write := func(rel, body string) {
		p := filepath.Join(h.Root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("helper.go", "package app\n\n// Helper does a thing.\nfunc Helper(x int) int {\n\treturn x + 1\n}\n")
	write("main.go", "package app\n\nfunc Run() int {\n\treturn Helper(41)\n}\n")
	if _, err := h.Invoke("prism_index", map[string]any{"dir": h.Root}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

// node <symbol> must return the symbol's source AND its graph neighbours in
// one call — the whole point of the view.
func TestToolNode_Symbol(t *testing.T) {
	h := nodeFixture(t)
	res, err := h.Invoke("prism_node", map[string]any{"name": "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("got %T", res)
	}
	if m["view"] != "symbol" {
		t.Errorf("view = %v, want symbol", m["view"])
	}
	content, _ := m["content"].(string)
	if !strings.Contains(content, "func Helper") {
		t.Errorf("source body missing: %q", content)
	}
	edges, _ := m["edges"].(map[string]any)
	if len(edges) == 0 {
		t.Fatal("no neighbours returned — the composition with prism_edges is broken")
	}
	if _, ok := edges["calls in"]; !ok {
		t.Errorf("Run calls Helper, so 'calls in' must be present; got groups %v", edgeGroupNames(edges))
	}
}

// node <file> must return source + defined symbols + the files depending on it.
func TestToolNode_File(t *testing.T) {
	h := nodeFixture(t)
	res, err := h.Invoke("prism_node", map[string]any{"name": "helper.go"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]any)
	if m["view"] != "file" {
		t.Fatalf("view = %v, want file", m["view"])
	}
	defines, _ := m["defines"].([]map[string]any)
	found := false
	for _, d := range defines {
		if n, _ := d["name"].(string); strings.Contains(n, "Helper") {
			found = true
		}
	}
	if !found {
		t.Errorf("helper.go should define Helper; got %v", defines)
	}
	deps, _ := m["dependents"].([]string)
	wantDep := false
	for _, d := range deps {
		if d == "main.go" {
			wantDep = true
		}
	}
	if !wantDep {
		t.Errorf("main.go calls Helper so it must be a dependent of helper.go; got %v", deps)
	}
}

// An unresolvable name must say so plainly rather than return an empty shell.
func TestToolNode_NotFound(t *testing.T) {
	h := nodeFixture(t)
	res, err := h.Invoke("prism_node", map[string]any{"name": "NoSuchSymbolAnywhere"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]any)
	if matched, _ := m["matched"].(bool); matched {
		t.Errorf("unknown name reported as matched: %v", m)
	}
	if note, _ := m["note"].(string); note == "" {
		t.Errorf("no explanatory note for an unresolvable name: %v", m)
	}
}

func TestToolNode_RequiresName(t *testing.T) {
	h := newH(t)
	if _, err := h.Invoke("prism_node", map[string]any{}); err == nil {
		t.Error("expected an error when name is missing")
	}
}

func edgeGroupNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
