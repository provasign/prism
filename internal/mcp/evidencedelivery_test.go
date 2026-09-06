package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func evidenceHandler(t *testing.T, files map[string]string) *Handler {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gc.Shutdown() })
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestImpactEvidencePreservesSitesAndSource(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"store.go":       "package p\ntype Store struct{}\nfunc (s *Store) Get() int { return 1 }\nfunc Use(s *Store) int {\n return s.Get()\n}\n",
		"tests/calls.go": "package p\ntype Other struct{}\nfunc (s *Other) Get() int { return 2 }\nfunc TestOther(s *Other) { s.Get() }\n",
		"store_test.go":  "package p\nfunc TestUse() { s := &Store{}; s.Get() }\n",
	})
	before, err := h.Grove.ChangeImpactScoped(t.Context(), "Store.Get", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.Invoke("prism_change_impact", map[string]any{"query": "Store.Get"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	for key, sites := range map[string][]grove.SymbolRecord{
		"declarations": before.Declarations, "supers": before.Supers, "family": before.Family, "callers": before.Callers,
	} {
		entries := out[key].([]map[string]any)
		if len(entries) != len(sites) {
			t.Fatalf("%s: lost sites: %d != %d", key, len(entries), len(sites))
		}
		for i, site := range sites {
			if entries[i]["filePath"] != site.FilePath || entries[i]["line"] != site.Span.Start || entries[i]["qualifiedName"] != site.QualifiedName {
				t.Fatalf("%s: changed site identity: %v", key, entries[i])
			}
		}
	}
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatalf("impact unexpectedly fell back to JSON: %v", out)
	}
	for _, want := range []string{"Get()", "5: return s.Get()", "[test]", "not independent receiver-resolution proof"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	other, err := h.Invoke("prism_change_impact", map[string]any{"query": "Other.Get"})
	if err != nil {
		t.Fatal(err)
	}
	text, ok = renderChangeImpactAsText(other.(map[string]any))
	if !ok || !strings.Contains(text, "[test]") {
		t.Fatalf("root tests/ path must be labeled, not filtered: %t %s", ok, text)
	}
}

func TestImpactEvidenceBoundsAndUncertainty(t *testing.T) {
	caller := grove.SymbolRecord{Span: grove.SpanInfo{Start: 10}, RawText: "def use():\n a.get()\n b.get()\n c.get()\n wrong()",
		CallSites: []grove.CallSite{{Callee: "c.get", Line: 13}, {Callee: "a.get", Line: 11},
			{Callee: "b.get", Line: 12}, {Callee: "a.get", Line: 11}, {Callee: "get", Line: 900}, {Callee: "forget", Line: 14}}}
	entry := map[string]any{}
	budget := impactEvidenceMaxBytes
	addImpactCallEvidence(entry, caller, "get", &budget)
	evidence := entry["evidence"].([]map[string]any)
	if len(evidence) != 2 || evidence[0]["line"] != 11 || evidence[1]["line"] != 12 {
		t.Fatalf("want two ordered deduplicated lines: %v", evidence)
	}
	if note := entry["evidenceNote"].(string); !strings.Contains(note, "unavailable") || !strings.Contains(note, "1 matching line(s) omitted") {
		t.Fatal(note)
	}
	entry, budget = map[string]any{}, 0
	addImpactCallEvidence(entry, caller, "get", &budget)
	if entry["evidence"] != nil || !strings.Contains(entry["evidenceNote"].(string), "3 matching line(s) omitted") {
		t.Fatalf("zero budget must explain every omitted line: %v", entry)
	}
	entry = map[string]any{}
	addImpactCallEvidence(entry, caller, "other", &budget)
	if !strings.Contains(entry["evidenceNote"].(string), "unavailable") {
		t.Fatal(entry)
	}
	if line := compactImpactLine(strings.Repeat("x", 300)); !strings.Contains(line, "truncated") || len(line) >= 300 {
		t.Fatal(line)
	}
}

func TestImpactEvidenceRendererPreservesWarnings(t *testing.T) {
	out := map[string]any{"query": "X.get", "totalSites": 0, "completeness": "closed", "hasHeuristicRefs": true,
		"staleWarning": "stale index", "ambiguityNote": "ambiguous query", "scopeNote": "scope gap"}
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatal("known warnings should render compactly")
	}
	for _, want := range []string{"not receiver certainty", "stale index", "ambiguous query", "scope gap"} {
		if !strings.Contains(text, want) {
			t.Error("lost warning:", want)
		}
	}
	out["callers"] = []map[string]any{{"name": "caller", "newProvenance": "unrecognized"}}
	if _, ok := renderChangeImpactAsText(out); ok {
		t.Fatal("unrecognized site evidence must survive via JSON fallback")
	}
}

func TestLookupBatchMatchesSinglesAndKeepsMisses(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"store.go": "package p\ntype Store struct{}\nfunc (s *Store) Get() int { return 1 }\nfunc (s *Store) Set() {}\n"})
	names := []any{"Store.Get", "Store.Set", "NotHere"}
	args := map[string]any{"name": names, "file": "store.go"}
	result, err := h.Invoke("prism_lookup", args)
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	entries := out["results"].([]map[string]any)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	for i, name := range names {
		single, err := h.Invoke("prism_lookup", map[string]any{"name": name, "file": "store.go"})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(single)
		got, _ := json.Marshal(entries[i]["result"])
		if string(got) != string(want) {
			t.Fatalf("batch changed single result for %s", name)
		}
	}
	text, ok := renderLookupAsText(out)
	if !ok || strings.Count(text, "return 1") != 1 || !strings.Contains(text, "no symbol named") {
		t.Fatalf("bad batch rendering: %t %s", ok, text)
	}
	projected, err := h.Invoke("prism_lookup", map[string]any{"name": []string{"Store.Get", "Store.Set"}, "fields": []any{"signature"}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok = renderLookupAsText(projected.(map[string]any))
	if !ok || strings.Contains(text, "return 1") || !strings.Contains(text, "Get()") {
		t.Fatalf("batch must honor fields: %t %s", ok, text)
	}
}

func TestLookupBatchRejectsInvalidInputBeforeQuerying(t *testing.T) {
	for _, raw := range []any{[]any{}, []any{"Get", 42}, []string{""}, make([]string, 11), 42} {
		if _, _, err := lookupBatchNames(raw); err == nil {
			t.Errorf("accepted invalid batch: %#v", raw)
		}
	}
	if _, batch, err := lookupBatchNames("Get"); batch || err != nil {
		t.Fatal("single lookup compatibility lost")
	}
	h := &Handler{}
	if _, err := h.toolLookup(context.Background(), map[string]any{"name": []any{"Get", false}}); err == nil {
		t.Fatal("invalid batch should fail before touching Grove")
	}
}

func TestLookupBatchBudgetNeverTruncatesBodiesSilently(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"large.go": "package p\nfunc Large() string { return `" + strings.Repeat("x", lookupBatchMaxBytes) + "` }\nfunc Small() int { return 7 }\n"})
	result, err := h.Invoke("prism_lookup", map[string]any{"name": []any{"Large", "Small"}})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if omitted := out["omitted"].([]string); len(omitted) != 1 || omitted[0] != "Large" {
		t.Fatal(omitted)
	}
	text, ok := renderLookupAsText(out)
	if !ok || !strings.Contains(text, "NOT DELIVERED: [Large]") || !strings.Contains(text, "return 7") {
		t.Fatalf("bad bounded batch: %t %s", ok, text)
	}
}

func TestLookupBatchRenderingPreservesFailuresAndUnknownFields(t *testing.T) {
	if _, ok := renderLookupAsText(map[string]any{"results": "malformed"}); ok {
		t.Fatal("malformed batch must not become a successful empty response")
	}
	out := map[string]any{"results": []map[string]any{{"name": "Get", "error": "index unavailable"}}}
	text, ok := renderLookupAsText(out)
	if !ok || !strings.Contains(text, "ERROR: index unavailable") {
		t.Fatal(text)
	}
	out["results"] = []map[string]any{{"name": "Get", "result": map[string]any{"newField": true}}}
	if _, ok := renderLookupAsText(out); ok {
		t.Fatal("unknown nested result must fall back intact")
	}
}
