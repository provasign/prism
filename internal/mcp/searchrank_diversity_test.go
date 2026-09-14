package mcp

import (
	"fmt"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestRankSearchSymbolsKeepsExactNamesAndDiversifiesPathMatches(t *testing.T) {
	symbols := []grove.SymbolRecord{{ID: "exact", Name: "ranking", FilePath: "other.go"}}
	for i := 0; i < 20; i++ {
		symbols = append(symbols, grove.SymbolRecord{
			ID: fmt.Sprintf("a%d", i), Name: fmt.Sprintf("Thing%d", i), FilePath: "ranking/a.go",
		})
	}
	symbols = append(symbols, grove.SymbolRecord{ID: "b", Name: "Other", FilePath: "ranking/b.go"})
	got := rankSearchSymbols(symbols, "ranking")
	if got[0].symbol.ID != "exact" || got[0].matchKind != "name-exact" {
		t.Fatalf("exact name lost first place: %+v", got[0])
	}
	if got[2].symbol.ID != "b" {
		t.Fatalf("second matching file was buried after repeated hits: first three = %s, %s, %s",
			got[0].symbol.ID, got[1].symbol.ID, got[2].symbol.ID)
	}
}
