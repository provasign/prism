package mcp

import (
	"context"

	"github.com/provasign/prism/internal/grove"
)

// attachSearchBodies is the one place a search answer gets its source
// bodies. The shape says how many (searchshape.go); the strategies below say
// which:
//
//   - the small-result pick (compactSearchBodiesPicked): a unique exact-name
//     symbol, a lone symbol match, or one complete text hit inside at most
//     two functions;
//   - the evidence-ranked choice (compactSearchBodiesWith): hits scored by
//     line shape, term overlap and declaration match, one region under the
//     default budget, up to two per term otherwise, with the hit-order
//     picker (compactSearchBodiesLegacyWith) as its fallback.
//
// Before 2026-10-11 the budgeted choice was attached here and the pick was
// made again at render time, where it replaced the first; an explicit
// context skipped the pick but not the attachment, so windows and every
// enclosing body were both sent. term labels a flat single-term result so
// body selection can recognise the definition of a named symbol.
func (h *Handler) attachSearchBodies(ctx context.Context, out map[string]any, shape searchShape, budget searchBudget, term ...string) {
	if shape.bodies == bodiesNone || h.Grove == nil {
		return
	}
	labeled := out
	if len(term) > 0 {
		labeled = withSearchQuery(out, term[0])
	}
	var bodies string
	switch shape.bodies {
	case bodiesOne:
		picked, delivered := h.compactSearchBodiesPicked(ctx, labeled)
		if picked != "" && budgetPickFits(delivered, budget.testsTargeted) {
			bodies = picked
		} else {
			bodies = h.compactSearchBodiesBudgeted(ctx, labeled, budget.testsTargeted)
		}
	case bodiesWide:
		// The pick first; the enclosing pass skips what the pick printed
		// (its file section, wider than the symbol), not only the symbol.
		picked, delivered := h.compactSearchBodiesPicked(ctx, labeled)
		skip := append(delivered, deliveredSpanSymbols(picked)...)
		bodies = picked + h.compactSearchBodiesEnclosingExcept(ctx, labeled, skip)
	case bodiesWideNoPick:
		bodies = h.compactSearchBodiesEnclosing(ctx, labeled)
	}
	if bodies == "" {
		return
	}
	out["inlineBodies"] = bodies
	if shape.dedupeContext {
		dropContextInsideBodies(out, bodies)
	}
}

// fitSearchAnswer bounds the rendered answer the way the shape says.
func (h *Handler) fitSearchAnswer(ctx context.Context, out map[string]any, queries []string, shape searchShape, budget searchBudget) {
	switch shape.fit {
	case fitBudget, fitBudgetWide:
		h.applySearchBudget(ctx, out, queries, budget)
	case fitBounded:
		boundSearchPresentation(out)
		boundSymbolPresentation(out)
	}
}

// budgetPickFits reports whether a small-result pick can serve as the
// default budget's single body.
func budgetPickFits(picked []grove.SymbolRecord, allowTests bool) bool {
	if len(picked) != 1 {
		return false
	}
	sym := picked[0]
	if !allowTests && isTestFilePath(sym.FilePath) {
		return false
	}
	return sym.Span.End-sym.Span.Start+1 <= searchBudgetBodyLines
}
