package grove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	groveeng "github.com/tabladrum/grove-suite/grove/pkg/grove"
)

func writePrismFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClient_RequiresEnsureRunning(t *testing.T) {
	c := NewClient("", "")
	ctx := context.Background()
	if _, err := c.Status(ctx); err == nil {
		t.Fatal("expected status error before EnsureRunning")
	}
	if _, err := c.Index(ctx, ""); err == nil {
		t.Fatal("expected index error before EnsureRunning")
	}
	if _, err := c.QueryByIntent(ctx, "x", 1); err == nil {
		t.Fatal("expected query error before EnsureRunning")
	}
	if _, err := c.SearchSymbols(ctx, "x", 1); err == nil {
		t.Fatal("expected symbols error before EnsureRunning")
	}
	if _, err := c.Deps(ctx, "x"); err == nil {
		t.Fatal("expected deps error before EnsureRunning")
	}
	if _, err := c.Impact(ctx, "x", 1); err == nil {
		t.Fatal("expected impact error before EnsureRunning")
	}
	if _, err := c.Semantic(ctx, "x", 1); err == nil {
		t.Fatal("expected semantic error before EnsureRunning")
	}
	if _, err := c.Tests(ctx, "x"); err == nil {
		t.Fatal("expected tests error before EnsureRunning")
	}
}

func TestClient_EnsureRunningRequiresRoot(t *testing.T) {
	err := NewClient("", "").EnsureRunning(context.Background())
	if err == nil || !strings.Contains(err.Error(), "WithTokenFromDir") {
		t.Fatalf("expected root error, got %v", err)
	}
}

func TestClient_EndToEndEmbedded(t *testing.T) {
	dir := t.TempDir()
	_ = writePrismFile(t, dir, "main.go", "package main\n\nfunc Main() {}\n")
	_ = writePrismFile(t, dir, "use.go", "package main\n\nfunc Use() { Main() }\n")
	_ = writePrismFile(t, dir, "main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) { Main() }\n")

	c := NewClient("ignored", "ignored").WithTokenFromDir(dir)
	if got := c.BaseURL(); got != "embedded://grove" {
		t.Fatalf("baseURL marker mismatch: %q", got)
	}

	ctx := context.Background()
	if err := c.EnsureRunning(ctx); err != nil {
		t.Fatalf("ensure running: %v", err)
	}
	if err := c.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	// Idempotent open path.
	if err := c.EnsureRunning(ctx); err != nil {
		t.Fatalf("ensure running (2nd): %v", err)
	}

	if _, err := c.Index(ctx, dir); err != nil {
		t.Fatalf("index: %v", err)
	}
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.FilesIndexed == 0 {
		t.Fatalf("expected indexed files, got %+v", st)
	}

	syms, err := c.SearchSymbols(ctx, "Main", 10)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("expected Main symbol")
	}

	if _, err := c.QueryByIntent(ctx, "main entry point", 10); err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, err := c.Deps(ctx, syms[0].FilePath); err != nil {
		t.Fatalf("deps: %v", err)
	}
	if _, err := c.Impact(ctx, "Main", 3); err != nil {
		t.Fatalf("impact: %v", err)
	}
	if _, err := c.Semantic(ctx, "main entry", 5); err != nil {
		t.Fatalf("semantic: %v", err)
	}
	if _, err := c.Tests(ctx, "Main"); err != nil {
		t.Fatalf("tests: %v", err)
	}

	c.Shutdown()
	if err := c.Health(ctx); err == nil {
		t.Fatal("expected health error after shutdown")
	}
	// Idempotent shutdown.
	c.Shutdown()
}

func TestConvertSymbol(t *testing.T) {
	in := groveeng.Symbol{
		ID:            "id-1",
		FilePath:      "main.go",
		BlobSHA:       "abc",
		Language:      "go",
		Kind:          "function",
		Name:          "Main",
		QualifiedName: "main.Main",
		Signature:     "func Main()",
		Docstring:     "doc",
		Span: struct {
			Start int `json:"start"`
			End   int `json:"end"`
		}{Start: 1, End: 2},
		ParentSymbol:   "",
		Imports:        []string{"fmt"},
		Exports:        true,
		RawText:        "func Main() {}",
		Modifiers:      []string{"public"},
		TypeParameters: []string{"T"},
		Annotations:    []string{"anno"},
	}

	out := convertSymbol(in)
	if out.ID != in.ID || out.Name != in.Name {
		t.Fatalf("bad conversion: %+v", out)
	}

	many := convertSymbols([]groveeng.Symbol{in})
	if len(many) != 1 || many[0].ID != in.ID {
		t.Fatalf("bad convertSymbols: %+v", many)
	}
}
