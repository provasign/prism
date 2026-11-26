package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/textsearch"
)

// searchToolRequest is a decoded prism_search call: the terms, the scope and
// the one answer shape (searchshape.go) every stage shares.
type searchToolRequest struct {
	queries  []string
	termNote string
	limit    int
	scope    string
	regex    bool
	shape    searchShape
	sc       searchScope
	budget   searchBudget
}

// searchRouteArgs resolves linked-repository and directory-term routing. When
// done is true, the call is answered (or refused) by result and err.
func (h *Handler) searchRouteArgs(ctx context.Context, args map[string]any) (routedArgs map[string]any, pathNote string, done bool, result any, err error) {
	if id, rels, ok := h.linkedPathRepo(args); ok {
		// path=../boxo/... names a linked repository: search it there
		// instead of rejecting the path as outside the root (multi-repo
		// bench 2026-11-14: the rejection sent agents to "run prism there").
		routed := make(map[string]any, len(args)+1)
		for k, v := range args {
			routed[k] = v
		}
		routed["repo"], routed["path"] = id, rels
		args = routed
	}
	if routed, note, listing, ok := h.pathScopeTerms(args); ok {
		// A term like "sigstore-go/" names a directory: scope the search
		// to it instead of searching the literal text.
		if routed == nil {
			return nil, "", true, map[string]any{"linkedSearch": listing}, nil
		}
		args, pathNote = routed, note
	}
	if repo := stringArg(args, "repo", ""); repo != "" && repo != "all" {
		roots, err := h.linkedSearchRoots(repo)
		if err != nil {
			return nil, "", true, nil, err
		}
		listing := h.linkedSearchListing(ctx, roots, args)
		if pathNote != "" {
			listing = "// " + pathNote + "\n" + listing
		}
		return nil, "", true, map[string]any{"linkedSearch": listing}, nil
	} else if repo == "all" {
		if _, err := h.linkedSearchRoots(repo); err != nil {
			return nil, "", true, nil, err
		}
	}
	return args, pathNote, false, nil, nil
}

// searchDecodeRequest validates the arguments and fixes the answer shape.
func (h *Handler) searchDecodeRequest(args map[string]any, pathNote string) (searchToolRequest, error) {
	// A caller label is metadata, never a retrieval term or scope override.
	if task, present := args["task"]; present {
		if _, ok := task.(string); !ok {
			return searchToolRequest{}, errors.New("task must be a string label; query supplies the search terms")
		}
	}
	queries := stringsArg(args, "query")
	if len(queries) == 0 {
		// The schema declares query required; without this an empty string
		// returned an arbitrary slice of the index as if it were a result.
		return searchToolRequest{}, errors.New("query is required (a name, a name fragment, or an array of them)")
	}
	termNote := pathNote
	if len(queries) > searchTermCap {
		// Never narrow the request quietly: a note at the end of a long
		// answer was missed (BUG-09). Refuse and say how to split it.
		return searchToolRequest{}, fmt.Errorf("search takes up to %d terms; got %d. Split into two calls", searchTermCap, len(queries))
	}
	limit := intArg(args, "limit", defaultSearchLimit)
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > exhaustiveSymbolCap {
		termNote = appendNote(termNote, fmt.Sprintf("limit clamped to %d (asked for %d)", exhaustiveSymbolCap, limit))
		limit = exhaustiveSymbolCap
	}
	scope := stringArg(args, "scope", "both")
	if scope != "text" && scope != "symbols" && scope != "both" {
		return searchToolRequest{}, fmt.Errorf("invalid scope %q; use text, symbols, or both", scope)
	}
	regex := boolArg(args, "regex")
	// One decision for the whole answer: context lines, bodies, fit
	// (searchshape.go). A raised limit keeps the exact-count pass (adaptive):
	// click get_command with max_results=500 (2026-09-28) got a 5-per-file
	// sample with no completeness line and the agent grepped for the list.
	shape := searchShapeFor(args, limit)
	if note := shape.clampNote(); note != "" {
		termNote = appendNote(termNote, note)
	}
	sc := shape.scope(args)
	if !sc.noDefaultExcludes && len(sc.paths) == 0 && scope != "symbols" {
		sc.notSearched = textsearch.DefaultExcludedPresent(h.Root)
	}
	budget := shape.budgetFor(sc, queries, regex)
	if len(queries) > 1 && len(sc.paths) == 0 && len(sc.glob) == 0 {
		filtered := queries[:0]
		var skipped []string
		for _, q := range queries {
			if genericSyntaxSearch(q) {
				skipped = append(skipped, q)
				continue
			}
			filtered = append(filtered, q)
		}
		if len(filtered) > 0 && len(skipped) > 0 {
			queries = filtered
			termNote = appendNote(termNote, fmt.Sprintf(
				"skipped repository-wide syntax term(s) %q because the specific batched term supplies the declaration/call inventory; run broad syntax searches separately with path=/glob=", skipped))
		}
	}
	return searchToolRequest{queries: queries, termNote: termNote, limit: limit, scope: scope,
		regex: regex, shape: shape, sc: sc, budget: budget}, nil
}

