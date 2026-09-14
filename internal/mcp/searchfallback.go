package mcp

import (
	"context"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/ranking"
)

var fallbackIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// searchFallbackTerms preserves the existing phrase fallback and adds one
// identifier suffix for names that plausibly gained a qualifier in the code.
// A fallback is only used after the exact term has failed.
func searchFallbackTerms(q string) (selected, omitted []string) {
	selected, omitted = tokenFallbackTerms(q)
	longest := ""
	for _, word := range fallbackIdentifier.FindAllString(q, -1) {
		if len(word) > len(longest) {
			longest = word
		}
	}
	if cut := strings.IndexByte(longest, '_'); cut > 0 && cut+4 < len(longest) {
		suffix := longest[cut+1:]
		duplicate := false
		for _, term := range selected {
			if strings.EqualFold(term, suffix) {
				duplicate = true
			}
		}
		if !duplicate {
			selected = append([]string{suffix}, selected...)
		}
	}
	if len(selected) > 3 {
		omitted = append(omitted, selected[3:]...)
		selected = selected[:3]
	}
	return selected, omitted
}

// appendBatchedSearchFallback runs at most two tiny, locator-only searches
// for exact terms that missed. It does not change the original completion
// status or widen path/glob filters. Its marginal output is capped even when
// the successful terms have already produced a large answer.
func (h *Handler) appendBatchedSearchFallback(ctx context.Context, out map[string]any, results []map[string]any, scope string, sc searchScope, limit int) {
	baseText, ok := renderSearchAsText(out)
	if !ok {
		return
	}
	baseTokens := ranking.EstimateTokens(baseText)
	var empty []map[string]any
	for _, result := range results {
		if searchResultEmpty(result) && !searchResultPartial(result) {
			empty = append(empty, result)
		}
	}
	if len(empty) == 0 {
		return
	}
	fallbackScope := sc
	fallbackScope.context = 0
	fallbackScope.adaptive = false
	var fallback []map[string]any
	probes := 0
	for _, missed := range empty {
		original, _ := missed["query"].(string)
		candidates, _ := searchFallbackTerms(original)
		for _, candidate := range candidates {
			if len(fallback) >= 2 || probes >= 4 {
				break
			}
			probes++
			found, err := h.searchOne(ctx, candidate, scope, minInt(limit, 2), false, fallbackScope)
			if err != nil || searchResultEmpty(found) {
				continue
			}
			found["query"] = candidate
			found["fallbackFrom"] = original
			fallback = append(fallback, found)
			if len(empty) > 1 {
				break // spread the two slots across failed terms
			}
		}
		if len(fallback) >= 2 || probes >= 4 {
			break
		}
	}
	for len(fallback) > 0 {
		out["fallbackResults"] = fallback
		text, ok := renderSearchAsText(out)
		if ok && ranking.EstimateTokens(text)-baseTokens <= 450 {
			return
		}
		fallback = fallback[:len(fallback)-1]
	}
	delete(out, "fallbackResults")
}
