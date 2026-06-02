package ranking

import (
	"sort"

	"github.com/provasign/prism/internal/grove"
)

// DisclosureLevel controls how much of a symbol is rendered.
type DisclosureLevel string

const (
	DisclosureFull      DisclosureLevel = "full"
	DisclosureSignature DisclosureLevel = "signature"
	DisclosureReference DisclosureLevel = "reference"
)

// Category is the budget allocation bucket for a symbol.
type Category string

const (
	CategoryTarget     Category = "target"
	CategoryDependency Category = "dependency"
	CategoryTest       Category = "test"
	CategoryDoc        Category = "doc"
	CategorySummary    Category = "summary"
)

// CategoryShares maps the budget fraction per category.
var CategoryShares = map[Category]float64{
	CategoryTarget:     0.35,
	CategoryDependency: 0.25,
	CategoryTest:       0.20,
	CategoryDoc:        0.10,
	CategorySummary:    0.10,
}

// BudgetedSymbol is the output of the selector for one chosen symbol.
type BudgetedSymbol struct {
	Symbol     grove.SymbolRecord
	Score      float64
	Category   Category
	Disclosure DisclosureLevel
	TokenCost  int
}

// Candidate is one input symbol with a precomputed score and category.
type Candidate struct {
	Symbol   grove.SymbolRecord
	Score    float64
	Category Category
	// PreviouslySeen indicates the symbol's file has already been delivered
	// in this session at the given confidence.
	PreviouslySeen bool
	Confidence     string // "high" | "medium" | "low"
}

// Select runs the budget-aware greedy selector.
//
//   - Seeds are always included at DisclosureFull and are NOT charged against
//     the budget (they ARE the targets).
//   - Remaining candidates are sorted by score desc and assigned a disclosure
//     level that fits each per-category budget.
//   - Candidates below RelevanceThreshold are forced to DisclosureSignature
//     even if a higher level would fit.
//   - Previously-seen items are demoted by confidence.
func Select(seeds []grove.SymbolRecord, candidates []Candidate, totalBudget int) []BudgetedSymbol {
	out := make([]BudgetedSymbol, 0, len(seeds)+len(candidates))
	for _, s := range seeds {
		out = append(out, BudgetedSymbol{
			Symbol:     s,
			Score:      1.0,
			Category:   CategoryTarget,
			Disclosure: DisclosureFull,
			TokenCost:  EstimateTokens(Render(s, DisclosureFull)),
		})
	}

	// Per-category budgets. Targets share is reserved for seeds in this
	// simple model; the budget allocator treats it as already spent and
	// distributes the rest among non-target categories proportionally.
	perCat := map[Category]int{}
	for c, share := range CategoryShares {
		if c == CategoryTarget {
			continue
		}
		perCat[c] = int(float64(totalBudget) * share)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	for _, cand := range candidates {
		desired := chooseDisclosure(cand)
		// Try desired level first, then degrade until it fits or we give up.
		levels := []DisclosureLevel{desired, DisclosureSignature, DisclosureReference}
		picked := DisclosureLevel("")
		var cost int
		for _, lvl := range levels {
			cost = EstimateTokens(Render(cand.Symbol, lvl))
			if cost <= perCat[cand.Category] {
				picked = lvl
				break
			}
		}
		if picked == "" {
			continue // does not fit even at reference level
		}
		perCat[cand.Category] -= cost
		out = append(out, BudgetedSymbol{
			Symbol:     cand.Symbol,
			Score:      cand.Score,
			Category:   cand.Category,
			Disclosure: picked,
			TokenCost:  cost,
		})
	}
	return out
}

// chooseDisclosure picks the desired (pre-budget-check) disclosure level
// based on score + session history.
func chooseDisclosure(c Candidate) DisclosureLevel {
	if c.PreviouslySeen {
		switch c.Confidence {
		case "high":
			return DisclosureReference
		case "medium":
			return DisclosureSignature
		}
		// low confidence — treat as fresh
	}
	if c.Score >= RelevanceThreshold {
		return DisclosureFull
	}
	return DisclosureSignature
}

// Render returns the textual representation of a symbol at the given level.
// Used by both the selector (cost estimate) and the compressor (output).
func Render(sym grove.SymbolRecord, lvl DisclosureLevel) string {
	switch lvl {
	case DisclosureFull:
		if sym.RawText != "" {
			return sym.RawText
		}
		return sym.Signature
	case DisclosureSignature:
		if sym.Docstring != "" && sym.Signature != "" {
			return sym.Docstring + "\n" + sym.Signature
		}
		if sym.Signature != "" {
			return sym.Signature
		}
		return sym.QualifiedName
	case DisclosureReference:
		return formatReference(sym)
	}
	return sym.QualifiedName
}

func formatReference(s grove.SymbolRecord) string {
	q := s.QualifiedName
	if q == "" {
		q = s.Name
	}
	return s.Kind + " " + q + " (" + s.FilePath + ":" + itoa(s.Span.Start) + ")"
}

// EstimateTokens is the ~4 chars/token approximation also used by Grove.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len(text) / 4
	if n == 0 {
		n = 1
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