// searchBatchedTerms runs each term through the same single-term path and
// groups the results under the term that produced them, so an agent can tell
// which hit answered which question.
func (h *Handler) searchBatchedTerms(ctx context.Context, req searchToolRequest) map[string]any {
	queries, scope, limit, regex, sc := req.queries, req.scope, req.limit, req.regex, req.sc
	perTerm := make([]map[string]any, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			r, err := h.searchOne(ctx, q, scope, limit, regex, sc)
			if err != nil {
				errs[i] = err
				return
			}
			r["query"] = q
			perTerm[i] = r
		}(i, q)
	}
	wg.Wait()
	out := map[string]any{"root": h.Root}
	if disclosure := searchScopeDisclosure(scope, sc); disclosure != "" {
		out["scopeNote"] = disclosure
	}
	results := make([]map[string]any, 0, len(queries))
	var failed []string
	for i := range queries {
		if errs[i] != nil {
			// One bad term must not lose the other nine's results.
			failed = append(failed, fmt.Sprintf("%s: %v", queries[i], errs[i]))
			continue
		}
		results = append(results, perTerm[i])
	}
	out["results"] = results
	if leads := searchLeads(results); len(leads) > 0 {
		out["searchLeads"] = leads
	} else if len(results) > 1 && !sc.filesOnly {
		out["searchLeadNote"] = "No indexed-name or cross-term source anchor; the results below are independent term matches."
	}
	if len(failed) > 0 {
		out["failedTerms"] = failed
	}
	if req.termNote != "" {
		out["note"] = req.termNote
	}
	allEmpty := len(results) > 0
	for _, r := range results {
		if !searchResultEmpty(r) {
			allEmpty = false
			break
		}
	}
	filterMiss := h.attachScopeFilterNote(out, sc, allEmpty)
	if allEmpty && !searchResultPartial(out) && !filterMiss {
		h.attachEmptySearchGuidance(ctx, out, queries, sc)
	}
	h.attachSearchBodies(ctx, out, req.shape, req.budget)
	h.fitSearchAnswer(ctx, out, queries, req.shape, req.budget)
	if !regex && !sc.exhaustive && !sc.filesOnly && !sc.rollupOnly {
		h.appendBatchedSearchFallback(ctx, out, results, scope, sc, limit)
	}
	return out
}

