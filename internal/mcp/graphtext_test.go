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

func TestRenderLookupAsText_FoundSymbol(t *testing.T) {
	out := map[string]any{
		"symbol": map[string]any{"qualifiedName": "CacheBase.get", "kind": "method",
			"filePath": "cache/__init__.py", "span": map[string]any{"start": 58, "end": 66},
			"rawText": "def get(self, key): ...", "blobSha": "beef", "id": "s1"},
		"content": "def get(self, key): ...",
	}
	text, ok := renderLookupAsText(out)
	if !ok {
		t.Fatal("found shape must render")
	}
	if !strings.Contains(text, "CacheBase.get") || !strings.Contains(text, "cache/__init__.py:58-66") {
		t.Errorf("header missing: %q", text)
	}
	if strings.Count(text, "def get(self, key)") != 1 {
		t.Errorf("body must appear exactly once (JSON shipped it twice): %q", text)
	}
	if strings.Contains(text, "beef") {
		t.Error("index internals must not leak")
	}
}

func TestRenderLookupAsText_MissAndProjection(t *testing.T) {
	miss := map[string]any{"symbol": nil, "name": "Nope", "matched": false,
		"note": "no symbol named \"Nope\"", "candidates": []string{"Near (a.py)"}}
	text, ok := renderLookupAsText(miss)
	if !ok || !strings.Contains(text, "no symbol named") || !strings.Contains(text, "Near (a.py)") {
		t.Errorf("miss shape wrong: ok=%v %q", ok, text)
	}
	proj := map[string]any{"signature": "def f()", "kind": "function"}
	text, ok = renderLookupAsText(proj)
	if !ok || !strings.Contains(text, "signature: def f()") {
		t.Errorf("projection shape wrong: ok=%v %q", ok, text)
	}
	if _, ok := renderLookupAsText(map[string]any{"symbol": nil, "shiny": 1}); ok {
		t.Error("unknown field must force JSON fallback")
	}
}

func TestRenderVerifyAsText_CleanAndIncomplete(t *testing.T) {
	clean := map[string]any{"verdict": "clean", "base": "HEAD", "note": "no changes vs HEAD"}
	text, ok := renderVerifyAsText(clean)
	if !ok || !strings.Contains(text, "verify: clean") {
		t.Errorf("clean shape: ok=%v %q", ok, text)
	}
	inc := map[string]any{
		"verdict": "incomplete", "base": "HEAD",
		"changedFiles":     []string{"a.py", "b.py"},
		"signatureChanges": []map[string]any{{"file": "a.py", "line": 657, "reason": "signature of X changed"}},
		"missedSites": []map[string]any{{"file": "a.py", "line": 1310,
			"qualifiedName": "P.parse_root_type", "detail": "calls set_title at line 1310"}},
		"unverifiedSeeds": []string{"Y — review manually"},
		"newDependencies": []map[string]any{{"from": "a", "to": "b", "weight": 2, "minTier": "call"}},
		"archStatus":      "review", "archIntroduced": []string{"rule R broken"},
		"notes":           []string{"scored against HEAD"},
	}
	text, ok = renderVerifyAsText(inc)
	if !ok {
		t.Fatal("incomplete shape must render")
	}
	for _, want := range []string{"verify: incomplete — 2 changed files", "MISSED SITES (1)",
		"P.parse_root_type", "UNVERIFIED contract changes (1)", "a -> b  2 crossing(s)",
		"arch rules touched", "rule R broken", "note: scored against HEAD"} {
		if !strings.Contains(text, want) {
			t.Errorf("dropped %q from:\n%s", want, text)
		}
	}
	if _, ok := renderVerifyAsText(map[string]any{"verdict": "complete", "extra": 1}); ok {
		t.Error("unknown field must force JSON fallback")
	}
}

func TestRenderQuerySourceAsText(t *testing.T) {
	out := map[string]any{
		"content": "**Source** — ...\n### a.py\n1\tcode\n",
		"delivery": "source", "deliveredTokens": 100, "symbolCount": 3,
		"files": []string{"a.py"},
		"textMatches": []map[string]any{{"file": "b.cfg",
			"hits": []map[string]any{{"line": 4, "text": "key = 1"}}}},
		"textBackend": "rg",
	}
	text, ok := renderQuerySourceAsText(out)
	if !ok {
		t.Fatal("source delivery must render")
	}
	for _, want := range []string{"### a.py", "b.cfg:4: key = 1", "text matches"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "deliveredTokens") {
		t.Error("bookkeeping must not render")
	}
	if _, ok := renderQuerySourceAsText(map[string]any{"content": "x", "newKey": 1}); ok {
		t.Error("unknown field must force JSON fallback")
	}
}
