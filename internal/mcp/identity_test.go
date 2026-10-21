package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

var namesakeFixture = map[string]string{
	"go.mod":          "module audit\ngo 1.22\n",
	"left/a.go":       "package left\nfunc Target() {}\nfunc Caller() { Target() }\n",
	"right/a.go":      "package right\nfunc Target() {}\nfunc OtherCaller() { Target() }\n",
	"right/a_test.go": "package right\nimport \"testing\"\nfunc TestRightTarget(t *testing.T) { Target() }\n",
}

func leftTarget(t *testing.T, h *Handler) grove.SymbolRecord {
	t.Helper()
	syms, err := h.Grove.FileSymbols(context.Background(), "left/a.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range syms {
		if s.Name == "Target" {
			return s
		}
	}
	t.Fatal("left Target not indexed")
	return grove.SymbolRecord{}
}

// Audit 2026-10-20 #3: the anchor line for left.Target counted right/a.go's
// call to an unrelated Target as its own.
func TestAnchorSummaryCountsCallersByIdentity(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	h := evidenceHandler(t, namesakeFixture)
	ctx := context.Background()
	s := leftTarget(t, h)
	edges, err := h.Grove.Edges(ctx, s.ID, "in", []string{"calls"})
	if err != nil || len(edges) != 1 {
		t.Fatalf("fixture: want 1 identity-resolved caller, got %d (%v)", len(edges), err)
	}
	out := h.renderAnchorSummary(ctx, []grove.SymbolRecord{s}, nil, nil)
	if !strings.Contains(out, "1 caller in `left/a.go`") || strings.Contains(out, "right/") {
		t.Fatalf("anchor summary mixed in a namesake's callers:\n%s", out)
	}
}

// Overloads share a qualified name inside one file; identity expansion keeps
// their callers together.
func TestIdentityIDsKeepSameFileOverloads(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	h := evidenceHandler(t, map[string]string{
		"A.java": "class A {\n  void f() {}\n  void f(int x) {}\n  void g() { f(1); }\n}\n",
		"B.java": "class B {\n  void f() {}\n}\n",
	})
	syms, err := h.Grove.FileSymbols(context.Background(), "A.java")
	if err != nil {
		t.Fatal(err)
	}
	var f grove.SymbolRecord
	n := 0
	for _, s := range syms {
		if s.Name == "f" {
			n++
			if f.ID == "" {
				f = s
			}
		}
	}
	if n < 2 {
		t.Skipf("parser did not index both overloads (%d)", n)
	}
	ids := h.identityIDs(context.Background(), f)
	if len(ids) != 2 {
		t.Fatalf("want both A.f overloads and not B.f, got %v", ids)
	}
	for _, id := range ids {
		if strings.Contains(id, "B.java") {
			t.Fatalf("namesake in another file joined the identity: %v", ids)
		}
	}
}

// A seed selected inside left/ must not pull in right/'s callers or tests
// through call-neighbor expansion.
func TestQueryExpansionDoesNotMixNamesakes(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	srv := compactFixture(t, namesakeFixture)
	text := callCompact(t, srv, "query", map[string]any{"terms": []any{"Target"}, "paths": []any{"left"}})
	for _, bad := range []string{"OtherCaller", "TestRightTarget", "right/a_test.go"} {
		if strings.Contains(text, bad) {
			t.Fatalf("left-scoped query delivered namesake evidence %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "left/a.go") {
		t.Fatalf("query lost its own anchor:\n%s", text)
	}
}
