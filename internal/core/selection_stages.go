package core

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// selectionState carries the locals that the selectContext stages (seed
// resolution, graph expansion, candidate scoring) share.
type selectionState struct {
	p     selectParams
	scope searchScope

	// Seed resolution.
	seeds     []grove.SymbolRecord
	textMerge textMergeResult
	// contentOnlySeeds: see the declaration in selectContext.
	contentOnlySeeds map[string]bool
	seedSyms         []grove.SymbolRecord
	candidateSyms    []grove.SymbolRecord

	// Graph expansion.
	directCallIDs map[string]bool
	// testCallers: see the declaration in selectionExpandGraph.
	testCallers  map[string][]grove.SymbolRecord
	graphExtra   []grove.SymbolRecord
	familySyms   []grove.SymbolRecord
	relatedTests []relatedTestHint
}

// selectionTermSeeds resolves st.seeds from the explicit terms: per-term
// symbol search, round-robin interleave, then the full-text merge.
func (h *Handler) selectionTermSeeds(ctx context.Context, st *selectionState) error {
	p := st.p
	// Term-seeded retrieval: search for each agent-supplied term and union
	// the results. This gives grep-level precision as the entry point.
	//
	// Seeds INTERLEAVE across terms (round-robin below) instead of
	// concatenating per term. Concatenation let one noisy term poison
	// the whole selection: measured 2026-08-26 (jackson-core-1309),
	// terms ["valueOf","looksLikeValidNumber"] scored gold-recall 0.0
	// where the good term alone scored 1.0 — valueOf's fan-out filled
	// every seed slot and the term that named the actual fix region
	// never seeded. Each term now gets seed representation.
	perTermSeeds := make([][]grove.SymbolRecord, 0, len(p.terms))
	for _, term := range p.terms {
		termSeeds, ok, err := h.selectionSeedsForTerm(ctx, st, term)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		perTermSeeds = append(perTermSeeds, termSeeds)
	}
	st.seeds = interleaveUniqueTermSeeds(perTermSeeds)
	st.seeds = filterGeneratedPrismContext(st.seeds)
	h.selectionMergeText(ctx, st, len(perTermSeeds))
	return nil
}

