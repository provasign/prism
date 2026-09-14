package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

func TestSearchRootAndBothPassesRemainVisible(t *testing.T) {
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["root"] != h.Root || m["symbolsTruncated"] != true || m["truncated"] != true {
		t.Fatalf("root and both truncation flags must survive: %v", m)
	}
	txt, ok := renderSearchAsText(m)
	if !ok {
		t.Fatal("search fell back to JSON")
	}
	for i := 0; i < 3; i++ {
		got := h.once.apply(txt)
		for _, want := range []string{"// root: " + h.Root, "a SAMPLE", "Text matches are a SAMPLE"} {
			if !strings.Contains(got, want) {
				t.Fatalf("repeat %d lost %q: %s", i, want, got)
			}
		}
	}
}

func TestSearchBatchedEmptyResultsNameTheirRoot(t *testing.T) {
	h := symbolCapFixture(t, 1)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"definitelyMissingA", "definitelyMissingB"}, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	txt, ok := renderSearchAsText(out.(map[string]any))
	if !ok || strings.Count(txt, "// root: "+h.Root) != 1 {
		t.Fatalf("batch must state one actual root: %s", txt)
	}
}

func TestSearchBatchedPhraseFallbackPreservesExactResult(t *testing.T) {
	h := symbolCapFixture(t, 1)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"FooThing nonsense", "definitelyMissing"}, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	results := m["results"].([]map[string]any)
	if len(results) != 2 || !searchResultEmpty(results[0]) {
		t.Fatalf("the exact phrase must remain a reported miss: %v", results)
	}
	fallback := anySlice(m["fallbackResults"])
	if len(fallback) == 0 {
		t.Fatalf("batched phrase miss had no fallback: %v", m)
	}
	first := fallback[0].(map[string]any)
	if first["query"] != "FooThing" || first["fallbackFrom"] != "FooThing nonsense" || searchResultEmpty(first) {
		t.Fatalf("fallback did not identify the matching shorter term and its origin: %v", fallback)
	}
}

func TestSearchBatchedPhraseFallbackSurvivesLargeExistingAnswer(t *testing.T) {
	h := symbolCapFixture(t, 1)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"FooThing nonsense", "definitelyMissing"},
		"scope": "text", "include_bodies": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	delete(m, "fallbackResults")
	m["inlineBodies"] = strings.Repeat("// Existing exact source section with line-numbered content.\n", 260)
	base, ok := renderSearchAsText(m)
	if !ok || ranking.EstimateTokens(base) <= 3200 {
		t.Fatal("fixture must exceed the old total-answer gate")
	}
	h.appendBatchedSearchFallback(t.Context(), m, m["results"].([]map[string]any),
		"text", searchScope{}, defaultSearchLimit)
	fallback := anySlice(m["fallbackResults"])
	if len(fallback) == 0 || fallback[0].(map[string]any)["query"] != "FooThing" {
		t.Fatalf("large existing answer suppressed the bounded fallback: %v", fallback)
	}
	result, ok := renderSearchAsText(m)
	if !ok || ranking.EstimateTokens(result)-ranking.EstimateTokens(base) > 450 {
		t.Fatal("fallback exceeded its marginal output budget")
	}
}

func TestSearchRejectedScopeNeverClaimsAbsence(t *testing.T) {
	h := symbolCapFixture(t, 1)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "both", "path": "../elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	txt, ok := renderSearchAsText(m)
	if !ok || !searchResultPartial(m) || len(anySlice(m["rejectedPaths"])) == 0 {
		t.Fatalf("rejected scope was not retained: %v", m)
	}
	if strings.Contains(txt, "exact strings absent") || strings.Contains(txt, "full index checked") {
		t.Fatalf("invalid scope claimed absence: %s", txt)
	}
}

func TestSearchLargeLimitClampsAndInvalidScopeErrors(t *testing.T) {
	h := symbolCapFixture(t, 40)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "FooThing", "scope": "symbols", "limit": int(^uint(0) >> 1)})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if len(anySlice(m["symbols"])) != 40 || !strings.Contains(fmt.Sprint(m["note"]), "clamped") {
		t.Fatalf("large positive limit must clamp without overflowing: %v", m)
	}
	if _, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "typo"}); err == nil {
		t.Fatal("invalid scope was silently accepted")
	}
}

func TestScopedSymbolScanHonorsHardBoundAndUnknownCount(t *testing.T) {
	var requests []int
	fetch := func(_ context.Context, _ string, n int) ([]grove.SymbolRecord, error) {
		requests = append(requests, n)
		result := make([]grove.SymbolRecord, n)
		for i := range result {
			result[i].FilePath = "outside/file.go"
		}
		return result, nil
	}
	syms, exhausted, err := scopedSymbolSearch(context.Background(), fetch, "Foo",
		searchScope{paths: []string{"inside"}}, 2, 5)
	if err != nil || exhausted || len(syms) != 0 {
		t.Fatalf("expected incomplete zero-match prefix, got %v %v %v", syms, exhausted, err)
	}
	if fmt.Sprint(requests) != "[3 5]" {
		t.Fatalf("fetch exceeded hard bound or stopped prematurely: %v", requests)
	}
	warning := symbolSearchWarning(len(syms), 2, true, exhausted, false)
	if !strings.Contains(warning, "returned 0") || !strings.Contains(warning, "unknown") || strings.Contains(warning, "MORE than") {
		t.Fatalf("scan cap invented a result count: %s", warning)
	}
}

func TestScopedSymbolScanChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := scopedSymbolSearch(ctx, func(context.Context, string, int) ([]grove.SymbolRecord, error) {
		t.Fatal("canceled request reached backend")
		return nil, nil
	}, "Foo", searchScope{}, 2, 5)
	if err != context.Canceled {
		t.Fatalf("want cancellation, got %v", err)
	}
}

func TestSearchOmittedTermsAreNotDeclaredAbsent(t *testing.T) {
	h := symbolCapFixture(t, 1)
	queries := make([]string, searchTermCap+1)
	for i := range queries {
		queries[i] = fmt.Sprintf("notPresent%d", i)
	}
	queries[searchTermCap] = "FooThing"
	out, err := h.Invoke("prism_search", map[string]any{"query": queries, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	txt, ok := renderSearchAsText(m)
	if !ok || !searchResultPartial(m) || !strings.Contains(txt, "NOT searched: [FooThing]") {
		t.Fatalf("omitted matching term was not identified: %s", txt)
	}
	if strings.Contains(fmt.Sprint(m["note"]), "not timed out") {
		t.Fatalf("incomplete batch claimed an exhaustive negative: %s", txt)
	}
}
