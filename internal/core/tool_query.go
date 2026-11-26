package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

// --- Tool implementations -----------------------------------------------

type queryResult struct {
	BudgetUsed int            `json:"budgetUsed"`
	Symbols    []rankedSymbol `json:"symbols"`
	// TextMatches are full-text hits outside any indexed symbol (comments,
	// configs, docs) — the grep half of the merged search.
	TextMatches []map[string]any `json:"textMatches,omitempty"`
	TextBackend string           `json:"textBackend,omitempty"`
	// Note explains an empty result so agents can tell "wrong root" or
	// "term typo" apart from "genuinely no matches" without guessing.
	Note string `json:"note,omitempty"`
	// StaleWarning: the pre-answer index refresh failed or left files
	// unindexed (attachStaleWarning).
	StaleWarning string `json:"staleWarning,omitempty"`
}

type rankedSymbol struct {
	Name          string         `json:"name"`
	QualifiedName string         `json:"qualifiedName"`
	FilePath      string         `json:"filePath"`
	Kind          string         `json:"kind"`
	Category      string         `json:"category"`
	Content       string         `json:"content"`
	Span          grove.SpanInfo `json:"span"`
}

func (h *Handler) toolQuery(ctx context.Context, args map[string]any) (any, error) {
	return h.toolQueryScoped(ctx, args, searchScope{})
}

func formatQueryTerms(terms []string) string {
	encoded, _ := json.Marshal(terms)
	return string(encoded)
}

