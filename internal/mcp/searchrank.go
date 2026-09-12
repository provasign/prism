package mcp

import (
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

type rankedSearchSymbol struct {
	symbol    grove.SymbolRecord
	matchKind string
	tier      int
}

// rankSearchSymbols applies Prism's declared match tiers before the delivery
// cap. Grove's order remains the stable tie-breaker within each tier.
func rankSearchSymbols(symbols []grove.SymbolRecord, query string) []rankedSearchSymbol {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]rankedSearchSymbol, len(symbols))
	for i, sym := range symbols {
		kind, tier := symbolMatchTier(sym, q)
		if sym.Kind == "document" {
			tier = 2
		} else if isTestDouble(sym.FilePath) && kind != "name-exact" {
			tier = 1
		}
		out[i] = rankedSearchSymbol{symbol: sym, matchKind: kind, tier: tier}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].tier > out[j].tier })
	return out
}

func symbolMatchTier(sym grove.SymbolRecord, q string) (string, int) {
	name := strings.ToLower(sym.Name)
	qualified := strings.ToLower(sym.QualifiedName)
	switch {
	case q == "":
		return "unclassified", 0
	case name == q:
		return "name-exact", 9
	case strings.HasPrefix(name, q):
		return "name-prefix", 8
	case strings.Contains(name, q):
		return "name-substring", 7
	case strings.Contains(qualified, q):
		return "qualified", 6
	case strings.Contains(strings.ToLower(sym.FilePath), q):
		return "path", 5
	case strings.Contains(strings.ToLower(sym.Signature), q):
		return "signature", 4
	case strings.Contains(strings.ToLower(sym.Docstring), q):
		return "doc", 3
	case strings.Contains(strings.ToLower(sym.RawText), q):
		return "body", 2
	default:
		return "unclassified", 0
	}
}
