package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// A qualified removed name never appears at its call sites (w.Status()), so
// matching it literally reported "clean" while every caller remained (gin
// ResponseWriter.Status, 2026-10-22). It must be matched by its final
// identifier, and the answer must say so.
func TestVerifyRemovedQualifiedNameFindsCallers(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"rw.go":  "package p\n\ntype ResponseWriter interface {\n\tStatusCode() int\n}\n",
		"use.go": "package p\n\nfunc use(w ResponseWriter) int { return w.Status() }\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)

	for _, name := range []string{"ResponseWriter.Status", "p::ResponseWriter::Status", "ResponseWriter#Status"} {
		out, err := h.Invoke("prism_verify", map[string]any{"removed_symbols": []any{name}})
		if err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
		m := out.(map[string]any)
		if m["clean"] != 0 {
			t.Errorf("%s: reported clean although use.go still calls w.Status()", name)
		}
		text, ok := renderRemovedSymbolsAsText(m)
		if !ok || !strings.Contains(text, "use.go:3") || !strings.Contains(text, "mention(s) of Status") {
			t.Errorf("%s: text answer must list the caller and name the matched identifier:\n%s", name, text)
		}
	}
	// An unqualified name keeps its exact behavior.
	out, err := h.Invoke("prism_verify", map[string]any{"removed_symbols": []any{"Gone"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["clean"] != 1 {
		t.Errorf("an absent plain name must stay clean: %v", out)
	}
}
