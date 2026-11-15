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

// The default (compact) tool surface must accept the repo argument its own
// answers and instructions name.
func TestCompactSurfaceAcceptsRepo(t *testing.T) {
	for op, args := range map[string]map[string]any{
		"search": {"terms": "x", "repo": "lib"},
		"lookup": {"name": "x", "repo": "lib"},
	} {
		allowed := false
		for _, f := range compactFields[op] {
			allowed = allowed || f == "repo"
		}
		if !allowed {
			t.Fatalf("compact %s rejects repo (args %v)", op, args)
		}
	}
}

// A long-lived server picks up a link confirmed after it started.
func TestHandlerReloadsWorkspaceWhenLinksChange(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	for rel, content := range map[string]string{
		"app/prism.yaml": "version: 2\n", "app/a.go": "package app\n",
		"lib/go.mod": "module example.com/lib\n", "lib/lib.go": "package lib\n\n// LateNeedle\n",
	} {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(parent, "app")
	cfg, _ := config.LoadFromDir(root)
	h := NewHandlerWithLedger(cfg, root, nil, session.NewLedger("t"))
	if h.Workspace != nil {
		t.Fatal("a workspace before any link")
	}
	if err := os.WriteFile(filepath.Join(root, "prism.yaml"), []byte("version: 2\nworkspace_repo: lib=../lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, _ := config.LoadFromDir(root)
	workspace.ConfirmLinks(root, cfg2)
	h.reloadWorkspace()
	if h.Workspace == nil || len(h.Workspace.Linked()) != 1 {
		t.Fatalf("the server did not pick up the new link: %+v", h.Workspace)
	}
}

// path=../lib/... searches the linked repository (it used to be rejected as
// outside the root, with a hint to run prism there), and the default tail
// shows the first linked matches instead of only a count.
func TestSearchLinkedPathAndPreview(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	for rel, content := range map[string]string{
		"app/prism.yaml": "version: 2\nworkspace_repo: lib=../lib\n", "app/a.go": "package app\n\n// NeedleShared\n",
		"lib/go.mod": "module example.com/lib\n", "lib/sub/lib.go": "package sub\n\n// NeedleShared in lib\n",
		"other/x.go": "package other\n",
	} {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(parent, "app")
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	cfg, _ := config.LoadFromDir(root)
	workspace.ConfirmLinks(root, cfg)
	h := NewHandlerWithLedger(cfg, root, nil, session.NewLedger("t"))

	id, rels, ok := h.linkedPathRepo(map[string]any{"path": []string{"../lib/sub"}})
	if !ok || id != "lib" || len(rels) != 1 || rels[0] != "sub" {
		t.Fatalf("../lib/sub -> %q %v %v", id, rels, ok)
	}
	if _, _, ok := h.linkedPathRepo(map[string]any{"path": []string{"../lib/sub", "a.go"}}); ok {
		t.Fatal("a path in this repository must not be routed")
	}
	if _, _, ok := h.linkedPathRepo(map[string]any{"path": []string{"../other"}}); ok {
		t.Fatal("an unlinked directory must not be routed")
	}
	out, err := h.toolSearch(t.Context(), map[string]any{"query": "NeedleShared", "path": []string{"../lib/sub"}})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); !strings.Contains(m["linkedSearch"].(string), "../lib/sub/lib.go:3:") {
		t.Fatalf("routed search: %v", out)
	}
	if hint := h.outsideRootHint(); !strings.Contains(hint, "--repo <id>") || !strings.Contains(hint, "linked: lib") {
		t.Fatalf("hint: %q", hint)
	}
	tail := h.LinkedSearchTail(t.Context(), map[string]any{"query": "NeedleShared"})
	if !strings.Contains(tail, "lib 1 in 1 file(s)") || !strings.Contains(tail, "../lib/sub/lib.go:3: // NeedleShared in lib") {
		t.Fatalf("tail:\n%s", tail)
	}
}

// "lib/" names a linked repository and "sub/" a directory of this one: both
// scope the search instead of searching the literal text (multi-repo bench
// 2026-11-14: `search sigstore-go/` returned ~10k characters of noise).
func TestSearchPathLikeTermsScope(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	for rel, content := range map[string]string{
		"app/prism.yaml": "version: 2\nworkspace_repo: lib=../lib\n", "app/a.go": "package app\n\n// NeedleShared lib/ mention\n",
		"app/sub/s.go": "package sub\n\n// NeedleShared in app sub\n",
		"lib/go.mod":   "module example.com/lib\n", "lib/sub/lib.go": "package sub\n\n// NeedleShared in lib\n",
	} {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(parent, "app")
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	cfg, _ := config.LoadFromDir(root)
	workspace.ConfirmLinks(root, cfg)
	h := NewHandlerWithLedger(cfg, root, nil, session.NewLedger("t"))

	// A linked repository with a term: searched there, with a note.
	out, err := h.toolSearch(t.Context(), map[string]any{"query": []string{"NeedleShared", "lib/"}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.(map[string]any)
	got, _ := m["linkedSearch"].(string)
	if !strings.Contains(got, `treated "lib/" as a path scope (--repo lib)`) || !strings.Contains(got, "../lib/sub/lib.go:3:") || strings.Contains(got, "app") {
		t.Fatalf("lib/ + term:\n%s", got)
	}
	// Only the directory: its files are listed.
	out, err = h.toolSearch(t.Context(), map[string]any{"query": "lib/sub/"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = out.(map[string]any)["linkedSearch"].(string)
	if !strings.Contains(got, "../lib/sub/lib.go") || strings.Contains(got, "go.mod") {
		t.Fatalf("lib/sub/ listing:\n%s", got)
	}
	// A directory of this repository.
	out, err = h.toolSearch(t.Context(), map[string]any{"query": "sub/"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = out.(map[string]any)["linkedSearch"].(string)
	if !strings.Contains(got, "sub/s.go") || strings.Contains(got, "lib.go") {
		t.Fatalf("sub/ listing:\n%s", got)
	}
	// No trailing slash, nothing real, or a regex: left as text.
	for _, args := range []map[string]any{
		{"query": "lib/sub"}, {"query": "nosuch/"}, {"query": "lib/", "regex": true}, {"query": "lib/", "repo": "lib"},
	} {
		if _, note, _, ok := h.pathScopeTerms(args); ok {
			t.Fatalf("%v rewritten: %s", args, note)
		}
	}
}
