package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// TestToolLookup_TypeMethodDisambiguation checks that a Type.Method name resolves
// to that exact method, not another type's same-named method or a mock — the bug
// the grafana benchmark surfaced (lookup returned the mock/wrong Get).
func TestToolLookup_TypeMethodDisambiguation(t *testing.T) {
	dir := t.TempDir()
	// Two real types with a Get method, plus a mock with the same method name.
	os.WriteFile(filepath.Join(dir, "store.go"), []byte(
		"package p\n\ntype SQLStore struct{}\n\nfunc (s *SQLStore) Get(k string) string { return \"sql:\" + k }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cache.go"), []byte(
		"package p\n\ntype CacheStore struct{}\n\nfunc (c *CacheStore) Get(k string) string { return \"cache:\" + k }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "store_mock.go"), []byte(
		"package p\n\ntype MockStore struct{}\n\nfunc (m *MockStore) Get(k string) string { return \"mock\" }\n"), 0o644)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	check := func(name, wantFile string) {
		out, err := h.Invoke("prism_lookup", map[string]any{"name": name})
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		m := out.(map[string]any)
		sym, _ := m["symbol"].(grove.SymbolRecord)
		if !strings.HasSuffix(sym.FilePath, wantFile) {
			t.Errorf("lookup %q resolved to %q, want %s (content=%q)", name, sym.FilePath, wantFile, m["content"])
		}
	}
	check("SQLStore.Get", "store.go")
	check("CacheStore.Get", "cache.go")
	// pkg-qualified form should also land on the real SQL store.
	check("p.SQLStore.Get", "store.go")
}

// TestToolLookup_QualifierRejectsOtherOwner: "Context.Errors" named a struct
// field, and lookup answered with errorMsgs.Errors -- another type's method --
// as a confident match (gin, 2026-09-25). A qualifier that names a different
// owner must never produce a confident match.
func TestToolLookup_QualifierRejectsOtherOwner(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "context.go"), []byte(
		"package p\n\ntype Context struct {\n\tErrors errorMsgs\n}\n\n"+
			"func (c *Context) SetErrors(e errorMsgs) { c.Errors = e }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "errors.go"), []byte(
		"package p\n\ntype errorMsgs []string\n\nfunc (a errorMsgs) Errors() []string { return a }\n"), 0o644)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	lookup := func(name string) (grove.SymbolRecord, map[string]any) {
		out, err := h.Invoke("prism_lookup", map[string]any{"name": name})
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		m := out.(map[string]any)
		sym, _ := m["symbol"].(grove.SymbolRecord)
		return sym, m
	}

	sym, m := lookup("Context.Errors")
	if sym.QualifiedName == "errorMsgs.Errors" && m["matched"] != false {
		t.Fatalf("Context.Errors resolved to errorMsgs.Errors as a confident match: %v", m)
	}
	if m["matched"] == false && sym.QualifiedName != "" && sym.QualifiedName != "Context.SetErrors" {
		t.Errorf("closest hit should be owned by Context, got %s", sym.QualifiedName)
	}
	// The owner-matching and unqualified forms still resolve.
	if sym, m := lookup("errorMsgs.Errors"); sym.QualifiedName != "errorMsgs.Errors" || m["matched"] == false {
		t.Errorf("errorMsgs.Errors: got %s %v", sym.QualifiedName, m["matched"])
	}
	// With Go struct fields indexed, the qualified name lands on the field
	// itself and the bare name is honestly ambiguous between the two.
	if sym.Kind == "field" {
		if sym.QualifiedName != "Context.Errors" || m["matched"] == false {
			t.Errorf("Context.Errors: got %s %v", sym.QualifiedName, m["matched"])
		}
		if _, m := lookup("Errors"); m["ambiguous"] != true {
			t.Errorf("bare Errors should list both the field and the method: %v", m)
		}
	}
	if sym, m := lookup("p.errorMsgs.Errors"); sym.QualifiedName != "errorMsgs.Errors" || m["matched"] == false {
		t.Errorf("p.errorMsgs.Errors: got %s %v", sym.QualifiedName, m["matched"])
	}
}
