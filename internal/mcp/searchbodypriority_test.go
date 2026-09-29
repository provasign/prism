package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestSearchBodiesPreferSecondProductionRegionBeforeTest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"src/impl.go":      "package p\nfunc Primary() { _ = \"alpha\" }\nfunc Secondary() { _ = \"alpha\" }\n",
		"src/impl_test.go": "package p\nfunc TestCase() { _ = \"beta\" }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Index(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	out := map[string]any{"results": []map[string]any{
		{"textHits": []map[string]any{{"file": "src/impl.go", "hits": []map[string]any{{"line": 2}, {"line": 3}}}}},
		{"textHits": []map[string]any{{"file": "src/impl_test.go", "hits": []map[string]any{{"line": 2}}}}},
	}}
	text := h.compactSearchBodiesEnclosing(t.Context(), out)
	second := strings.Index(text, "Full enclosing body Secondary")
	test := strings.Index(text, "Full enclosing body TestCase")
	if second < 0 || (test >= 0 && test < second) {
		t.Fatalf("test body displaced the second production region:\n%s", text)
	}
}
