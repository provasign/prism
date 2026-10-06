package mcp

import (
	"strings"
	"testing"
)

func TestDeclarationTermNamesOneSymbol(t *testing.T) {
	for term, name := range map[string]string{
		"func walk": "walk", "func (mx *Mux) Mount": "Mount", "def add_url_rule": "add_url_rule",
		"def add(": "add", "class Foo": "Foo", "function bar": "bar", "fn baz": "baz", "async def qux": "qux",
		"func (f *FlagBase[T, C, V]) Apply": "Apply",
	} {
		m := declarationTerm.FindStringSubmatch(term)
		if m == nil || m[1] != name {
			t.Errorf("%q declares %v, want %q", term, m, name)
		}
	}
	for _, term := range []string{"walk", "Walk(", "x = walk", "func walk(r Routes) error {", "_SkipClose"} {
		if declarationTerm.MatchString(term) {
			t.Errorf("%q treated as a declaration term", term)
		}
	}
}

// chi pr1148: `func walk` matched the text of `func walkXFF` and search
// delivered walkXFF's body, five sessions in a row.
func TestDeclarationSearchDeliversOnlyTheDeclaredSymbol(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"xff.go":  "package r\n\nfunc walkXFF(v int) int {\n\tx := v + 1\n\treturn x\n}\n",
		"tree.go": "package r\n\nfunc walk(v int) int {\n\ty := walkXFF(v) * 2\n\treturn y\n}\n",
	})
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"func walk"}, "scope": "text"})
	if strings.Contains(out, "body walkXFF") || strings.Contains(out, "enclosing walkXFF") {
		t.Fatalf("declaration term delivered a different symbol's body:\n%s", out)
	}
	if !strings.Contains(out, "y := walkXFF(v) * 2") {
		t.Fatalf("declaration term lost the declared symbol's body:\n%s", out)
	}
	only := compactFixture(t, map[string]string{
		"xff.go": "package r\n\nfunc walkXFF(v int) int {\n\tx := v + 1\n\treturn x\n}\n",
	})
	miss := callCompact(t, only, "search", map[string]any{"terms": []any{"func walk"}, "scope": "text"})
	if strings.Contains(miss, "body walkXFF") || strings.Contains(miss, "enclosing walkXFF") {
		t.Fatalf("no declared symbol: walkXFF's body should not be delivered:\n%s", miss)
	}
}

// The declared symbol wins even when a longer same-prefix name outranks it.
func TestDeclarationSearchFallsBackToTheDeclaredSymbol(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"xff.go":  "package r\n\nfunc walkXFF(v int) int {\n\tx := v + 1\n\tx = x * 3\n\treturn x\n}\n",
		"tree.go": "package r\n\nfunc Walk(v int) int {\n\treturn v\n}\n",
	})
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"func walk", "func Walk"}, "scope": "text"})
	if strings.Contains(out, "body walkXFF") || !strings.Contains(out, "body Walk") {
		t.Fatalf("want Walk's body, not walkXFF's:\n%s", out)
	}
}