// searchSingleTerm answers one term in the flat shape.
func (h *Handler) searchSingleTerm(ctx context.Context, req searchToolRequest) (map[string]any, error) {
	queries, sc := req.queries, req.sc
	out, err := h.searchOne(ctx, queries[0], req.scope, req.limit, req.regex, sc)
	if err != nil {
		return nil, err
	}
	out["root"] = h.Root
	if disclosure := searchScopeDisclosure(req.scope, sc); disclosure != "" {
		out["scopeNote"] = disclosure
	}
	if req.termNote != "" {
		out["note"] = req.termNote
	}
	if h.attachScopeFilterNote(out, sc, searchResultEmpty(out)) {
		// The filter selected no file: no term was tested, so neither the
		// retry-the-terms guidance nor the token fallback applies.
	} else if searchResultEmpty(out) && !searchResultPartial(out) {
		h.attachEmptySearchGuidance(ctx, out, queries, sc)
		if !req.regex && !sc.exhaustive && !sc.filesOnly && !sc.rollupOnly {
			h.searchSingleFallback(ctx, out, req)
		}
	}
	// The flat single-term shape carries no "query"; body selection needs
	// the term to recognise the definition of a named symbol.
	h.attachSearchBodies(ctx, out, req.shape, req.budget, queries[0])
	h.fitSearchAnswer(ctx, out, queries, req.shape, req.budget)
	return out, nil
}

// searchSingleFallback attaches bounded shorter-term results to a single-term
// miss, trimming them until the rendered answer fits.
func (h *Handler) searchSingleFallback(ctx context.Context, out map[string]any, req searchToolRequest) {
	selected, omitted := searchFallbackTerms(req.queries[0])
	if len(selected) == 0 {
		return
	}
	fallbackScope := req.sc
	fallbackScope.context = 0
	fallbackScope.adaptive = false
	fallback := make([]map[string]any, 0, len(selected))
	for _, term := range selected {
		r, err := h.searchOne(ctx, term, req.scope, minInt(req.limit, 2), false, fallbackScope)
		if err != nil {
			out["fallbackFailed"] = append(anySlice(out["fallbackFailed"]), term+": "+err.Error())
			continue
		}
		r["query"] = term
		fallback = append(fallback, r)
	}
	if len(fallback) > 0 {
		out["fallbackResults"] = fallback
	}
	if len(omitted) > 0 {
		out["fallbackOmitted"] = omitted
	}
	for len(fallback) > 0 {
		text, ok := renderSearchAsText(out)
		if ok && ranking.EstimateTokens(text) <= 2000 {
			break
		}
		last := fallback[len(fallback)-1]
		out["fallbackOmitted"] = append(anySlice(out["fallbackOmitted"]), last["query"])
		fallback = fallback[:len(fallback)-1]
		out["fallbackResults"] = fallback
	}
}

