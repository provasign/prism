package mcp

import (
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// Grove's closed hierarchy does not prove Go method-set or interface coverage.
// Keep its inventory intact, but never promote that engine label to a guarantee.
func impactCoverage(r *grove.ChangeImpactResult) (string, string) {
	if r == nil {
		return "", ""
	}
	if r.Completeness != "closed" {
		return r.Completeness, ""
	}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Supers, r.Family, r.DeclaringTypes} {
		for _, sym := range group {
			if strings.EqualFold(sym.Language, "go") || strings.EqualFold(filepath.Ext(sym.FilePath), ".go") {
				return "partial", "Go interface dispatch and embedded method sets are not fully modeled; contracts or callers may be missing. Use targeted text search before claiming completeness."
			}
		}
	}
	return r.Completeness, ""
}
