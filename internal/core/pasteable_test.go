package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// GAP-01: each of several resolve candidates carries a name that resolves
// to it alone, and lookup accepts that name.
func TestResolvePasteableNames(t *testing.T) {
	dir := t.TempDir()
	planWrite(t, dir, "go.mod", "module example.com/p\n\ngo 1.26\n")
	planWrite(t, dir, "alpha/s.go", "package alpha\n\ntype Server struct{}\n\nfunc (Server) Handler() {}\n")
	planWrite(t, dir, "beta/s.go", "package beta\n\ntype Server struct{}\n\nfunc (Server) Handler() {}\n")
	gc := grove.NewClient().WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_resolve", map[string]any{"name": "Server.Handler"})
	if err != nil {
		t.Fatal(err)
	}
	cands, _ := out.(map[string]any)["candidates"].([]map[string]any)
	if len(cands) != 2 {
		t.Fatalf("candidates: %v", out)
	}
	for _, c := range cands {
		paste, _ := c["pasteable"].(string)
		if paste == "" {
			t.Fatalf("no pasteable name: %v", c)
		}
		got, err := h.Invoke("prism_lookup", map[string]any{"name": paste})
		if err != nil || strings.Contains(fmt.Sprint(got), "ambiguous") || !strings.Contains(fmt.Sprint(got), fmt.Sprint(c["file"])) {
			t.Errorf("%s does not resolve to %v alone: %v %v", paste, c["file"], err, got)
		}
	}
}

// GAP-02: references to a type list the lines that use it, not only its
// declaration; every resolved site has a kind and kind= filters on it.
func TestReferencesTypeUsesAndKinds(t *testing.T) {
	dir := t.TempDir()
	planWrite(t, dir, "go.mod", "module example.com/p\n\ngo 1.26\n")
	planWrite(t, dir, "a.go", "package p\n\ntype Cfg struct{ N int }\n\nfunc New() *Cfg { return &Cfg{} }\n\nfunc Set(c *Cfg) { c.N = 2 }\n\nfunc Get(c *Cfg) int { return c.N }\n")
	gc := grove.NewClient().WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_references", map[string]any{"name": "p.Cfg"})
	if err != nil {
		t.Fatal(err)
	}
	sites := fmt.Sprint(out.(map[string]any)["sites"])
	for _, want := range []string{"a.go:5:", "a.go:7:", "a.go:9:", "[type-use]", "[decl]"} {
		if !strings.Contains(sites, want) {
			t.Errorf("type references lack %q: %s", want, sites)
		}
	}
	out, err = h.Invoke("prism_references", map[string]any{"name": "Cfg.N", "kind": "write"})
	if err != nil {
		t.Fatal(err)
	}
	sites = fmt.Sprint(out.(map[string]any)["sites"])
	if !strings.Contains(sites, "a.go:7:") || strings.Contains(sites, "a.go:9:") {
		t.Errorf("kind=write: %s", sites)
	}
}
