package mcp

import (
	"sort"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
	"github.com/provasign/prism/internal/textsearch"
)

// selectParams are the inputs to the shared retrieve→expand→rank→budget
// pipeline behind prism_query and prism_explore.
type selectParams struct {
	minedTerms []string // identifiers mined from the task text; seed AFTER explicit terms
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
	familySyms []grove.SymbolRecord
	graphExtra []grove.SymbolRecord
	seeds      []grove.SymbolRecord
	budget     int
	// testCallers maps a seed's symbol ID to its verified test callers
	// (real test files, doubles excluded) -- pointer-only, see the
	// declaration in selectContext for why this never enters the
	// budget/disclosure pipeline.
	testCallers map[string][]grove.SymbolRecord
	// contentOnlySeeds: seed IDs matched only by body content, never by
	// name — delivered at signature disclosure, not full windows (see the
	// declaration in selectContext).
	contentOnlySeeds map[string]bool
	// textHits are full-text matches no indexed symbol encloses (comments,
	// configs, docs) — the grep half of the merged search; textBackend
	// records which engine produced them (rg/grep/native).
	textHits    []textsearch.Hit
	textBackend string
}

// selectContext runs retrieval (term-seeded or intent-ranked), graph and test
// expansion, scoring, and budgeted selection. It is the single pipeline both
// prism_query and prism_explore deliver from; only the delivery format differs.
func (h *Handler) selectContext(ctx context.Context, p selectParams) (*selection, error) {
	// The task string does NOT choose the profile or the budget.
	//
	// It used to: DetectPhase() keyword-matched the English task and picked a
	// ranking profile plus a budget multiplier from it, so rewording the same
	// request changed which files came back and how many. That is a natural-
	// language retrieval key, which is exactly what this surface elsewhere
	// refuses to have — the v0.41.0 measurement that killed the NL front door
	// applies with equal force to an NL back door. Measured downstream: agents
	// called query and then searched anyway in 14 of the 32 cells where they
	// used both, which is what an unpredictable result looks like from the
	// outside.
	//
	// Retrieval now keys on terms; sizing keys on budget; ranking keys on the
	// profile. All three are the caller's to set, and identical arguments
	// produce an identical selection no matter how the task is phrased.
	profileName := p.explicitProfile
	if profileName == "" {
		profileName = h.Cfg.Profile
	}
	callCfg := h.Cfg.WithModel(p.model)

	timing := os.Getenv("PRISM_TIMING") != ""
	tSel := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing]   sel:%-18s %8.0fms\n", stage, float64(time.Since(tSel).Milliseconds()))
		}
	}
	var seeds []grove.SymbolRecord
	var textMerge textMergeResult
	// contentOnlySeeds marks seeds whose only claim is that a term appears
	// somewhere in their BODY (RawText), not in their name — a license
	// header, a doc comment, an incidental mention. They stay seeds (the
	// mention may matter) but must not earn full source windows: measured
	// 2026-09-02 (BACKLOG addendum #7, upai2v1g #93), a 22.5kB delivery
	// spent most of its budget dumping the full license-headed body of a
	// file whose only connection to the task was a content match, and
	// nothing it returned was ever used. Demoted to signature disclosure
	// at delivery.
	contentOnlySeeds := map[string]bool{}

	if len(p.terms) > 0 {
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
		seenTermSeeds := map[string]bool{}
		perTermSeeds := make([][]grove.SymbolRecord, 0, len(p.terms))
		for _, term := range p.terms {
			// Honor --limit here too. This path hardcoded 10, so
			// `--limit 50 --terms Foo` silently capped at 10 per term — and
			// terms is the RECOMMENDED entry point in prism's own guidance.
			perTerm := p.limit
			if perTerm <= 0 {
				perTerm = 10
			}
			matches, err := h.Grove.SearchSymbols(ctx, term, perTerm)
			if err != nil {
				continue
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
				case nameContains && isTestFilePath(m.FilePath) && nameLower != termLower && qualLower != termLower:
					// A test whose name merely CONTAINS the term as a
					// substring -- the common case for any TestFoo-style
					// naming convention, which trivially contains "Foo" --
					// is not a deliberate ask the way an EXACT name match
					// is (terms=["TestFoo"] still seeds normally below).
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
						contentOnlySeeds[m.ID] = true
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
			var termSeeds []grove.SymbolRecord
			for _, m := range append(nameHits, contentHits...) {
				if !seenTermSeeds[m.ID] {
					seenTermSeeds[m.ID] = true
					termSeeds = append(termSeeds, m)
				}
			}
			perTermSeeds = append(perTermSeeds, termSeeds)
		}
		for i := 0; ; i++ {
			any := false
			for _, ts := range perTermSeeds {
				if i < len(ts) {
					seeds = append(seeds, ts[i])
					any = true
				}
			}
			if !any {
				break
			}
		}
		// Mined terms (identifiers lifted from the task text) seed strictly
		// AFTER everything the caller asked for: they only shape the
		// selection when explicit terms are weak, so a caller with good
		// terms loses nothing. Measured motivation: realistic-terms gold
		// recall is 0.31 vs 0.49 with oracle terms — the gap IS term
		// quality, and the task text usually names the fix region
		// (issue titles carry the type/method being discussed).
		for _, term := range p.minedTerms {
			matches, err := h.Grove.SearchSymbols(ctx, term, 5)
			if err != nil {
				continue
			}
			tl := strings.ToLower(term)
			for _, m := range matches {
				if !strings.Contains(strings.ToLower(m.Name), tl) &&
					!strings.Contains(strings.ToLower(m.QualifiedName), tl) {
					continue
				}
				if !seenTermSeeds[m.ID] {
					seenTermSeeds[m.ID] = true
					seeds = append(seeds, m)
				}
			}
		}
		seeds = filterGeneratedPrismContext(seeds)

		// Full-text merge: run the real grep (rg/grep/native) for the same
		// terms. Hits inside indexed symbols promote or confirm seeds; hits
		// the graph cannot see (comments, configs, docs) are delivered raw.
		// This is why the agent needs no separate grep tool.
		seededIDs := make(map[string]bool, len(seeds))
		for _, s := range seeds {
			seededIDs[s.ID] = true
		}
		textMerge = h.mergeTextSearch(ctx, p.terms, seededIDs)
		if len(textMerge.extraSeeds) > 0 {
			extra := filterGeneratedPrismContext(textMerge.extraSeeds)
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
				if isTestFilePath(s.FilePath) {
					continue
				}
				// extraSeeds are content-driven BY CONSTRUCTION (a grep hit
				// inside the symbol's body promoted it) — unless the symbol
				// also name-matches a term, it gets the same signature-level
				// demotion as SearchSymbols content hits. This was the path
				// the upai2v1g #93 license-header dump actually took.
				if !seedNameMatchesAnyTerm(s, p.terms) {
					contentOnlySeeds[s.ID] = true
				}
				kept = append(kept, s)
			}
			seeds = append(seeds, kept...)
		}
		if len(textMerge.confirmed) > 0 {
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
			if len(perTermSeeds) == 1 {
				term := ""
				if len(p.terms) == 1 {
					term = strings.ToLower(p.terms[0])
				}
				seeds = promoteSingleTermSeeds(seeds, textMerge.confirmed, term)
			} else {
				roundOf := make(map[string]int, len(seeds))
				for i, s := range seeds {
					roundOf[s.ID] = i // interleave emitted round-major order
				}
				sort.SliceStable(seeds, func(i, j int) bool {
					ri, rj := roundOf[seeds[i].ID]/len(perTermSeeds),
						roundOf[seeds[j].ID]/len(perTermSeeds)
					if ri != rj {
						return ri < rj
					}
					ci, cj := textMerge.confirmed[seeds[i].ID], textMerge.confirmed[seeds[j].ID]
					if ci != cj {
						return ci
					}
					return roundOf[seeds[i].ID] < roundOf[seeds[j].ID]
				})
			}
		}
		stamp("text-merge")
	} else {
		// No terms: fail closed with guidance rather than guess. This used
		// to fall back to embedding-based intent ranking; measured
		// (2026-08-01, 15 hand-verified concept queries, 5 real corpora) an
		// agent guessing ONE keyword through lexical search already wins or
		// ties that fallback in 12/15 cases, often by a wide margin — so the
		// fallback was adding an unreliable extra hop, not covering a real
        // gap. The actual fix for "I don't know any names yet" is doing what
		// the agent would do anyway: grep or prism_search a guessed term,
		// THEN call this with terms. Same discipline mason's own harness
		// already enforces (code_context requires both task and terms).
		return nil, fmt.Errorf(
			"no terms given — guess ONE keyword from the task (a class/function name fragment, " +
				"a domain term) and call this again with terms=[\"<guess>\"]. If you are not sure what " +
				"to guess, use prism_search or grep first to find an anchor, then retry with terms")
	}
	stamp("seeds")
	// Build candidates: the first interleave round seeds (distance 0), the
	// remainder are candidates. Fixed 5 breaks with more than five terms —
	// round-robin gets cut MID-ROUND and the last terms never seed at all
	// (measured 2026-08-26: two cells regressed the moment the oracle
	// started passing 8 terms). Every term contributes its best match; the
	// cap only guards against absurd term lists.
	seedCount := minInt(maxInt(5, len(p.terms)), minInt(10, len(seeds)))
	seedSyms := seeds[:seedCount]
	candidateSyms := seeds[seedCount:]

	profile := ranking.SelectProfile(profileName)
	profile = h.Weights.Apply(profile)

	// Test-relevance used to be boosted when the task string looked like it
	// was about writing tests. Ask for it with profile="code_review" or a
	// terms list that names the tests; phrasing is not a control surface.

	graphDist := make(map[string]int)
	hasTestEdgeID := make(map[string]bool)
	testFilePaths := make(map[string]bool)
	// testCallers is pointer-only (delivery.go renders locations, never
	// bodies): a verified `calls` edge from a real test file into a seed.
	// Deliberately outside the budget/disclosure pipeline entirely -- the
	// prior CategoryTest delivery path was removed (pr3493) because a
	// lexically-task-matched test with no verified relation to the anchor
	// earned a whole source window; a capped location list cannot repeat
	// that failure because it never competes for budget or renders a body.
	testCallers := make(map[string][]grove.SymbolRecord)
	const testCallersPerSeedCap = 5

	seenIDs := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seenIDs[s.ID] = true
	}
	var graphExtra []grove.SymbolRecord
	var familySyms []grove.SymbolRecord
	famSeen := make(map[string]bool)

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
				// Verified test callers, for the pointer line in
				// renderAnchorSummary -- NOT added to graphExtra/candidates,
				// see testCallers' declaration above.
				if tc, err := h.Grove.InboundCallers(ctx, seedQuery); err == nil {
					for _, caller := range tc {
						if !isVerifiedTestCaller(caller.FilePath) {
							continue
						}
						hasTestEdgeID[seed.ID] = true
						if len(testCallers[seed.ID]) < testCallersPerSeedCap {
							testCallers[seed.ID] = append(testCallers[seed.ID], caller)
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
			if r, err := h.Grove.ChangeImpact(ctx, seedQuery); err == nil && r != nil && os.Getenv("PRISM_NO_FAMILY") == "" {
				for _, fs := range r.Family {
					if len(familySyms) >= 12 {
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
						familySyms = append(familySyms, fs)
					}
				}
			}
		}
	}

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
	stamp("graph-expand")
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
		dist, inGraph := graphDist[sym.ID]
		if !inGraph {
			// Not reached by BFS: fall back to retrieval position as distance
			// proxy so semantically adjacent symbols still score above
			// unrelated ones.
			dist = 3 + (i / 10)
		}
		sv := h.Signals.Compute(ctx, sym, dist, hasTestEdgeID[sym.ID], testFilePaths[sym.FilePath])
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
		// One default, the same for every task. Pass budget= to change it.
		budget = defaultTaskBudget
	}
	picked := ranking.Select(seedSyms, candidates, budget)
	stamp("rank+budget")

	return &selection{
		picked:      picked,
		seedSyms:    seedSyms,
		familySyms:  familySyms,
		graphExtra:  graphExtra,
		seeds:       seeds,
		budget:      budget,
		textHits:    textMerge.rawHits,
		textBackend: textMerge.backend,
		testCallers:      testCallers,
		contentOnlySeeds: contentOnlySeeds,
	}, nil
}

