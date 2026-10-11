package mcp

import (
	"strings"
	"testing"
)

// jackson-databind pr6042 (Sonnet 5.5, v0.87.7, 2026-10-11): a batched search
// with context=40 (clamped to 15) returned the context windows AND the full
// enclosing body of every hit, including two callers the agent never asked
// about — 13.7k chars, the windows printed twice. The schema promises that an
// explicit context disables enclosing-body delivery; across 22 benchmark
// searches with context, every follow-up edit used window lines, never
// body-only lines.
func TestSearchExplicitContextDeliversNoBodies(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"Deser.java": "package p;\n\nclass Deser {\n" +
			"    Object fromArray(Object p) {\n        int act = findCoercion(p);\n        if (act > 0) {\n            return null;\n        }\n        return p;\n    }\n\n" +
			"    String parseString(Object p) {\n        if (p == null) {\n            return null;\n        }\n        return p.toString();\n    }\n\n" +
			"    int findCoercion(Object p) {\n        return 1;\n    }\n}\n",
	})
	args := map[string]any{"terms": []any{"String parseString(", "findCoercion("}, "scope": "text"}

	withCtx := map[string]any{"context": 3}
	for k, v := range args {
		withCtx[k] = v
	}
	got := callCompact(t, srv, "search", withCtx)
	if strings.Contains(got, "Full enclosing body") || strings.Contains(got, "// WINDOW") {
		t.Fatalf("explicit context must not attach enclosing bodies:\n%s", got)
	}
	if !strings.Contains(got, "int act = findCoercion(p);") || !strings.Contains(got, "String parseString(Object p)") {
		t.Fatalf("context windows missing:\n%s", got)
	}

	// Context past the cap asks for the function: bodies are delivered and
	// their lines are not repeated in the windows.
	wide := map[string]any{"context": 40}
	for k, v := range args {
		wide[k] = v
	}
	got = callCompact(t, srv, "search", wide)
	if !strings.Contains(got, "Full enclosing body Deser.parseString") || !strings.Contains(got, "enclosing functions are delivered") {
		t.Fatalf("context past the cap should deliver the enclosing function:\n%s", got)
	}
	// Match lines stay as locators; context lines must not repeat body text.
	for _, line := range []string{"return p.toString();", "if (act > 0) {", "return 1;"} {
		if n := strings.Count(got, line); n != 1 {
			t.Fatalf("%q printed %d times; a line must not appear in both a window and a body:\n%s", line, n, got)
		}
	}

	// An explicit include_bodies=true still asks for both.
	both := map[string]any{"include_bodies": true}
	for k, v := range withCtx {
		both[k] = v
	}
	if got := callCompact(t, srv, "search", both); !strings.Contains(got, "Full enclosing body") {
		t.Fatalf("include_bodies=true with context should still deliver bodies:\n%s", got)
	}
}
