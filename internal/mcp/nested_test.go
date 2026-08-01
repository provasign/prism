package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func sym(raw string, start int, sites ...grove.CallSite) grove.SymbolRecord {
	return grove.SymbolRecord{
		Name: "outer", RawText: raw,
		Span: grove.SpanInfo{Start: start, End: start + 20}, CallSites: sites,
	}
}

// The django case: the call lives inside a nested def, and grove attributes it
// to the enclosing method — so the reader needs the nested scope's name.
func TestNestedScopeFor_PythonNestedDef(t *testing.T) {
	raw := "def table_names(self, cursor=None):\n" +
		"    def get_names(cursor):\n" +
		"        return sorted(self.get_table_list(cursor))\n" +
		"    return get_names(cursor)\n"
	s := sym(raw, 52, grove.CallSite{Callee: "self.get_table_list", Line: 54})
	if got := nestedScopeFor(s, "get_table_list"); got != "get_names" {
		t.Errorf("got %q, want get_names", got)
	}
}

// A call directly in the caller's body must produce NO annotation, so the
// common case renders exactly as it did before this feature.
func TestNestedScopeFor_DirectCallNoAnnotation(t *testing.T) {
	raw := "def handle(self):\n" +
		"    return self.get_table_list(None)\n"
	s := sym(raw, 10, grove.CallSite{Callee: "self.get_table_list", Line: 11})
	if got := nestedScopeFor(s, "get_table_list"); got != "" {
		t.Errorf("direct call annotated with %q; want no annotation", got)
	}
}

func TestNestedScopeFor_GoClosure(t *testing.T) {
	raw := "func Run() {\n" +
		"\thandler := func() {\n" +
		"\t\tRender(w)\n" +
		"\t}\n" +
		"\thandler()\n" +
		"}\n"
	s := sym(raw, 100, grove.CallSite{Callee: "Render", Line: 102})
	if got := nestedScopeFor(s, "Render"); got != "handler" {
		t.Errorf("got %q, want handler", got)
	}
}

// Guard rails: missing data must never panic or invent a scope.
func TestNestedScopeFor_Degrades(t *testing.T) {
	cases := []struct {
		name string
		s    grove.SymbolRecord
	}{
		{"no rawtext", grove.SymbolRecord{Span: grove.SpanInfo{Start: 1}, CallSites: []grove.CallSite{{Callee: "x", Line: 2}}}},
		{"no callsites", sym("def f():\n    pass\n", 1)},
		{"zero span", grove.SymbolRecord{RawText: "def f():\n", CallSites: []grove.CallSite{{Callee: "x", Line: 2}}}},
		{"line out of range", sym("def f():\n    pass\n", 1, grove.CallSite{Callee: "x", Line: 9999})},
		{"single line", sym("def f(): pass", 1, grove.CallSite{Callee: "x", Line: 1})},
	}
	for _, c := range cases {
		if got := nestedScopeFor(c.s, "x"); got != "" {
			t.Errorf("%s: got %q, want empty", c.name, got)
		}
	}
}

// A Go method header ("func (r T) M(") is not a nested scope.
func TestNestedFuncName(t *testing.T) {
	cases := map[string]string{
		"    def get_names(cursor):":  "get_names",
		"\thandler := func() {":       "handler",
		"  const cb = function () {":  "",
		"func (r JSON) Render(w) {":   "",
		"    return sorted(x)":        "",
		"":                            "",
	}
	for in, want := range cases {
		if got := nestedFuncName(in); got != want {
			t.Errorf("nestedFuncName(%q) = %q, want %q", in, got, want)
		}
	}
}