// selectionSeedsForTerm returns one term's seeds: name hits (real
// implementations before test doubles) followed by at most three content-only
// hits. ok=false means the symbol search failed and the term contributes no
// interleave slot; a non-nil error aborts the whole selection.
func (h *Handler) selectionSeedsForTerm(ctx context.Context, st *selectionState, term string) ([]grove.SymbolRecord, bool, error) {
	p, scope := st.p, st.scope
	// Honor --limit here too. This path hardcoded 10, so
	// `--limit 50 --terms Foo` silently capped at 10 per term — and
	// terms is the RECOMMENDED entry point in prism's own guidance.
	perTerm := p.limit
	if perTerm <= 0 {
		perTerm = 10
	}
	fetchLimit := minInt(exhaustiveSymbolCap, maxInt(minInt(perTerm, exhaustiveSymbolCap/4)*4, 64))
	var matches []grove.SymbolRecord
	var err error
	if len(p.paths) > 0 || len(p.glob) > 0 {
		var exhausted bool
		searchScoped := func(ctx context.Context, query string, limit int) ([]grove.SymbolRecord, error) {
			return h.Grove.SearchSymbolsScoped(ctx, query, limit, scope.paths, scope.glob)
		}
		matches, exhausted, err = scopedSymbolSearch(ctx, searchScoped, term, scope, fetchLimit, symbolFetchHardMax)
		if err == nil && !exhausted && len(matches) <= fetchLimit {
			return nil, false, fmt.Errorf("scoped query for %q could not complete its bounded symbol scan; narrow paths/glob or use a longer term", term)
		}
	} else {
		matches, err = h.Grove.SearchSymbols(ctx, term, fetchLimit)
	}
	if err != nil {
		return nil, false, nil
	}
	ranked := rankSearchSymbols(matches, term)
	matches = matches[:0]
	for _, item := range ranked {
		if item.symbol.Kind == "document" && !p.includeSet["docs"] {
			continue
		}
		matches = append(matches, item.symbol)
	}
	if len(matches) > perTerm {
		matches = matches[:perTerm]
	}
	// Prioritise symbols whose Name/QualifiedName contains the term
	// (grep-level precision). Content-only matches (term appears only
	// in RawText) are capped at 3 to suppress doc-string noise.
	termLower := strings.ToLower(term)
	var nameHits, contentHits []grove.SymbolRecord
	for _, m := range matches {
		nameLower, qualLower := strings.ToLower(m.Name), strings.ToLower(m.QualifiedName)
		nameContains := strings.Contains(nameLower, termLower) || strings.Contains(qualLower, termLower)
		switch {
		case nameContains && isTestFilePath(m.FilePath) && !explicitTestTerm(termLower):
			// A test whose name merely CONTAINS the term as a
			// substring -- the common case for any TestFoo-style
			// naming convention, which trivially contains "Foo" --
			// is not a deliberate ask. Exact common method names such as
			// `skip` are excluded too; terms that explicitly say test/spec
			// (for example TestFoo) still seed normally below.
			// Seeds are force-stamped CategoryTarget in
			// ranking.Select, bypassing the CategoryTest delivery
			// guard entirely, so an incidental substring match
			// here was pr3493's exact failure (an unrelated test
			// earning a whole source window) relocated from the
			// candidate path -- fixed in v0.25.0 -- to the seed
			// path, which never was (caught 2026-09-02 writing a
			// test for the "tested by" pointer feature: terms=
			// ["FormatGreeting"] pulled in the FULL BODY of
			// TestFormatGreeting, purely because that name
			// contains "FormatGreeting"). EXCLUDED outright, not
			// merely deprioritized into contentHits -- on a small
			// candidate set a deprioritized bucket still survives
			// the seed cut (measured: it did, on exactly this
			// fixture). It is still discoverable, correctly, via
			// the new InboundCallers "tested by" pointer.
			continue
		case nameContains:
			nameHits = append(nameHits, m)
		case isTestFilePath(m.FilePath):
			// Content-only match landing inside a test file's
			// body (e.g. it calls the production function under
			// test) -- same reasoning: never seed a test this way.
			continue
		default:
			contentHits = append(contentHits, m)
			// Demotion is decided separately from bucket routing:
			// a FILE PATH match (term "jsonrpc_app.py" naming the
			// file) is a deliberate ask and keeps full disclosure,
			// but must NOT join nameHits — the first attempt did
			// that and a term like "__init__.py" flooded the seed
			// order with every package's file, collapsing
			// oracle-terms recall on two cells. Routing stays
			// name/qual-only; only the disclosure decision is
			// path-aware. (Both directions measured 2026-09-02 on
			// the query oracle bed: demoting path matches 0.588 ->
			// 0.335; promoting them into nameHits broke two cells
			// the other way.)
			if !strings.Contains(strings.ToLower(m.FilePath), termLower) {
				st.contentOnlySeeds[m.ID] = true
			}
		}
	}
	if len(contentHits) > 3 {
		contentHits = contentHits[:3]
	}
	// Prefer real implementations over test doubles (mock/fake/stub,
	// non-test-file) among name hits, so a term like "DecryptedValues"
	// seeds the graph on the real Service method (and expands its
	// call chain) rather than on a mock that shares the name — which
	// would leave the real chain out of reach.
	var realHits, doubleHits []grove.SymbolRecord
	for _, m := range nameHits {
		if isTestDouble(m.FilePath) {
			doubleHits = append(doubleHits, m)
		} else {
			realHits = append(realHits, m)
		}
	}
	nameHits = append(realHits, doubleHits...)
	termSeeds := append(append([]grove.SymbolRecord(nil), nameHits...), contentHits...)
	return termSeeds, true, nil
}

