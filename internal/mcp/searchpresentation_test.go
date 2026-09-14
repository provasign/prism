package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/ranking"
)

func TestSearchBatchUsesBoundedIdentifierFallback(t *testing.T) {
	h := newTextSearchHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "help.go"), []byte("package p\nfunc make_default_short_help() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []string{"make_short_help", "Alpha"}, "scope": "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	results := m["results"].([]map[string]any)
	if !searchResultEmpty(results[0]) {
		t.Fatal("exact missed term was silently replaced by fallback")
	}
	text, ok := renderSearchAsText(m)
	if !ok || !strings.Contains(text, "make_short_help → short_help") ||
		!strings.Contains(text, "make_default_short_help") {
		t.Fatalf("batch did not deliver a labeled suffix locator: %s", text)
	}
}

func TestSearchBatchSharesPresentationBudget(t *testing.T) {
	h := newTextSearchHandler(t)
	var terms []string
	for i := 0; i < 4; i++ {
		term := fmt.Sprintf("Needle%d", i)
		terms = append(terms, term)
		if err := os.WriteFile(filepath.Join(h.Root, fmt.Sprintf("file%d.go", i)),
			[]byte("package p\n"+strings.Repeat(term+" "+strings.Repeat("payload ", 50)+"\n", 30)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.Invoke("prism_search", map[string]any{"query": terms, "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	text, ok := renderSearchAsText(m)
	if !ok || ranking.EstimateTokens(text) > 4000 {
		t.Fatalf("shared budget failed (%d estimated tokens)", ranking.EstimateTokens(text))
	}
	for _, result := range m["results"].([]map[string]any) {
		if result["totalHits"] != 30 || result["countComplete"] != true {
			t.Fatalf("search counts lost under display budget: %#v", result)
		}
		if searchResultEmpty(result) {
			t.Fatalf("a term lost every displayed hit: %#v", result)
		}
	}
	if !strings.Contains(text, "displayed ") {
		t.Fatalf("sampled delivery has no explicit disclosure:\n%s", text)
	}
}

func TestSearchSymbolOnlyBatchSharesPresentationBudget(t *testing.T) {
	var results []map[string]any
	for term := 0; term < 8; term++ {
		var symbols []map[string]any
		for i := 0; i < 25; i++ {
			symbols = append(symbols, map[string]any{
				"name": fmt.Sprintf("Needle%dMethod%d", term, i), "kind": "method",
				"filePath":  fmt.Sprintf("src/very/long/path/to/module%d.go", term),
				"span":      map[string]any{"start": i + 1, "end": i + 3},
				"signature": "func " + strings.Repeat("LongGenericParameter", 8),
			})
		}
		results = append(results, map[string]any{"query": fmt.Sprintf("Needle%d", term), "symbols": symbols})
	}
	out := map[string]any{"results": results}
	boundSymbolPresentation(out)
	text, ok := renderSearchAsText(out)
	if !ok || ranking.EstimateTokens(text) > 4000 {
		t.Fatalf("symbol display exceeded shared budget (%d tokens)", ranking.EstimateTokens(text))
	}
	for _, result := range results {
		if len(anySlice(result["symbols"])) == 0 || result["symbolsTruncated"] != true ||
			!strings.Contains(stringArg(result, "warning", ""), "Displayed") {
			t.Fatalf("symbol sample not diversified or disclosed: %#v", result)
		}
	}
}
