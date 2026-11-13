package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/workspace"
)

// The MCP search op takes repo=<id> without a schema entry and answers with
// that linked repository's matches, under paths relative to the server root.
func TestSearchRepoArgumentOnTheToolSurface(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	write := func(rel, content string) {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/prism.yaml", "version: 2\nworkspace_repo: lib=../lib\n")
	write("app/a.go", "package app\n")
	write("lib/go.mod", "module example.com/lib\n")
	write("lib/lib.go", "package lib\n\n// NeedleInLib\n")
	root := filepath.Join(parent, "app")
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// A hand-written link is a proposal until a command confirms it.
	if ws := workspace.Load(root, cfg); ws == nil || len(ws.Linked()) != 0 || ws.Roots[0].Refused != workspace.Unconfirmed {
		t.Fatalf("an unconfirmed link was used: %+v", ws)
	}
	workspace.ConfirmLinks(root, cfg)
	h := NewHandlerWithLedger(cfg, root, nil, session.NewLedger("t"))

	args := map[string]any{"query": "NeedleInLib", "repo": "lib"}
	if err := rejectUnknownArgs("prism_search", args); err != nil {
		t.Fatalf("repo rejected as an unknown argument: %v", err)
	}
	out, err := h.toolSearch(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	text, rendered := h.RenderToolText(true, "prism_search", args, out)
	if !rendered || !strings.Contains(text, "../lib/lib.go:3: // NeedleInLib") {
		t.Fatalf("rendered=%v text:\n%s", rendered, text)
	}
	if _, err := h.toolSearch(t.Context(), map[string]any{"query": "x", "repo": "nope"}); err == nil ||
		!strings.Contains(err.Error(), "linked: lib (../lib)") {
		t.Fatalf("unknown repo: %v", err)
	}
	if props := toolSchema("prism_search")["properties"].(map[string]any); props["repo"] != nil {
		t.Fatal("repo was added to the published schema; it is meant to stay out of it")
	}
}