// selectionMergeText runs the full-text search for the terms, appends the
// text-promoted seeds and promotes text-confirmed seeds within each
// interleave round. termRounds is the number of terms that produced seeds.
func (h *Handler) selectionMergeText(ctx context.Context, st *selectionState, termRounds int) {
	p := st.p
	// Full-text merge: run the real grep (rg/grep/native) for the same
	// terms. Hits inside indexed symbols promote or confirm seeds; hits
	// the graph cannot see (comments, configs, docs) are delivered raw.
	// This is why the agent needs no separate grep tool.
	seededIDs := make(map[string]bool, len(st.seeds))
	for _, s := range st.seeds {
		seededIDs[s.ID] = true
	}
	st.textMerge = h.mergeTextSearchScoped(ctx, p.terms, seededIDs, st.scope)
	if len(st.textMerge.extraSeeds) > 0 {
		extra := filterGeneratedPrismContext(st.textMerge.extraSeeds)
		// extraSeeds are pure text hits promoted to a seed by whatever
		// symbol encloses them -- always content-driven, never a name
		// match, so (unlike the SearchSymbols path above) there is no
		// "exact name" carve-out here: a test file's body mentioning a
		// term is never a deliberate ask, only ever incidental. Same
		// pr3493-class bug this loop's sibling exclusion fixes -- this
		// is the second of two places a test could reach `seeds` this
		// way (caught 2026-09-02: excluding only the SearchSymbols path
		// was not enough, mergeTextSearch's grep pass re-added the same
		// symbol via this list on the identical fixture).
		kept := extra[:0]
		for _, s := range extra {
			if s.Kind == "document" && !p.includeSet["docs"] {
				continue
			}
			if isTestFilePath(s.FilePath) {
				continue
			}
			// extraSeeds are content-driven BY CONSTRUCTION (a grep hit
			// inside the symbol's body promoted it) — unless the symbol
			// also name-matches a term, it gets the same signature-level
			// demotion as SearchSymbols content hits. This was the path
			// the upai2v1g #93 license-header dump actually took.
			if !seedNameMatchesAnyTerm(s, p.terms) {
				st.contentOnlySeeds[s.ID] = true
			}
			kept = append(kept, s)
		}
		st.seeds = append(st.seeds, kept...)
	}
	if len(st.textMerge.confirmed) > 0 {
		seeds, confirmed := st.seeds, st.textMerge.confirmed
		// Two independent signals (symbol match + text hit) beat one —
		// but promote confirmed seeds within each ROUND of the term
		// interleave, never globally. The global reorder un-did the
		// round-robin: a broad term text-matches everywhere, so ALL its
		// seeds were "confirmed" and promoted above the precise terms'
		// seeds (a qualified term's literal never appears in source
		// text), and the top-5 seed cut was single-term again —
		// measured 2026-08-26 (jackson-core-1263): the qualified terms
		// contributed ZERO anchors and gold recall was 0.0 while
		// change-impact knew the whole family. Round position = which
		// interleave pass produced the seed; promotion happens inside a
		// round, so every term keeps its seed representation.
		if termRounds == 1 {
			term := ""
			if len(p.terms) == 1 {
				term = strings.ToLower(p.terms[0])
			}
			st.seeds = promoteSingleTermSeeds(seeds, confirmed, term)
		} else {
			roundOf := make(map[string]int, len(seeds))
			for i, s := range seeds {
				roundOf[s.ID] = i // interleave emitted round-major order
			}
			sort.SliceStable(seeds, func(i, j int) bool {
				ri, rj := roundOf[seeds[i].ID]/termRounds,
					roundOf[seeds[j].ID]/termRounds
				if ri != rj {
					return ri < rj
				}
				ci, cj := confirmed[seeds[i].ID], confirmed[seeds[j].ID]
				if ci != cj {
					return ci
				}
				return roundOf[seeds[i].ID] < roundOf[seeds[j].ID]
			})
		}
	}
}

