package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestSearchExactCallableIncludesSingleCallerPointer(t *testing.T) {
	root := t.TempDir()
	source := "package sample\nfunc Target() {}\nfunc Caller() { Target() }\n"
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Target", "scope": "symbols"})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := renderSearchAsText(out.(map[string]any))
	if !ok || !strings.Contains(text, "1 indexed caller site(s): Caller src/a.go:3") {
		t.Fatalf("exact callable search missed its compact graph pointer:\n%s", text)
	}
}
