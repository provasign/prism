package mcp

import (
	"strings"
	"testing"
)

func manySymbolsFixture(t *testing.T) *Server {
	return compactFixture(t, map[string]string{
		"app.go":     "package r\n\ntype App struct{}\n\nfunc (a *App) MakeResponse(v int) int {\n\tx := v + 1\n\treturn x\n}\n",
		"helpers.go": "package r\n\nfunc MakeResponse(v int) int {\n\ty := v * 2\n\treturn y\n}\n",
		"unique.go":  "package r\n\nfunc FullDispatch(v int) int {\n\tz := MakeResponse(v)\n\treturn z\n}\n",
	})
}

// Several listed symbols: the one picked body was rarely the one wanted, so
// the answer stays a locator with a pointer to lookup.
func TestSearchListingSeveralSymbolsAttachesNoBody(t *testing.T) {
	srv := manySymbolsFixture(t)
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"MakeResponse"}})
	if strings.Contains(out, "**`") || !strings.Contains(out, "No source attached: 2 symbols match") {
		t.Fatalf("two listed symbols should give a locator answer and the lookup note:\n%s", out)
	}
	limited := callCompact(t, srv, "search", map[string]any{"terms": []any{"MakeResponse"}, "max_results": 20})
	if strings.Contains(limited, "**`") {
		t.Fatalf("non-default shape still attached a body for several symbols:\n%s", limited)
	}
	explicit := callCompact(t, srv, "search", map[string]any{"terms": []any{"MakeResponse"}, "include_bodies": true})
	if !strings.Contains(explicit, "**`") || strings.Contains(explicit, "No source attached") {
		t.Fatalf("explicit include_bodies=true lost its bodies:\n%s", explicit)
	}
}

func TestSearchListingOneSymbolKeepsItsBody(t *testing.T) {
	srv := manySymbolsFixture(t)
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"FullDispatch"}})
	if strings.Contains(out, "No source attached") || !strings.Contains(out, "z := MakeResponse(v)") {
		t.Fatalf("a single listed symbol should keep its body:\n%s", out)
	}
}