// promoteSingleTermSeeds reorders seeds for the single-term case: two
// independent signals (symbol match + text hit) beat one, but confirmation
// must never outrank an exact match by too wide a margin. A pure function
// (no grep, no grove) so its exact/confirmed tier boundaries can be unit
// tested directly instead of through real text-search timing/ordering,
// which is NOT portable across backends -- see the git history of this
// function's tests for a CI failure (2026-09-02) caused by exactly that:
// a fixture tuned against rg's --sort path determinism broke under the
// grep fallback CI runners actually use (neither GitHub-hosted ubuntu nor
// macos images ship ripgrep).
//
// Two real, opposite-direction bugs shaped this function (2026-09-02):
//
//  1. Plain global confirmation-promotion (the original design) could push
//     an EXACT name/qualified-name match (graph rank 100) behind a mere
//     substring match (graph rank 70) purely because mergeTextSearch's
//     confirmation grep is capped (40 hits) and happened to surface the
//     substring symbol's definition lines before the exact symbol's --
//     django BaseDatabaseOperations.quote_name: geo_quote_name (5 unrelated
//     GIS methods, substring match) got confirmed and promoted ahead of
//     the real quote_name family (7 ties, exact match), which never got a
//     chance. Fix: pin exact matches ahead of confirmed-but-inexact ones.
//
//  2. That pin, applied unconditionally, is itself wrong when "exact" is a
//     common field/attribute name shared by many unrelated types rather
//     than a real declaration/override family: a2aproject/a2a-python-414,
//     term "JSONRPC" -- 23 Pydantic models each declare a boilerplate
//     `jsonrpc` field (23 "exact" ties, not one concept), and pinning them
//     all ahead buried two symbols confirmation had correctly promoted
//     (JsonRpcTransport, A2AClientJSONRPCError, each independently
//     referenced elsewhere) under the boilerplate. Oracle recall dropped
//     0.222 -> 0.111 on prism's swebench query_oracle bed before this was
//     caught by rerunning that existing, deterministic (no agent noise)
//     benchmark -- a single hand-picked repro is not enough evidence for a
//     ranking change like this one.
//
// No fixed priority order over {exact, confirmed} satisfies both cases at
// once (django needs exact-but-unconfirmed to beat confirmed-inexact;
// a2a-414 needs the opposite) -- exactTieLimit is a tuned compromise
// between "few ties = a real family, exactness is the strong signal" and
// "many ties = a name collision, confirmation is the strong signal", not a
// proof, and may need revisiting on more data.
func promoteSingleTermSeeds(seeds []grove.SymbolRecord, confirmed map[string]bool, term string) []grove.SymbolRecord {
	if len(confirmed) == 0 {
		return seeds
	}
	var exact, rest []grove.SymbolRecord
	for _, sd := range seeds {
		if term != "" && (strings.ToLower(sd.Name) == term || strings.ToLower(sd.QualifiedName) == term) {
			exact = append(exact, sd)
		} else {
			rest = append(rest, sd)
		}
	}
	const exactTieLimit = 10
	if len(exact) > exactTieLimit {
		exact, rest = nil, seeds
	}
	confirmedSyms := make([]grove.SymbolRecord, 0, len(rest))
	var unconfirmed []grove.SymbolRecord
	for _, sd := range rest {
		if confirmed[sd.ID] {
			confirmedSyms = append(confirmedSyms, sd)
		} else {
			unconfirmed = append(unconfirmed, sd)
		}
	}
	return append(append(exact, confirmedSyms...), unconfirmed...)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// seedNameMatchesAnyTerm reports whether a symbol's name or qualified name
// contains any of the caller's terms — the distinction between a seed the
// caller effectively asked for by name and one that only content-matched.
func seedNameMatchesAnyTerm(s grove.SymbolRecord, terms []string) bool {
	nameLower, qualLower := strings.ToLower(s.Name), strings.ToLower(s.QualifiedName)
	pathLower := strings.ToLower(s.FilePath)
	for _, t := range terms {
		tl := strings.ToLower(t)
		if tl != "" && (strings.Contains(nameLower, tl) || strings.Contains(qualLower, tl) ||
			strings.Contains(pathLower, tl)) {
			return true
		}
	}
	return false
}