// selectionExpandGraph expands every seed through its verified call
// neighborhood, test callers and override/overload family, then orders and
// scopes the derived sets and, when no verified test caller exists, collects
// lexical related-test hints.
func (h *Handler) selectionExpandGraph(ctx context.Context, st *selectionState) {
	p, scope := st.p, st.scope
	st.directCallIDs = make(map[string]bool)
	// testCallers is pointer-only (delivery.go renders locations, never
	// bodies): a verified `calls` edge from a real test file into a seed.
	// Deliberately outside the budget/disclosure pipeline entirely -- the
	// prior CategoryTest delivery path was removed (pr3493) because a
	// lexically-task-matched test with no verified relation to the anchor
	// earned a whole source window; a capped location list cannot repeat
	// that failure because it never competes for budget or renders a body.
	st.testCallers = make(map[string][]grove.SymbolRecord)

	seenIDs := make(map[string]bool, len(st.seeds))
	for _, s := range st.seeds {
		seenIDs[s.ID] = true
	}
	famSeen := make(map[string]bool)

	for _, seed := range st.seedSyms {
		if p.includeSet["graph"] {
			h.selectionExpandSeed(ctx, st, seed, seenIDs, famSeen)
		}
	}

	graphExtra, familySyms := st.graphExtra, st.familySyms
	// Graph-derived sets arrive in adjacency-map order; sort them into a
	// stable order at the source so no downstream tie can inherit map layout.
	sort.SliceStable(graphExtra, func(i, j int) bool {
		if graphExtra[i].FilePath != graphExtra[j].FilePath {
			return graphExtra[i].FilePath < graphExtra[j].FilePath
		}
		if graphExtra[i].Span.Start != graphExtra[j].Span.Start {
			return graphExtra[i].Span.Start < graphExtra[j].Span.Start
		}
		return graphExtra[i].ID < graphExtra[j].ID
	})
	sort.SliceStable(familySyms, func(i, j int) bool {
		if familySyms[i].FilePath != familySyms[j].FilePath {
			return familySyms[i].FilePath < familySyms[j].FilePath
		}
		return familySyms[i].Span.Start < familySyms[j].Span.Start
	})
	if len(p.paths) > 0 || len(p.glob) > 0 {
		st.graphExtra = filterSymbolsByScope(st.graphExtra, scope)
		st.familySyms = filterSymbolsByScope(st.familySyms, scope)
		for seedID, callers := range st.testCallers {
			st.testCallers[seedID] = filterSymbolsByScope(callers, scope)
		}
	}
	if p.includeSet["graph"] && !hasAnyTestCallers(st.testCallers) {
		st.relatedTests = h.relatedTestHints(ctx, p.terms, scope)
	}
}