// searchOneText is scope="text": a pure grep with a minimal envelope.
func (h *Handler) searchOneText(ctx context.Context, q string, limit int, regex bool, sc searchScope) map[string]any {
	opts := textsearch.Options{
		MaxHits: limit, Timeout: textSearchTimeout, Regex: regex,
		Paths: sc.paths, Glob: sc.glob, FilesOnly: sc.filesOnly,
		Exhaustive: sc.exhaustive, Context: sc.context, Adaptive: sc.adaptive,
		CaseSensitive: sc.caseSensitive(q, regex), Word: sc.word, NoDefaultExcludes: sc.noDefaultExcludes,
	}
	r := textsearch.Search(ctx, h.Root, q, opts)
	withholdSensitive(&r, sc.includeSensitive)
	out := map[string]any{
		"textHits":      h.renderedTextSearchHits(ctx, r, sc.exhaustive, sc.contextAsked),
		"textBackend":   r.Backend,
		"truncated":     r.Truncated,
		"caseSensitive": opts.CaseSensitive,
	}
	if ns := h.excludedMatches(ctx, q, opts, sc.notSearched); len(ns) > 0 {
		out["notSearched"] = ns
	}
	attachTextSearchCompleteness(out, r)
	if r.Truncated {
		// Truncation always carries a denominator. Without one the agent
		// cannot tell a complete answer from a 2% sample, and the failure
		// is silent: it reads the capped list as the whole picture.
		out["totalHits"] = r.TotalHits
		out["filesMatched"] = r.FilesMatched
		// Point at the grouped evidence without treating capped group names
		// or lower-bound counts as an exhaustive inventory.
		ru := h.hitRollup(ctx, q, sc, regex)
		if len(ru) > 0 {
			out["hitRollup"] = ru
			out["warning"] = fmt.Sprintf(
				"showing %d of %s matches across %d %s — this is a SAMPLE, not "+
					"the full set. Check the bounded hitRollup below before re-querying; "+
					"omitted groups and unprobed hits are noted. It is not a complete site inventory. "+
					"Need every raw line? exhaustive=true; narrow path=/glob= for evidence gaps.",
				len(r.Hits), textMatchCount(r, false), r.FilesMatched, textMatchFileWord(r.FilesMatched))
			if sc.rollupOnly {
				// The caller already knows the sample's raw lines are
				// not what they need this call -- "how many, and where
				// do they cluster" is fully answered by hitRollup alone
				// (real usage, 2026-09-02: paid for the sample's tokens
				// on repeated searches where only the rollup was ever
				// read). Ordering the rollup earlier in the payload
				// would not have saved anything -- deliveredTokens
				// bills the whole response regardless of read order;
				// not delivering the sample at all is the only real
				// lever. Only suppressed when a rollup actually exists
				// (nothing to answer with otherwise, and dropping
				// information silently is worse than the tokens).
				delete(out, "textHits")
			}
		} else {
			out["warning"] = fmt.Sprintf(
				"showing %d of %s matches across %d %s — this is a SAMPLE, not the "+
					"full set. Raise limit=, narrow with path=/glob=, or use files_only=true to "+
					"see the spread before drawing conclusions. Need every raw line? exhaustive=true.",
				len(r.Hits), textMatchCount(r, false), r.FilesMatched, textMatchFileWord(r.FilesMatched))
		}
	}
	if (r.Truncated || textHitsOmitFiles(out["textHits"])) && !sc.exhaustive && !sc.filesOnly {
		// The lines are a sample; the files must not be. Every matching
		// file is named with its hit count (see searchinventory.go).
		if inv := h.textFileInventory(ctx, q, r, opts); inv != nil {
			out["fileInventory"] = inv
			out["warning"] = appendNote(stringArg(out, "warning", ""), fileInventoryWarning)
		}
	}
	if sc.budget && r.Truncated {
		out["warning"] = budgetSampleWarning(r, out["fileInventory"] != nil)
	}
	if sc.filesOnly {
		// Locations without lines: the cheapest answer to "where does
		// this live". Done here rather than via rg --files-with-matches,
		// whose bare-path output breaks the shared line parser.
		seen := map[string]bool{}
		var files []string
		for _, hit := range r.Hits {
			if !seen[hit.File] {
				seen[hit.File] = true
				files = append(files, hit.File)
			}
		}
		delete(out, "textHits")
		out["fileCount"] = len(files)
		out["files"] = files
		if sc.exhaustive || r.ResultsComplete {
			out["note"] = strconv.Itoa(len(files)) + " files match — COMPLETE exact path inventory; no directory counts or path expansion required"
		}
	}
	if len(r.RejectedPaths) > 0 {
		// Never let a dropped scope pass as a completed search.
		out["rejectedPaths"] = r.RejectedPaths
		out["warning"] = fmt.Sprintf(
			"these path= entries resolve outside the project root %s and were NOT searched: %v (%s)",
			h.Root, r.RejectedPaths, h.outsideRootHint())
	}
	if r.TimedOut {
		out["timedOut"] = true
		out["warning"] = appendNote(stringArg(out, "warning", ""), textTimeoutWarning(r))
	}
	// Structural hint first: the fan-out of the symbol this term names
	// (implementations + callers with sites) outranks the noise-ratio
	// note — full38 showed missed fan-out sites, not noisy greps, are
	// where searches go wrong.
	// Structural hints are repository-wide. A path/glob filter must not
	// quietly add evidence from files outside the requested match scope.
	// files_only asked for paths alone.
	if len(sc.paths) == 0 && len(sc.glob) == 0 && !sc.filesOnly {
		if n := h.structuralNote(ctx, q); n != "" {
			out["resolvedNote"] = n
		} else if n := h.resolvedRefNote(ctx, q, r.Hits); n != "" {
			out["resolvedNote"] = n
		}
	}
	return out
}

