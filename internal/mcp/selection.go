package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// selectParams are the inputs to the shared retrieve→expand→rank→budget
// pipeline behind prism_query and prism_explore.
type selectParams struct {
	task            string
	terms           []string
	includeSet      map[string]bool
	explicitProfile string
	limit           int
	contextUsed     int64
	model           string
	budgetArg       int // >0 is honored exactly; 0 = task-sized default with phase shaping
}

// selection is the pipeline output: the budgeted picks plus the intermediate
// sets that response assembly needs (seeds for empty-result notes, seedSyms +
// graphExtra for coverage gaps and blast radius).
type selection struct {
	picked     []ranking.BudgetedSymbol
	seedSyms   []grove.SymbolRecord
	graphExtra []grove.SymbolRecord
	seeds      []grove.SymbolRecord
	budget     int
	// testEdgeIDs marks test symbols connected to a seed by a real tests edge,
	// as opposed to tests that merely matched the task text lexically.
	testEdgeIDs map[string]bool
}

// selectContext runs retrieval (term-seeded or intent-ranked), graph and test
// expansion, scoring, and budgeted selection. It is the single pipeline both
// prism_query and prism_explore deliver from; only the delivery format differs.
func (h *Handler) selectContext(ctx context.Context, p selectParams) (*selection, error) {
	// B: phase-aware budget shaping — infer the agent work phase from the task
	// description and auto-select a matching profile + budget multiplier.
	// An explicit "profile" arg always wins; otherwise let phase detection decide.
	phase := ranking.DetectPhase(p.task)
	phaseProfileHint, phaseBudgetMult := ranking.ShapeForPhase(phase)
	profileName := p.explicitProfile
	if profileName == "" {
		profileName = phaseProfileHint
	}
	if profileName == "" {
		profileName = h.Cfg.Profile
	}
	callCfg := h.Cfg.WithModel(p.model)

	// Semantic similarity scores for this task, served from Grove's cached
	// embedding index (one engine call; no corpus rebuild in Prism).
	h.loadSemanticScores(ctx, p.task)
	var seeds []grove.SymbolRecord

	if len(p.terms) > 0 {
		// Term-seeded retrieval: search for each agent-supplied term and union
		// the results. This gives grep-level precision as the entry point.
		seenTermSeeds := map[string]bool{}
		for _, term := range p.terms {
			matches, err := h.Grove.SearchSymbols(ctx, term, 10)
			if err != nil {
				continue
			}
			// Prioritise symbols whose Name/QualifiedName contains the term
			// (grep-level precision). Content-only matches (term appears only
			// in RawText) are capped at 3 to suppress doc-string noise.
			termLower := strings.ToLower(term)
			var nameHits, contentHits []grove.SymbolRecord
			for _, m := range matches {
				if strings.Contains(strings.ToLower(m.Name), termLower) ||
					strings.Contains(strings.ToLower(m.QualifiedName), termLower) {
					nameHits = append(nameHits, m)
				} else {
					contentHits = append(contentHits, m)
				}
			}
			if len(contentHits) > 3 {
				contentHits = contentHits[:3]
			}
			// Prefer real implementations over test doubles among name hits, so a
			// term like "DecryptedValues" seeds the graph on the real Service
			// method (and expands its call chain) rather than on a mock that
			// shares the name — which would leave the real chain out of reach.
			var realHits, doubleHits []grove.SymbolRecord
			for _, m := range nameHits {
				if isTestDouble(m.FilePath) {
					doubleHits = append(doubleHits, m)
				} else {
					realHits = append(realHits, m)
				}
			}
			nameHits = append(realHits, doubleHits...)
			for _, m := range append(nameHits, contentHits...) {
				if !seenTermSeeds[m.ID] {
					seenTermSeeds[m.ID] = true
					seeds = append(seeds, m)
				}
			}
		}
		seeds = filterGeneratedPrismContext(seeds)
	} else {
		// Intent-ranked fallback (Grove Query) when no terms provided.
		var err error
		seeds, err = h.Grove.QueryByIntent(ctx, p.task, p.limit)
		if err != nil {
			return nil, fmt.Errorf("grove query: %w", err)
		}
		seeds = filterGeneratedPrismContext(seeds)
		seeds = filterDocSeeds(seeds)
	}
	// Build candidates: treat first 5 as seeds (distance 0), remainder as candidates.
	seedCount := minInt(5, len(seeds))
	seedSyms := seeds[:seedCount]
	candidateSyms := seeds[seedCount:]

	profile := ranking.SelectProfile(profileName)
	profile = h.Weights.Apply(profile)

	// For test-writing tasks, boost TestRelevance so test symbols rank higher
	// in scoring. The budget expansion happens after callerBudget is parsed.
	if p.explicitProfile == "" && isTestWritingTask(p.task) && profile.TestRelevance < 0.45 {
		profile.TestRelevance = minFloat(profile.TestRelevance*2.0, 0.45)
	}

	graphDist := make(map[string]int)
	hasTestEdgeID := make(map[string]bool)
	testFilePaths := make(map[string]bool)

	seenIDs := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seenIDs[s.ID] = true
	}
	var graphExtra []grove.SymbolRecord

	for _, seed := range seedSyms {
		// Expand by qualified name when the symbol has one: bare names
		// ("Get", "Keys") collide across packages on large repos and drag
		// unrelated symbols' callers and tests into the result set.
		seedQuery := seed.QualifiedName
		if seedQuery == "" {
			seedQuery = seed.Name
		}
		if p.includeSet["graph"] {
			// Use the typed call neighborhood (callees + callers, test doubles
			// excluded) rather than Grove.Impact's flat blast radius. Impact
			// traverses calls AND uses-type together and erases edge types, which
			// floods the result with type-mention noise and buries the actual
			// call chain; CallNeighbors returns exactly the resolved calls edges.
			if neighbors, err := h.Grove.CallNeighbors(ctx, seedQuery); err == nil {
				for _, nb := range neighbors {
					if _, exists := graphDist[nb.ID]; !exists {
						graphDist[nb.ID] = 1
					}
					if !seenIDs[nb.ID] {
						seenIDs[nb.ID] = true
						graphExtra = append(graphExtra, nb)
					}
				}
			}
		}
		if p.includeSet["tests"] {
			if tests, err := h.Grove.Tests(ctx, seedQuery); err == nil {
				for _, tst := range tests {
					hasTestEdgeID[tst.ID] = true
					testFilePaths[tst.FilePath] = true
					if _, exists := graphDist[tst.ID]; !exists {
						graphDist[tst.ID] = 1
					}
					if !seenIDs[tst.ID] {
						seenIDs[tst.ID] = true
						graphExtra = append(graphExtra, tst)
					}
				}
			}
		}
	}

	// Merge candidates and graph-enriched symbols, then filter by include set.
	merged := make([]grove.SymbolRecord, 0, len(candidateSyms)+len(graphExtra))
	merged = append(merged, candidateSyms...)
	merged = append(merged, graphExtra...)

	// Drop categories the agent did not request.
	if len(p.includeSet) > 0 {
		filtered := merged[:0]
		for _, sym := range merged {
			cat := string(categorize(sym))
			switch {
			case cat == string(ranking.CategoryTest) && !p.includeSet["tests"]:
				continue
			case cat == string(ranking.CategoryDoc) && !p.includeSet["docs"]:
				continue
			case (cat == string(ranking.CategoryTarget) || cat == string(ranking.CategoryDependency)) && !p.includeSet["graph"]:
				continue
			}
			filtered = append(filtered, sym)
		}
		merged = filtered
	}

	candidates := make([]ranking.Candidate, 0, len(merged))
	for i, sym := range merged {
		dist, inGraph := graphDist[sym.ID]
		if !inGraph {
			// Not reached by BFS: fall back to retrieval position as distance
			// proxy so semantically adjacent symbols still score above
			// unrelated ones.
			dist = 3 + (i / 10)
		}
		sv := h.Signals.Compute(ctx, p.task, sym, dist, hasTestEdgeID[sym.ID], testFilePaths[sym.FilePath])
		score := ranking.Score(sv, profile)
		cat := categorize(sym)
		sessionPath := normalizePath(sym.FilePath)
		entry, seen, _ := h.Session.Lookup(sessionPath, "")
		conf := session.Low
		if seen {
			conf = h.confidenceFor(entry, p.contextUsed, callCfg.ContextWindow())
		}
		candidates = append(candidates, ranking.Candidate{
			Symbol:         sym,
			Score:          score,
			Category:       cat,
			PreviouslySeen: seen,
			Confidence:     string(conf),
		})
	}
	// Default budget is task-sized (8k tokens), not context-window-sized.
	// The score-cliff cutoff in Select() stops early when relevance drops off,
	// so the ceiling here is a safety cap, not a fill target.
	const defaultTaskBudget = 8000
	var budget int
	if p.budgetArg > 0 {
		// An explicit budget is a contract: honor it exactly — no floor, no
		// phase shaping. The caller knows its token constraints best.
		budget = p.budgetArg
	} else {
		budget = defaultTaskBudget
		// B: apply phase-derived budget multiplier (e.g. 0.60 for code_review),
		// floored so a shaped default never starves the response.
		if phaseBudgetMult > 0 && phaseBudgetMult != 1.0 {
			shaped := int(float64(budget) * phaseBudgetMult)
			if shaped < 4000 {
				shaped = 4000
			}
			budget = shaped
		}
		// Expand budget for test-writing tasks so the test category gets more
		// absolute token room (20% share of a larger total = more test content).
		if p.explicitProfile == "" && isTestWritingTask(p.task) {
			budget = int(float64(budget) * 1.25)
		}
	}
	picked := ranking.Select(seedSyms, candidates, budget)

	return &selection{
		picked:      picked,
		seedSyms:    seedSyms,
		graphExtra:  graphExtra,
		seeds:       seeds,
		budget:      budget,
		testEdgeIDs: hasTestEdgeID,
	}, nil
}
