package mcp

import (
	"strings"
	"testing"
)

// The shape table. One row per request kind; every column is the decision
// searchShapeFor makes, nothing else derives its own rule from the flags.
func TestSearchShapeTable(t *testing.T) {
	type row struct {
		name   string
		args   map[string]any
		limit  int
		ctx    int
		asked  bool
		bodies bodyMode
		fit    fitMode
		budget bool
		dedupe bool
	}
	q := map[string]any{"query": "needle"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range q {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	rows := []row{
		{"default", q, defaultSearchLimit, 2, false, bodiesOne, fitBudget, true, true},
		{"include_bodies=false", with(map[string]any{"include_bodies": false}), defaultSearchLimit, 2, false, bodiesNone, fitBudget, true, false},
		{"include_bodies=true", with(map[string]any{"include_bodies": true}), defaultSearchLimit, 2, false, bodiesWideNoPick, fitBudgetWide, true, true},
		{"context 0", with(map[string]any{"context": 0}), defaultSearchLimit, 0, true, bodiesNone, fitBounded, false, false},
		{"context 3", with(map[string]any{"context": 3}), defaultSearchLimit, 3, true, bodiesNone, fitBounded, false, false},
		{"context 15", with(map[string]any{"context": 15}), defaultSearchLimit, 15, true, bodiesNone, fitBounded, false, false},
		{"context 40", with(map[string]any{"context": 40}), defaultSearchLimit, 15, true, bodiesWide, fitBounded, false, true},
		{"context 3 + include_bodies=true", with(map[string]any{"context": 3, "include_bodies": true}), defaultSearchLimit, 3, true, bodiesWideNoPick, fitBounded, false, true},
		{"context 40 + include_bodies=false", with(map[string]any{"context": 40, "include_bodies": false}), defaultSearchLimit, 15, true, bodiesNone, fitBounded, false, false},
		{"max_results 100", with(map[string]any{"limit": 100}), 100, 2, false, bodiesWide, fitBounded, false, true},
		{"max_results 5", with(map[string]any{"limit": 5}), 5, 2, false, bodiesWide, fitNone, false, true},
		{"exhaustive", with(map[string]any{"exhaustive": true}), defaultSearchLimit, 0, false, bodiesNone, fitNone, false, false},
		{"files_only", with(map[string]any{"files_only": true}), defaultSearchLimit, 0, false, bodiesNone, fitNone, false, false},
		{"rollup_only", with(map[string]any{"rollup_only": true}), defaultSearchLimit, 0, false, bodiesNone, fitNone, false, false},
		{"files_only + include_bodies=true", with(map[string]any{"files_only": true, "include_bodies": true}), defaultSearchLimit, 0, false, bodiesNone, fitNone, false, false},
	}
	for _, r := range rows {
		got := searchShapeFor(r.args, r.limit)
		if got.context != r.ctx || got.contextAsked != r.asked || got.bodies != r.bodies ||
			got.fit != r.fit || got.budget != r.budget || got.dedupeContext != r.dedupe {
			t.Errorf("%s: got context=%d asked=%v bodies=%v fit=%v budget=%v dedupe=%v; want %d %v %v %v %v %v",
				r.name, got.context, got.contextAsked, got.bodies, got.fit, got.budget, got.dedupeContext,
				r.ctx, r.asked, r.bodies, r.fit, r.budget, r.dedupe)
		}
	}
	if note := searchShapeFor(with(map[string]any{"context": 40}), defaultSearchLimit).clampNote(); !strings.Contains(note, "enclosing functions are delivered") {
		t.Errorf("context 40 clamp note should say the functions are delivered: %q", note)
	}
	if note := searchShapeFor(with(map[string]any{"context": 40, "include_bodies": false}), defaultSearchLimit).clampNote(); !strings.Contains(note, "use prism_read") {
		t.Errorf("context 40 without bodies should point at read: %q", note)
	}
	if note := searchShapeFor(with(map[string]any{"context": 15}), defaultSearchLimit).clampNote(); note != "" {
		t.Errorf("context at the cap is not clamped: %q", note)
	}
}

// Each shape row, end to end on one fixture: the rendered answer carries the
// bodies the table promises and no others.
func TestSearchShapeRowsEndToEnd(t *testing.T) {
	// A fresh server per row: the session cache would otherwise turn a later
	// row's body into an "already delivered" pointer.
	fixture := func() *Server {
		return compactFixture(t, map[string]string{
			"Deser.java": "package p;\n\nclass Deser {\n" +
				"    Object fromArray(Object p) {\n        int act = findCoercion(p);\n        if (act > 0) {\n            return null;\n        }\n        return p;\n    }\n\n" +
				"    String parseString(Object p) {\n        if (p == null) {\n            return null;\n        }\n        return p.toString();\n    }\n\n" +
				"    int findCoercion(Object p) {\n        return 1;\n    }\n}\n",
		})
	}
	// Source lines that only a body section prints (never a window or a
	// match line): the declaration's return and the caller's branch.
	const defSrc, callerSrc = "return 1;", "if (act > 0) {"
	cases := []struct {
		name     string
		args     map[string]any
		wantSrc  []string
		noBodies bool
	}{
		{"default: one body, the definition", map[string]any{"terms": []any{"findCoercion"}}, []string{defSrc}, false},
		{"include_bodies=false", map[string]any{"terms": []any{"findCoercion"}, "include_bodies": false}, nil, true},
		{"include_bodies=true: wide", map[string]any{"terms": []any{"findCoercion"}, "include_bodies": true}, []string{defSrc, callerSrc}, false},
		{"context within cap: windows only", map[string]any{"terms": []any{"findCoercion"}, "context": 3}, nil, true},
		{"context past cap: the functions", map[string]any{"terms": []any{"findCoercion"}, "context": 40}, []string{defSrc, callerSrc}, false},
		{"max_results: wide", map[string]any{"terms": []any{"findCoercion"}, "max_results": 100}, []string{defSrc, callerSrc}, false},
		{"files_only", map[string]any{"terms": []any{"findCoercion"}, "files_only": true}, nil, true},
		{"exhaustive", map[string]any{"terms": []any{"findCoercion"}, "exhaustive": true}, nil, true},
	}
	for _, c := range cases {
		got := callCompact(t, fixture(), "search", c.args)
		if c.noBodies && strings.Contains(got, "// Exact source") {
			t.Errorf("%s: unexpected body:\n%s", c.name, got)
		}
		for _, want := range c.wantSrc {
			n := strings.Count(got, want)
			if n == 0 {
				t.Errorf("%s: missing %q:\n%s", c.name, want, got)
			} else if n > 1 {
				t.Errorf("%s: %q printed %d times; no source line twice in one answer:\n%s", c.name, want, n, got)
			}
		}
	}
	// The default shape delivers exactly one body section.
	got := callCompact(t, fixture(), "search", map[string]any{"terms": []any{"findCoercion"}})
	if n := strings.Count(got, "**`Deser.java`**"); n != 1 {
		t.Errorf("default shape should deliver one body section, got %d:\n%s", n, got)
	}
}