// searchOneSymbols runs the indexed-symbol pass: a ranked, labeled, capped
// symbol list with its truncation warning. limit is already clamped.
func (h *Handler) searchOneSymbols(ctx context.Context, q string, limit int, sc searchScope) (map[string]any, error) {
	symCap := limit
	if sc.exhaustive {
		// Bounded, not unbounded: past exhaustiveSymbolCap the response
		// would exceed what the host accepts and be cut at the transport
		// with no marker at all — the exact failure this fixes, one layer
		// down. The cap is stated when hit, with the narrowing to use.
		symCap = exhaustiveSymbolCap
	}
	// path=/glob= are honored by the TEXT pass (textsearch.Options) but were
	// silently dropped by the SYMBOL pass — the agent's narrowing simply did
	// not apply to half its own result. Measured on jackson (2026-08-25): a
	// search for "anySetter" scoped to src/main/java returned 25 symbols, 8
	// of them from src/test/java, and the agent re-grepped to recover. A
	// scope the tool advertises and then ignores is worse than no scope.
	//
	// The filters run AFTER the ranked fetch, so a fixed fetch size cannot
	// know whether the in-scope set is complete (review, 2026-09-06: 2,001
	// out-of-scope matches ranked first hid 3,000 in-scope ones with no
	// marker). Fetch in growing batches until the filtered result exceeds
	// the cap or the source itself is exhausted (it returned fewer than
	// asked); only then is "not truncated" a fact.
	scanCap := symCap
	if !sc.exhaustive {
		scanCap = minInt(exhaustiveSymbolCap, maxInt(symCap*4, 64))
	}
	searchScoped := func(ctx context.Context, query string, limit int) ([]grove.SymbolRecord, error) {
		return h.Grove.SearchSymbolsScoped(ctx, query, limit, sc.paths, sc.glob)
	}
	syms, sourceExhausted, err := scopedSymbolSearch(ctx, searchScoped, q, sc, scanCap, symbolFetchHardMax)
	if err != nil {
		return nil, err
	}
	ranked, condensed := condenseSearchSymbols(rankSearchSymbols(syms, q))
	annotated := make([]map[string]any, 0, len(ranked))
	for _, item := range ranked {
		s := item.symbol
		var m map[string]any
		if b, err := json.Marshal(s); err == nil {
			_ = json.Unmarshal(b, &m)
		}
		if m == nil {
			continue
		}
		m["matchKind"] = item.matchKind
		switch searchTestLabel(s) {
		case "test double":
			m["testDouble"] = true
		case "test":
			m["testCode"] = true
		}
		annotated = append(annotated, m)
	}
	// LOCATE returns locations, not bodies. The text renderer already drops
	// rawText (v0.55.6), but the JSON payload still carried whole symbol
	// bodies — measured on jackson (2026-08-25): one `search anySetter`
	// returned 95,315 bytes of class bodies for a locate question, in a
	// language where a class body runs hundreds of lines. Python hid this;
	// Java made it the dominant cost. Bodies remain one prism_lookup away,
	// and the pointer says so.
	for _, m := range annotated {
		delete(m, "rawText")
		delete(m, "callSites")
		delete(m, "blobSha")
		delete(m, "id")
		delete(m, "imports")
	}
	// A small complete result is cheaper to deliver once than to force an
	// exhaustive follow-up solely because it crossed the default count cap.
	if sc.adaptive && sourceExhausted && !sc.exhaustive && len(annotated) > symCap {
		if preview, ok := renderSearchAsText(map[string]any{"symbols": annotated}); ok &&
			ranking.EstimateTokens(preview) <= searchSymbolPayloadBudget {
			symCap = len(annotated)
		}
	}
	moreKnown := len(annotated) > symCap
	symbolsTruncated := moreKnown || !sourceExhausted
	if moreKnown {
		annotated = annotated[:symCap]
	}
	// No "locations only" note here: the text renderer states it once
	// under the symbol list (searchtext.go); carrying it in the envelope
	// too printed two near-identical pointers on every symbol result.
	out := map[string]any{"symbols": annotated}
	if condensed != "" {
		out["condensedNote"] = condensed
	}
	if symbolsTruncated {
		out["symbolsTruncated"] = true
		out["warning"] = symbolSearchWarning(len(annotated), symCap, sc.exhaustive, sourceExhausted, moreKnown)
		if !sourceExhausted {
			out["warning"] = appendNote(out["warning"].(string), fmt.Sprintf(
				"match tiers were evaluated over the first %d in-scope candidates; later candidates may change the top ranks. Narrow query/path or use exhaustive=true", len(syms)))
		}
	}
	return out, nil
}