// selectionExpandSeed adds one seed's call neighbors to graphExtra, its
// verified test callers to testCallers, and its family to familySyms.
func (h *Handler) selectionExpandSeed(ctx context.Context, st *selectionState, seed grove.SymbolRecord, seenIDs, famSeen map[string]bool) {
	const testCallersPerSeedCap = 5
	// Expand by the seed's identity (identity.go): even qualified names
	// collide across files and packages ("Target" in left/ and right/)
	// and drag unrelated symbols' callers and tests into the result
	// set. seedQuery (qualified name) remains only for the file-scoped
	// family query below.
	seedQuery := seed.QualifiedName
	if seedQuery == "" {
		seedQuery = seed.Name
	}
	// Use the typed call neighborhood (callees + callers, test doubles
	// excluded) rather than Grove.Impact's flat blast radius. Impact
	// traverses calls AND uses-type together and erases edge types, which
	// floods the result with type-mention noise and buries the actual
	// call chain; CallNeighbors returns exactly the resolved calls edges.
	if neighbors, err := h.identityCallNeighbors(ctx, seed); err == nil {
		for _, nb := range neighbors {
			st.directCallIDs[nb.ID] = true
			if !seenIDs[nb.ID] {
				seenIDs[nb.ID] = true
				st.graphExtra = append(st.graphExtra, nb)
			}
		}
		// Verified test callers, for the pointer line in
		// renderAnchorSummary -- NOT added to graphExtra/candidates,
		// see testCallers' declaration in selectionExpandGraph.
		if tc, err := h.identityInboundCallers(ctx, seed); err == nil {
			for _, caller := range tc {
				if !isVerifiedTestCaller(caller.FilePath) {
					continue
				}
				if len(st.testCallers[seed.ID]) < testCallersPerSeedCap {
					st.testCallers[seed.ID] = append(st.testCallers[seed.ID], caller)
				}
			}
		}
	}
	// Family expansion — into a SEPARATE set, never the candidate
	// pool. The call neighborhood alone missed a third of gold fix
	// regions on the jackson oracle bed (recall 0.65 with PERFECT
	// terms, 2026-08-26): every autopsied miss was a family member —
	// the same method in a sibling class, or an overload two methods
	// down. But the first version dumped family into the candidates
	// and mean recall DROPPED to 0.55: family lives in sibling
	// FILES, so it competed for the sourceDeliveryMaxFiles slots and
	// evicted the files that were covering gold, while itself
	// ranking too low to be delivered. Zero-sum budgets turn naive
	// enrichment into displacement. Family is therefore carried
	// separately and rendered as its own appended section
	// (delivery.go), where it can only add coverage.
	if r, err := h.Grove.ChangeImpactScoped(ctx, seedQuery, seed.FilePath); err == nil && r != nil && os.Getenv("PRISM_NO_FAMILY") == "" {
		for _, fs := range r.Family {
			if len(st.familySyms) >= 12 {
				break
			}
			// NOT seenIDs: marking family there stole symbols from
			// graphExtra (a family member that is also a later
			// seed's call-neighbor was silently demoted from a main
			// window to the truncated appendix — measured as recall
			// 0.65 -> 0.57 even in append-only form). The two sets
			// dedupe at render time instead.
			// famSeen only — NOT seenIDs. A same-name override IS
			// found by term search, so every family member is
			// already a seed candidate; excluding "known" symbols
			// excluded the entire family (measured: appendix never
			// fired, family-vs-none identical on all 12 oracle
			// tasks). Known-but-unpicked is exactly what the
			// appendix exists to rescue; true duplication is
			// prevented at render time against the PICKED set.
			if !famSeen[fs.ID] {
				famSeen[fs.ID] = true
				st.familySyms = append(st.familySyms, fs)
			}
		}
	}
}

// selectionCandidates merges the non-seed retrieval results with the graph
// neighbors, drops categories the agent did not request, and scores each
// remaining symbol as a ranking candidate.
func (h *Handler) selectionCandidates(st *selectionState, profile ranking.Profile, callCfg *config.Config) []ranking.Candidate {
	p := st.p
	// Merge candidates and graph-enriched symbols, then filter by include set.
	merged := make([]grove.SymbolRecord, 0, len(st.candidateSyms)+len(st.graphExtra))
	merged = append(merged, st.candidateSyms...)
	merged = append(merged, st.graphExtra...)

	// Drop categories the agent did not request.
	if len(p.includeSet) > 0 {
		filtered := merged[:0]
		for _, sym := range merged {
			cat := string(categorize(sym))
			switch {
			case cat == string(ranking.CategoryTest):
				// Test-coverage edges were removed; a lexically task-matched
				// test carries no verified relation to the anchor, so it is
				// never delivered.
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
		inGraph := st.directCallIDs[sym.ID]
		// Grove's retrieval order is the only within-tier signal. It is
		// deterministic for a fixed index and does not read live Git history.
		sv := ranking.SignalValues{RetrievalOrder: 1.0 / (1.0 + float64(i)/50.0)}
		score := ranking.Score(sv, profile)
		relation := ranking.RelationRetrieval
		if inGraph {
			relation = ranking.RelationDirectCall
		}
		cat := categorize(sym)
		sessionPath := normalizePath(sym.FilePath)
		entry, seen, _ := h.Session.Lookup(sessionPath, "")
		conf := session.Low
		if seen {
			conf = h.confidenceFor(entry, p.contextUsed, callCfg.ContextWindow())
		}
		candidates = append(candidates, ranking.Candidate{
			Symbol:         sym,
			Relation:       relation,
			Score:          score,
			Category:       cat,
			PreviouslySeen: seen,
			Confidence:     string(conf),
		})
	}
	return candidates
}
