package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newTextSearchHandler returns a handler over a small real tree. The shared
// newTestHandler has a nil Grove, so only the scope="text" path (a pure rg
// pass over the root) is exercisable without standing up the engine — which
// is exactly the path batching changes.
func newTextSearchHandler(t *testing.T) *Handler {
	t.Helper()
	h := newTestHandler(t)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(h.Root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.go", "package p\n\nfunc Alpha() {}\n")
	write("beta.go", "package p\n\nfunc Beta() { Alpha() }\n")
	return h
}

func TestStringsArg_AcceptsStringAndArray(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{"single string", map[string]any{"query": "Alpha"}, []string{"Alpha"}},
		{"json array", map[string]any{"query": []any{"Alpha", "Beta"}}, []string{"Alpha", "Beta"}},
		{"go slice", map[string]any{"query": []string{"Alpha", "Beta"}}, []string{"Alpha", "Beta"}},
		{"trims and drops empties", map[string]any{"query": []any{" Alpha ", "", "   "}}, []string{"Alpha"}},
		{"drops duplicates", map[string]any{"query": []any{"A", "B", "A"}}, []string{"A", "B"}},
		{"ignores non-strings", map[string]any{"query": []any{"A", 7, nil}}, []string{"A"}},
		{"absent", map[string]any{}, nil},
		{"wrong type", map[string]any{"query": 7}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stringsArg(c.in, "query"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("stringsArg = %#v, want %#v", got, c.want)
			}
		})
	}
}

// A single term must keep the exact flat shape it had before batching landed:
// every existing agent, CLI formatter and test reads "symbols"/"textHits" at
// the top level, and silently nesting them would break all of them.
func TestToolSearch_SingleTermKeepsFlatShape(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", out)
	}
	if _, nested := m["results"]; nested {
		t.Error("single-term search returned the grouped shape; it must stay flat")
	}
	if _, ok := m["textHits"]; !ok {
		t.Error("single-term search lost its top-level textHits field")
	}
}

func TestToolSearch_MultiTermGroupsByTerm(t *testing.T) {
	h := newTextSearchHandler(t)
	terms := []any{"Alpha", "Beta"}
	out, err := h.Invoke("prism_search", map[string]any{"query": terms, "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m := out.(map[string]any)
	groups, ok := m["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results missing or wrong type: %T", m["results"])
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 result groups, got %d", len(groups))
	}
	// Order must follow the terms as given, and each group must name its term
	// — otherwise an agent cannot tell which hit answered which question.
	for i, want := range []string{"Alpha", "Beta"} {
		if got, _ := groups[i]["query"].(string); got != want {
			t.Errorf("group %d is for %q, want %q", i, got, want)
		}
		if _, ok := groups[i]["textHits"]; !ok {
			t.Errorf("group %q has no textHits field", want)
		}
	}
	if _, err := json.Marshal(out); err != nil {
		t.Errorf("result is not JSON-serialisable: %v", err)
	}
}

// Over the cap the extra terms are dropped — but never silently. A quiet
// truncation reads to the agent as "searched everything, found nothing".
func TestToolSearch_OverCapSaysSo(t *testing.T) {
	h := newTextSearchHandler(t)
	terms := make([]any, searchTermCap+3)
	for i := range terms {
		terms[i] = string(rune('a'+i)) + "Term"
	}
	out, err := h.Invoke("prism_search", map[string]any{"query": terms, "scope": "text"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m := out.(map[string]any)
	if got := len(m["results"].([]map[string]any)); got != searchTermCap {
		t.Errorf("searched %d terms, want the cap of %d", got, searchTermCap)
	}
	if note, _ := m["note"].(string); note == "" {
		t.Error("terms were dropped with no note — truncation must never be silent")
	}
}

func TestToolSearch_EmptyQueryStillErrors(t *testing.T) {
	h := newTestHandler(t)
	for _, bad := range []any{"", []any{}, []any{"  "}} {
		if _, err := h.Invoke("prism_search", map[string]any{"query": bad}); err == nil {
			t.Errorf("query=%#v was accepted; it must error", bad)
		}
	}
}
