package mcp

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// Symbols at one location come back in one order whatever grove returned.
func TestRankSearchSymbolsSameSpotDeterministic(t *testing.T) {
	m := grove.SymbolRecord{Name: "unwrapped", QualifiedName: "R.unwrapped", Kind: "method", FilePath: "R.java", Span: grove.SpanInfo{Start: 46, End: 46}}
	f := m
	f.Kind = "field"
	a := rankSearchSymbols([]grove.SymbolRecord{m, f}, "unwrapped")
	b := rankSearchSymbols([]grove.SymbolRecord{f, m}, "unwrapped")
	if a[0].symbol.Kind != "field" || b[0].symbol.Kind != "field" {
		t.Fatalf("order depends on input: %s/%s vs %s/%s", a[0].symbol.Kind, a[1].symbol.Kind, b[0].symbol.Kind, b[1].symbol.Kind)
	}
}
