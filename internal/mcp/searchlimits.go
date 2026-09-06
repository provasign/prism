package mcp

import (
	"context"
	"fmt"

	"github.com/provasign/prism/internal/grove"
)

// Fetch prefixes until the scoped result is known or the work bound is reached.
// A work bound says nothing about how many additional in-scope matches exist.
func scopedSymbolSearch(ctx context.Context, search func(context.Context, string, int) ([]grove.SymbolRecord, error),
	query string, sc searchScope, cap, hardMax int) ([]grove.SymbolRecord, bool, error) {
	fetch := minInt(cap+1, hardMax)
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		raw, err := search(ctx, query, fetch)
		if err != nil {
			return nil, false, err
		}
		exhausted := len(raw) < fetch
		syms := filterSymbolsByScope(filterGeneratedPrismContext(raw), sc)
		if len(syms) > cap || exhausted || fetch == hardMax {
			return syms, exhausted, nil
		}
		if fetch > hardMax/4 {
			fetch = hardMax
		} else {
			fetch *= 4
		}
	}
}

func symbolSearchWarning(returned, cap int, exhaustive, sourceExhausted, moreKnown bool) string {
	if !moreKnown && !sourceExhausted {
		return fmt.Sprintf("INCOMPLETE symbol scan: returned %d in-scope matches; the source scan limit was reached. "+
			"The remaining count is unknown. Narrow with path=/glob= or a longer name.", returned)
	}
	if exhaustive {
		return exhaustiveCapWarning(cap)
	}
	return fmt.Sprintf("showing %d symbol matches - a SAMPLE, more exist. "+
		"Use exhaustive=true for completeness, or narrow with path=/glob=.", returned)
}

func searchResultPartial(m map[string]any) bool {
	if len(anySlice(m["omittedTerms"])) > 0 {
		return true
	}
	for _, key := range []string{"truncated", "symbolsTruncated", "timedOut"} {
		if value, _ := m[key].(bool); value {
			return true
		}
	}
	if len(anySlice(m["rejectedPaths"])) > 0 || len(anySlice(m["failedTerms"])) > 0 {
		return true
	}
	if results, ok := m["results"].([]map[string]any); ok {
		for _, result := range results {
			if searchResultPartial(result) {
				return true
			}
		}
	}
	return false
}
