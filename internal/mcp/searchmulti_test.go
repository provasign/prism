package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// --- path scoping ------------------------------------------------------
//
// The adoption lever. In the 2026-08-15 A/B cells, 12 of 20 grep invocations
// named a path, and the cells where the agent DECLINED prism opened with
// `grep -n alias octodns/manager.py` — a scoped search prism could not
// express. Whole-tree-only is why an agent that knows its file uses grep.

func TestToolSearch_PathScopeRestrictsResults(t *testing.T) {
	h := newTextSearchHandler(t)
	all, err := h.Invoke("prism_search", map[string]any{"query": "Alpha", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(asHits(all)); n < 2 {
		t.Fatalf("fixture should match Alpha in both files, got %d", n)
	}
	// Scoped to the file that only MENTIONS Alpha, not the one defining it.
	one, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "path": "beta.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range hitFiles(one) {
		if f != "beta.go" {
			t.Errorf("path=beta.go returned a hit in %q — the scope was not applied", f)
		}
	}
	if len(hitFiles(one)) == 0 {
		t.Error("scoping to a single file returned nothing; rg/grep omit the filename for a " +
			"single file operand and the path:line:text parse drops every hit without --with-filename")
	}
}

func TestToolSearch_PathOutsideRootIsRefusedNotWidened(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "path": "/etc"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	// The dangerous failure is not an error, it is a WIDENING: answering a
	// question about /etc with hits from the whole project reads as success.
	if n := len(asHits(out)); n != 0 {
		t.Errorf("an out-of-root scope returned %d hits; it must return none", n)
	}
	if m["rejectedPaths"] == nil || m["warning"] == nil {
		t.Errorf("a dropped scope must be reported, got %v", m)
	}
}

func TestToolSearch_FilesOnlyDropsLines(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Alpha", "scope": "text", "files_only": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, ok := m["textHits"]; ok {
		t.Error("files_only must not return per-line hits")
	}
	files, _ := m["files"].([]string)
	if len(files) == 0 {
		t.Fatalf("files_only returned no files: %v", m)
	}
	for _, f := range files {
		if f == "" {
			t.Error("empty file path in files_only result")
		}
	}
}

func asHits(out any) []any {
	m, _ := out.(map[string]any)
	if m == nil {
		return nil
	}
	var n []any
	for _, g := range toSlice(m["textHits"]) {
		gm, _ := g.(map[string]any)
		n = append(n, toSlice(gm["hits"])...)
	}
	return n
}

func hitFiles(out any) []string {
	m, _ := out.(map[string]any)
	var fs []string
	for _, g := range toSlice(m["textHits"]) {
		if gm, ok := g.(map[string]any); ok {
			if f, _ := gm["file"].(string); f != "" {
				fs = append(fs, f)
			}
		}
	}
	return fs
}

func toSlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	}
	return nil
}

// Every advertised tool must be exempt from client-side schema deferral.
//
// Claude Code defers MCP schemas behind a ToolSearch hop. Frontier models
// make the hop; cheap tiers do not — measured on SWE-bench-Live (haiku, same
// task): 0 prism calls deferred, 5 loaded, and with the flag haiku opens with
// prism_query at 30 turns/$0.30 against 45/$0.53. v0.44.0 shipped this and
// v0.52.0 reverted it wholesale with the rest of the arc, not on its own
// evidence. The steering block no longer carries a ToolSearch paragraph, so
// if this regresses the tools become both invisible AND unexplained.
func TestToolSchemas_AllAlwaysLoad(t *testing.T) {
	for _, tool := range ToolSchemas() {
		meta, ok := tool["_meta"].(map[string]any)
		if !ok {
			t.Errorf("%v has no _meta — client will defer its schema", tool["name"])
			continue
		}
		if meta["anthropic/alwaysLoad"] != true {
			t.Errorf("%v: anthropic/alwaysLoad = %v, want true", tool["name"], meta["anthropic/alwaysLoad"])
		}
	}
}

