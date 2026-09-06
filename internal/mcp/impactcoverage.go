package mcp

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// impactCoverage returns the completeness to report and, when the engine's
// label overstates what it can prove, a note saying what is missing.
//
// Until grove v0.43.2 Go interface dispatch was not modeled at all, and
// every Go closure was downgraded from "closed" to "partial". grove v0.43.2
// resolves interface contracts through method sets, across packages
// (`ReasonMethodSet` native edges), so that blanket downgrade is stale —
// and it invited the exact re-verification loop measured on Codex/Django
// (a `closed` result stapled to "verify before relying"). What v0.43.2 still
// skips, by design: interfaces with type parameters. Those, and only those,
// keep the downgrade. The inventory is never altered either way.
func impactCoverage(r *grove.ChangeImpactResult) (string, string) {
	if r == nil {
		return "", ""
	}
	if r.Completeness != "closed" {
		return r.Completeness, ""
	}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Supers, r.Family, r.DeclaringTypes} {
		for _, sym := range group {
			if isGo(sym) && strings.EqualFold(sym.Kind, "interface") && isGenericGoInterface(sym) {
				return "partial", "a generic Go interface is in this contract; the engine does not " +
					"model dispatch through interfaces with type parameters, so implementations " +
					"and callers via that interface may be missing. Use targeted text search " +
					"for those before claiming completeness."
			}
		}
	}
	return r.Completeness, ""
}

func isGo(sym grove.SymbolRecord) bool {
	return strings.EqualFold(sym.Language, "go") || strings.EqualFold(filepath.Ext(sym.FilePath), ".go")
}

// goGenericDecl matches `type Name[T any] interface` in a Go interface
// signature. The indexer does not always populate TypeParameters for Go
// interfaces (measured on a `type Store[T any] interface{...}` fixture:
// the field was empty while the signature carried the parameter list), so
// the signature is the authoritative evidence.
var goGenericDecl = regexp.MustCompile(`^\s*type\s+[A-Za-z_][A-Za-z0-9_]*\s*\[`)

func isGenericGoInterface(sym grove.SymbolRecord) bool {
	return len(sym.TypeParameters) > 0 || goGenericDecl.MatchString(sym.Signature)
}
