package mcp

import (
	"strings"
	"testing"
)

func TestRenderReadAsText_WholeFile(t *testing.T) {
	out := map[string]any{
		"file": "pkg/a.go", "strategy": "compressed",
		"originalTokens": 900, "deliveredTokens": 500, "savingsPercent": 44,
		"content": "1\tpackage a\n2\tfunc F() {}\n",
	}
	text, ok := renderReadAsText(out)
	if !ok {
		t.Fatal("known whole-file shape must render")
	}
	if !strings.Contains(text, "pkg/a.go") || !strings.Contains(text, "func F() {}") {
		t.Errorf("header or content missing: %q", text)
	}
	if strings.Contains(text, "originalTokens") {
		t.Error("bookkeeping fields must not be rendered — they are envelope, not content")
	}
}

func TestRenderReadAsText_RangedRead(t *testing.T) {
	out := map[string]any{
		"file": "pkg/a.go", "delivery": "range",
		"startLine": 10, "endLine": 20, "totalLines": 300,
		"content": "10\tx := 1\n", "warning": "past EOF",
	}
	text, ok := renderReadAsText(out)
	if !ok {
		t.Fatal("ranged shape must render")
	}
	for _, want := range []string{"lines 10-20 of 300", "x := 1", "past EOF"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestRenderReadAsText_FallsBackOnUnknownField(t *testing.T) {
	out := map[string]any{"file": "a.go", "content": "x", "surprise": true}
	if _, ok := renderReadAsText(out); ok {
		t.Fatal("unknown field must force JSON fallback, not silent drop")
	}
}

func TestRenderChangeImpactAsText_FullShape(t *testing.T) {
	sym := func(qn, fp string, line int) map[string]any {
		return map[string]any{"name": qn, "qualifiedName": qn, "filePath": fp, "line": line,
			"kind": "method", "signature": "def x()"}
	}
	out := map[string]any{
		"query": "CacheBase.get", "totalSites": 4, "completeness": "closed",
		"declarations": []map[string]any{sym("CacheBase.get", "cache/__init__.py", 58)},
		"supers":       []map[string]any{},
		"family": []map[string]any{
			sym("Cache.get", "cache/memory.py", 74),
			sym("Cache.get", "cache/memcached.py", 80),
		},
		"callers": []map[string]any{
			{"name": "heartbeat", "qualifiedName": "heartbeat", "filePath": "cache/__init__.py",
				"line": 75, "kind": "function", "signature": "def heartbeat()", "via": "ping"},
		},
	}
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatal("full known shape must render")
	}
	for _, want := range []string{
		"CacheBase.get — change-impact: 4 site(s)", "completeness: closed",
		"family (2):", "cache/memory.py:74", "callers (1):", "(via ping)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "signature") {
		t.Error("per-entry JSON keys must not leak into the text form")
	}
}

func TestRenderChangeImpactAsText_NothingSilentlyDropped(t *testing.T) {
	out := map[string]any{
		"query": "X.m", "totalSites": 1, "completeness": "project-local",
		"declarations":      []map[string]any{{"name": "X.m", "qualifiedName": "X.m", "filePath": "x.py", "line": 1, "kind": "method", "signature": "s"}},
		"overridesExternal": []string{"ABC"},
		"warning":           "external contract",
		"externalSupers":    []string{"ABC"},
		"declaringTypes":    []map[string]any{{"name": "X", "qualifiedName": "X", "filePath": "x.py", "line": 1, "kind": "class", "signature": "s"}},
		"declaringTypesNote": "type blocks change too",
		"widerAnchor": map[string]any{"qualifiedName": "Iface.m", "totalSites": 9,
			"completeness": "closed", "note": "wider"},
	}
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatal("must render")
	}
	for _, want := range []string{"project-local", "ABC", "external contract",
		"declaringTypes (1):", "type blocks change too", "Iface.m", "wider"} {
		if !strings.Contains(text, want) {
			t.Errorf("field dropped from text rendering: %q\n%s", want, text)
		}
	}
}

func TestRenderChangeImpactAsText_FallsBackOnUnknownField(t *testing.T) {
	out := map[string]any{"query": "X", "totalSites": 0, "newField": 1}
	if _, ok := renderChangeImpactAsText(out); ok {
		t.Fatal("unknown field must force JSON fallback")
	}
}
