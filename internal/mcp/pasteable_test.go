package mcp

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
