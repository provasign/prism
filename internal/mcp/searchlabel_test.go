package mcp

import (
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestSearchTaskLabelPreservesResults(t *testing.T) {
	h := symbolCapFixture(t, 40)
	for _, args := range []map[string]any{
		{"query": "FooThing", "scope": "text", "limit": 1},
		{"query": "FooThing", "scope": "symbols", "exhaustive": true},
		{"query": []string{"FooThing", "missing"}, "scope": "both", "path": ".", "limit": 1},
		{"query": "missing", "scope": "text"},
		{"query": "FooThing", "scope": "both", "path": "../elsewhere"},
	} {
		want, err := h.Invoke("prism_search", args)
		if err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{"", "locate response writer methods", "search another repository instead"} {
			labeled := maps.Clone(args)
			labeled["task"] = label
			got, err := h.Invoke("prism_search", labeled)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("label %q changed retrieval, scope, or warnings: %v\ngot: %#v\nwant: %#v", label, err, got, want)
			}
		}
	}
}

func TestSearchTaskLabelDoesNotReplaceQueryOrAcceptInvalidTypes(t *testing.T) {
	h := &Handler{}
	for _, label := range []any{nil, 1, false, []string{"Foo"}, map[string]any{"query": "Foo"}} {
		_, err := h.Invoke("prism_search", map[string]any{"query": "Foo", "task": label})
		if err == nil || !strings.Contains(err.Error(), "task must be a string label") {
			t.Fatalf("invalid label should fail before retrieval: %v", err)
		}
	}
	if _, err := h.Invoke("prism_search", map[string]any{"task": "Foo"}); err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("task must not become the query: %v", err)
	}
}

func TestSearchTaskLabelKeepsParameterAndScopeErrors(t *testing.T) {
	h := &Handler{Root: t.TempDir()}
	for key, value := range map[string]any{
		"query_terms": []string{"Other"},
		"scope":       "typo",
		"dir":         t.TempDir(),
	} {
		args := map[string]any{"query": "Foo", "task": "locate response writer methods", key: value}
		_, err := h.Invoke("prism_search", args)
		if err == nil || strings.Contains(err.Error(), "internal error") {
			t.Fatalf("invalid %s must fail before retrieval: %v", key, err)
		}
	}
}

func TestSearchSchemaDescribesLabelAndCoverageLimits(t *testing.T) {
	props := toolSchema("prism_search")["properties"].(map[string]any)
	label := props["task"].(map[string]any)
	if label["type"] != "string" || !strings.Contains(label["description"].(string), "does not affect retrieval or scope") {
		t.Fatal("the label's semantics must be visible to MCP clients")
	}
	exhaustive := props["exhaustive"].(map[string]any)["description"].(string)
	if !strings.Contains(exhaustive, "2000") || strings.Contains(exhaustive, "uncapped") {
		t.Fatal("exhaustive must describe its actual symbol bound")
	}
}
