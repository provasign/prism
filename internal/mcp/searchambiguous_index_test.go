package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// urfave pr2355: a scope=text search lists no symbols, so the shared-name
// guard saw nothing and printed one PreParse body of several. The index
// knows the other declarations; the body is replaced by all of them.
func TestTextSearchBodyForSharedNameUsesIndex(t *testing.T) {
	root := t.TempDir()
	src := `package flags

type Flag interface {
	PreParse() error
}

type BoolFlag struct{ applied bool }

func (b *BoolFlag) PreParse() error {
	b.applied = true
	return nil
}

type StringFlag struct{ applied bool }

func (s *StringFlag) PreParse() error {
	s.applied = true
	return nil
}

func UniqueHelper() int {
	return 42
}
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/flags\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "flags.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	render := func(term string) string {
		args := map[string]any{"query": []any{term}, "scope": "text"}
		out, err := h.Invoke("prism_search", args)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := h.RenderCompactSearchText(t.Context(), out.(map[string]any), args)
		return text
	}
	got := render("PreParse")
	if !strings.Contains(got, `different symbols share the name "PreParse"`) ||
		!strings.Contains(got, "BoolFlag.PreParse") || !strings.Contains(got, "StringFlag.PreParse") {
		t.Fatalf("shared-name candidates missing:\n%s", got)
	}
	if strings.Contains(got, "Full enclosing body") {
		t.Fatalf("one of several PreParse bodies was printed:\n%s", got)
	}
	if got := render("UniqueHelper"); !strings.Contains(got, "return 42") {
		t.Fatalf("a unique name lost its body:\n%s", got)
	}
}