// searchOneMergedText adds the merged full-text pass to a symbol result.
func (h *Handler) searchOneMergedText(ctx context.Context, out map[string]any, q string, limit int, regex bool, sc searchScope) {
	// The merged pass previously ran at MaxHits 50 — double the symbol
	// limit the caller asked for, on the default scope of the highest-
	// call-count tool. The caller's limit bounds both passes now.
	opts := textsearch.Options{
		MaxHits: limit, Timeout: textSearchTimeout, Regex: regex,
		Paths: sc.paths, Glob: sc.glob, FilesOnly: sc.filesOnly,
		Exhaustive: sc.exhaustive, Context: sc.context, Adaptive: sc.adaptive,
		CaseSensitive: sc.caseSensitive(q, regex), Word: sc.word, NoDefaultExcludes: sc.noDefaultExcludes,
	}
	r := textsearch.Search(ctx, h.Root, q, opts)
	withholdSensitive(&r, sc.includeSensitive)
	out["textHits"] = h.renderedTextSearchHits(ctx, r, sc.exhaustive, sc.contextAsked)
	out["textBackend"] = r.Backend
	out["caseSensitive"] = opts.CaseSensitive
	if ns := h.excludedMatches(ctx, q, opts, sc.notSearched); len(ns) > 0 {
		out["notSearched"] = ns
	}
	attachTextSearchCompleteness(out, r)
	if r.Truncated {
		out["truncated"] = true
		out["totalHits"] = r.TotalHits
		out["filesMatched"] = r.FilesMatched
		out["warning"] = appendNote(stringArg(out, "warning", ""), fmt.Sprintf(
			"Text matches are a SAMPLE: showing %d of %s. Use exhaustive=true or narrow path=/glob=.",
			len(r.Hits), textMatchCount(r, true)))
	}
	textWarning := stringArg(out, "warning", "")
	if (r.Truncated || textHitsOmitFiles(out["textHits"])) && !sc.exhaustive && !sc.filesOnly {
		if inv := h.textFileInventory(ctx, q, r, opts); inv != nil {
			out["fileInventory"] = inv
			out["warning"] = appendNote(stringArg(out, "warning", ""), fileInventoryWarning)
		}
	}
	if sc.budget && r.Truncated {
		// Replace the long text-sampling sentences, keep the symbol ones.
		symWarning := textWarning
		if i := strings.Index(textWarning, "Text matches are a SAMPLE"); i >= 0 {
			symWarning = strings.TrimSuffix(strings.TrimSpace(textWarning[:i]), ";")
		}
		out["warning"] = appendNote(symWarning, budgetSampleWarning(r, out["fileInventory"] != nil))
	}
	if r.TimedOut {
		out["timedOut"] = true
		out["warning"] = appendNote(stringArg(out, "warning", ""), textTimeoutWarning(r))
	}
	if len(r.RejectedPaths) > 0 {
		out["rejectedPaths"] = r.RejectedPaths
		out["warning"] = appendNote(stringArg(out, "warning", ""), fmt.Sprintf("Paths outside the root %s were NOT searched: %v (%s)", h.Root, r.RejectedPaths, h.outsideRootHint()))
	}
}
