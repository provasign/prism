package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestToolReferences_E2E drives the references layer end-to-end through the MCP
// handler: index a temp repo, then ask prism_references "where is StringUtils
// used". The resolved call graph would miss the class (a class is referenced,
// not called); the reference layer must find the var-type and composite-literal
// uses in app.go while excluding the comment.
func TestToolReferences_E2E(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "util.go"), []byte("package p\n\ntype StringUtils struct{}\n\nfunc (StringUtils) IsEmpty() bool { return true }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package p\n\n// StringUtils note\nfunc run(){\n\tvar u StringUtils\n\t_ = u.IsEmpty()\n\t_ = StringUtils{}\n}\n"), 0o644)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	// Close the engine before t.TempDir() cleanup runs: an open grove.db handle
	// blocks RemoveAll on Windows (POSIX silently unlinks open files; Windows
	// does not).
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)

	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	out, err := h.Invoke("prism_references", map[string]any{"name": "StringUtils"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	t.Logf("prism_references(StringUtils) => count=%v definitions=%v ambiguous=%v byFile=%v",
		m["count"], m["definitions"], m["ambiguous"], m["byFile"])
	if c, _ := m["count"].(int); c < 2 {
		t.Fatalf("expected >=2 references (var type + composite lit in app.go), got %v", m["count"])
	}
}