// Truncation must carry a denominator, and completeness must be askable.
//
// Before this, a capped search returned `truncated: true` and nothing else:
// an agent could not tell 22-of-25 from 22-of-990 (measured on this repo:
// "func " returned 22 hits against 1,311 real matches). A capped answer to
// "rewrite every call site" LOOKS complete, which is the expensive failure
// SWE-Explore measures -- missing evidence costs far more than noise.
func TestToolSearch_TruncationCarriesADenominator(t *testing.T) {
	h := newTextSearchHandler(t)
	// Fixture files both contain "package", so limit=1 forces truncation.
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "package", "scope": "text", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] != true {
		t.Fatalf("limit=1 over two matching files should truncate: %v", m)
	}
	total, _ := m["totalHits"].(int)
	if total < 2 {
		t.Errorf("totalHits = %v, want >= 2 — truncation without a denominator "+
			"reads to the agent as a complete answer", m["totalHits"])
	}
	if w, _ := m["warning"].(string); w == "" {
		t.Error("a truncated result must warn that it is a sample")
	}
}

func TestToolSearch_ExhaustiveIsNotCapped(t *testing.T) {
	h := newTextSearchHandler(t)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "package", "scope": "text", "limit": 1, "exhaustive": true})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	// exhaustive is the agent DECLARING it needs completeness; an explicit
	// limit must not silently re-impose the cap.
	if m["truncated"] == true {
		t.Errorf("exhaustive=true still truncated: %v", m)
	}
}

// --- ranged reads ---------------------------------------------------------
//
// prism_read was whole-file only, and 50.6% of the 374 file reads measured
// across real agent runs are line-ranged (sed -n A,Bp 25.7%, native
// Read(offset,limit) 23.8%). That gap is why 87% of the reads prism never
// saw were ranged, and why its session ledger fired 0 times in 45 calls.

func TestToolRead_Range(t *testing.T) {
	h := newTextSearchHandler(t)
	body := ""
	for i := 1; i <= 20; i++ {
		body += "line" + string(rune('0'+i%10)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(h.Root, "r.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_read", map[string]any{"file": "r.txt", "offset": 5, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["startLine"] != 5 || m["endLine"] != 7 {
		t.Errorf("window = %v-%v, want 5-7", m["startLine"], m["endLine"])
	}
	if m["totalLines"] != 20 {
		t.Errorf("totalLines = %v, want 20 — a window without a denominator reads as the whole file",
			m["totalLines"])
	}
	c, _ := m["content"].(string)
	if strings.Count(c, "\n") != 3 {
		t.Errorf("want 3 lines, got %q", c)
	}
	if !strings.Contains(c, "5\t") {
		t.Errorf("lines must be numbered so the agent can cite them: %q", c)
	}
	if m["note"] == nil {
		t.Error("a partial read must say it is partial")
	}
}

func TestToolRead_RangePastEOFClampsNotErrors(t *testing.T) {
	h := newTextSearchHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "s.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A tool that errors is a tool agents route around; clamp and explain.
	out, err := h.Invoke("prism_read", map[string]any{"file": "s.txt", "offset": 999})
	if err != nil {
		t.Fatalf("out-of-range offset must not error: %v", err)
	}
	m := out.(map[string]any)
	if m["totalLines"] != 2 || m["warning"] == nil {
		t.Errorf("want totalLines and a warning, got %v", m)
	}
}

func TestToolRead_RangeNeedsNoIndex(t *testing.T) {
	// A line window is a file operation, not a graph one. It must work before
	// the index is built or when the engine is unavailable -- otherwise the
	// agent falls back to `sed -n A,Bp` exactly when prism is least able to
	// see what it read. (The whole-file path DOES need Grove for symbols;
	// TestToolRead_OK covers that.)
	h := newTextSearchHandler(t) // nil Grove
	out, err := h.Invoke("prism_read", map[string]any{"file": "alpha.go", "offset": 1, "limit": 2})
	if err != nil {
		t.Fatalf("ranged read must not require the index: %v", err)
	}
	if out.(map[string]any)["delivery"] != "range" {
		t.Errorf("want a range delivery, got %v", out)
	}
}
