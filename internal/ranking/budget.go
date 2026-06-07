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

// ScoreCliffFactor is the multiplier applied to the highest candidate score
// seen so far to derive the cutoff for subsequent candidates. When a
// candidate's score drops below (peakScore * ScoreCliffFactor), selection
// stops — the remaining candidates are noise relative to the top of the list.
// 0.6 means: stop when score falls more than 40% below the peak.
const ScoreCliffFactor = 0.6

// Select runs the budget-aware greedy selector.
//
//   - Seeds are always included at DisclosureFull and are NOT charged against
//     the budget (they ARE the targets).
//   - Remaining candidates are sorted by score desc and assigned a disclosure
//     level that fits each per-category budget.
//   - Candidates below RelevanceThreshold are forced to DisclosureSignature
//     even if a higher level would fit.
//   - Previously-seen items are demoted by confidence.
//   - Selection stops when a candidate's score drops more than ScoreCliffFactor
//     below the peak score seen so far (score cliff cutoff).
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

	var peakScore float64
	for _, cand := range candidates {
		// Score-cliff cutoff: stop when relevance falls off sharply from the
		// peak. This prevents budget-filling with low-signal noise once the
		// genuinely relevant symbols have been selected.
		if cand.Score > peakScore {
			peakScore = cand.Score
		} else if peakScore > 0 && cand.Score < peakScore*ScoreCliffFactor {
			break
		}

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

// IsTrivialBody reports whether a symbol's body carries no additional
// information beyond its signature. Trivial symbols (short one-liners,
// passthrough wrappers, getters) are always rendered at DisclosureSignature
// regardless of their relevance score — the agent needs the signature but
// gains nothing from seeing the implementation.
//
// A symbol is trivial when ALL of:
//   - span is populated (End > Start) and spans ≤ 8 lines
//   - no outgoing calls (CallSites is empty)
//   - kind is function, method, or constructor
func IsTrivialBody(sym grove.SymbolRecord) bool {
	if sym.Span.End <= sym.Span.Start {
		return false // span not populated — do not assume trivial
	}
	if sym.Span.End-sym.Span.Start > 8 {
		return false
	}
	if len(sym.CallSites) > 0 {
		return false
	}
	switch sym.Kind {
	case "function", "method", "constructor":
		return true
	}
	return false
}

// chooseDisclosure picks the desired (pre-budget-check) disclosure level
// based on score + session history.
func chooseDisclosure(c Candidate) DisclosureLevel {
	// Doc symbols (markdown, plaintext) have no graph to traverse.
	// Return reference level only — the agent gets the ranked file name
	// and fetches the content itself via prism_read if needed.
	if c.Category == CategoryDoc {
		return DisclosureReference
	}
	if c.PreviouslySeen {
		switch c.Confidence {
		case "high":
			return DisclosureReference
		case "medium":
			return DisclosureSignature
		}
		// low confidence — treat as fresh
	}
	// Trivial bodies are semantically equivalent to their signature.
	if IsTrivialBody(c.Symbol) {
		return DisclosureSignature
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
		if sym.Kind == "document" || sym.Language == "plaintext" {
			if sym.Signature != "" {
				return sym.Signature
			}
			if sym.Docstring != "" {
				return sym.Docstring
			}
		}
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