func (h *Handler) toolQueryScoped(ctx context.Context, args map[string]any, scope searchScope) (any, error) {
	timing := os.Getenv("PRISM_TIMING") != ""
	tQuery := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing] %-22s %8.0fms\n", stage, float64(time.Since(tQuery).Milliseconds()))
		}
	}
	defer stamp("toolQuery total")

	// --- Agent-directed parameters ---
	for _, path := range scope.paths {
		if _, _, err := safePathWithinRoot(h.Root, path); err != nil {
			return nil, fmt.Errorf("query path %q: %w", path, err)
		}
	}

	// terms: agent-supplied grep-style search terms used to seed retrieval.
	// Prism searches for each term as a symbol name/substring and uses the
	// matches as seeds — same precision as the agent's own grep, plus graph expansion.
	var terms []string
	if raw, ok := args["terms"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, t := range v {
				if s, ok := t.(string); ok && s != "" {
					terms = append(terms, s)
				}
			}
		case []string:
			terms = v
		}
	}

	// include: controls which result categories are returned.
	// Accepted values: "graph" (code + callers/callees), "docs".
	// Default when omitted: ["graph"].
	includeSet := map[string]bool{}
	if raw, ok := args["include"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, t := range v {
				if s, ok := t.(string); ok {
					includeSet[s] = true
				}
			}
		case []string:
			for _, s := range v {
				includeSet[s] = true
			}
		}
	}
	if len(includeSet) == 0 {
		includeSet = map[string]bool{"graph": true}
	}

	// Note: graph_depth was removed from the schema — it was advertised for
	// months while no code path read it, so tuning it silently did nothing.
	// Expansion is a fixed one-hop typed call neighborhood (selectContext).
	stamp("pre-selectContext")
	sel, err := h.selectContext(ctx, selectParams{
		terms:           terms,
		includeSet:      includeSet,
		explicitProfile: stringArg(args, "profile", ""),
		limit:           intArg(args, "limit", 50),
		contextUsed:     int64(intArg(args, "context_used", 0)),
		model:           stringArg(args, "model", ""),
		budgetArg:       intArg(args, "budget", 0),
		paths:           scope.paths,
		glob:            scope.glob,
	})
	if err != nil {
		return nil, err
	}
	stamp("post-selectContext")

	// Delivery: "source" = verbatim line-numbered windows + anchor summary
	// (edit-ready); "symbols" = the compact per-symbol list.
	//
	// Source is the default, unconditionally. It used to depend on
	// task keyword inference — so "fix the timeout bug" returned editable windows
	// and "look at the timeout handling" returned a symbol list, from the same
	// seeds. Delivering context for an edit is what this tool is for; ask for
	// delivery="symbols" when you want the compact list.
	delivery := stringArg(args, "delivery", "")
	if delivery == "" && len(sel.picked) > 0 {
		delivery = "source"
	}
	if delivery == "source" {
		sourceBudget := sel.budget
		if sel.budget <= 256 && len(sel.deliverableTextHits(nil)) > 0 {
			// Tiny budgets must leave room for a matched line that the source
			// window cannot show. Otherwise the source preamble consumes the
			// whole allowance and the explicit text evidence disappears.
			sourceBudget = sel.budget / 3
		}
		out, sourceSections := h.deliverSource(ctx, strings.Join(terms, ", "), sel, intArg(args, "max_files", 0), sourceBudget)
		if tm := h.renderTextMatches(ctx, sel.deliverableTextHits(sourceSections), false); tm != nil {
			out["textMatches"] = tm
			out["textBackend"] = sel.textBackend
		}
		// Text hits are appended after source delivery, so charge their rendered
		// form as well. Remove lowest-priority hits until the actual MCP text fits.
		var delivered int
		for {
			rendered, ok := renderQuerySourceAsText(out)
			if !ok {
				return nil, errors.New("could not render query source response")
			}
			delivered = ranking.EstimateTokens(rendered)
			if delivered <= sel.budget {
				break
			}
			hits := anySlice(out["textMatches"])
			if len(hits) == 0 {
				return nil, fmt.Errorf("query source response exceeds budget=%d", sel.budget)
			}
			if len(hits) == 1 {
				delete(out, "textMatches")
				delete(out, "textBackend")
			} else {
				out["textMatches"] = hits[:len(hits)-1]
			}
		}
		out["deliveredTokens"] = delivered
		h.Ledger.Record("prism_query", h.queryBaselineTokens(sel.picked, delivered), delivered)
		return out, nil
	}
	picked := sel.picked

	// Build response.
	used := 0
	out := queryResult{
		Symbols: make([]rankedSymbol, 0, len(picked)),
	}
	for _, p := range picked {
		used += p.TokenCost
		out.Symbols = append(out.Symbols, rankedSymbol{
			Name:          p.Symbol.Name,
			QualifiedName: p.Symbol.QualifiedName,
			FilePath:      p.Symbol.FilePath,
			Kind:          p.Symbol.Kind,
			Category:      string(p.Category),
			Content:       ranking.Render(p.Symbol, p.Disclosure),
			Span:          p.Symbol.Span,
		})
	}
	if tm := h.renderTextMatches(ctx, sel.deliverableTextHits(nil), false); tm != nil {
		out.TextMatches = tm
		out.TextBackend = sel.textBackend
	}

	if len(out.Symbols) == 0 && len(out.TextMatches) == 0 {
		var filterMiss scopeFilterReport
		if len(scope.paths) > 0 || len(scope.glob) > 0 {
			filterMiss = scopeFilterCheck(h.Root, scope.paths, scope.glob, true)
		}
		switch {
		case filterMiss.noFiles:
			out.Note = filterMiss.note
		case len(sel.seeds) == 0:
			out.Note = fmt.Sprintf("no symbols matched terms %s under project root %s; check term spelling and that the code lives under this root", formatQueryTerms(terms), h.Root)
		default:
			out.Note = "seeds matched but nothing fit the requested include categories/budget; try include=[\"graph\"] or a larger budget"
		}
	}

	// Baseline for the savings ledger: the token cost of reading each
	// containing file once in full — what assembling the same context by
	// file reads would have cost. Measured from on-disk sizes, never assumed.
	for {
		out.BudgetUsed = used
		encoded, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		measured := ranking.EstimateTokens(string(encoded))
		if measured != used {
			used = measured
			continue
		}
		if used <= sel.budget {
			break
		}
		if n := len(out.TextMatches); n > 0 {
			out.TextMatches = out.TextMatches[:n-1]
			if len(out.TextMatches) == 0 {
				out.TextBackend = ""
			}
			continue
		}
		if n := len(out.Symbols); n > 0 {
			ref := ranking.Render(picked[n-1].Symbol, ranking.DisclosureReference)
			if out.Symbols[n-1].Content != ref {
				out.Symbols[n-1].Content = ref
			} else {
				out.Symbols = out.Symbols[:n-1]
			}
			continue
		}
		return nil, fmt.Errorf("query symbols response cannot fit budget=%d", sel.budget)
	}
	h.Ledger.Record("prism_query", h.queryBaselineTokens(picked, used), used)
	return out, nil
}

// queryBaselineTokens estimates what the delivered context would have cost
// without graph-ranked selection: one full read of every distinct file a
// selected symbol lives in. Files that cannot be stat'ed contribute nothing;
// the result is never below the delivered token count, so savings are never
// invented when measurement fails.
func (h *Handler) queryBaselineTokens(picked []ranking.BudgetedSymbol, delivered int) int {
	seen := map[string]bool{}
	total := 0
	for _, p := range picked {
		fp := normalizePath(p.Symbol.FilePath)
		if fp == "" || seen[fp] {
			continue
		}
		seen[fp] = true
		if info, err := os.Stat(filepath.Join(h.Root, filepath.FromSlash(fp))); err == nil {
			total += int(info.Size() / 4) // ~4 bytes/token, same estimate as EstimateTokens
		}
	}
	if total < delivered {
		return delivered
	}
	return total
}
