package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/sensitive"
	"github.com/provasign/prism/internal/textsearch"
)

// searchTermCap bounds how many terms one prism_search call will run. The
// point of batching is to collapse a run of turns, not to let one result
// become the payload that dominates every later turn (cache reads compound:
// a result at turn 3 of 28 is paid ~25 times). Ten covers every observed run
// in the A/B — the longest was 10 — and anything past it is a different kind
// of request that should be narrowed, not widened.
const searchTermCap = 10

// searchContextCap bounds context= lines per hit. Unbounded, an agent
// pairing a large context with exhaustive=true (or a wide path/no path) can
// turn one call into the token cost of reading whole files hit-by-hit --
// measured 2026-08-30: a context=30 call over a whole source file inside an
// already-expensive debugging loop, on a task where the extra payload did
// not shorten the loop.
//
// Tightened 30 -> 15 the same day, by binary search over 75 real context=
// requests mined from e2e sessions: 15-19 are behaviorally identical (no
// request in that range all day), so 15 sits at the true edge of normal
// usage -- 86% of all requests already fall at or under it. Every one of
// the 10 requests this tightening newly clamps (20,20,22,25,30x4,40x2) is
// the SAME misuse pattern: a single named function/class captured by
// guessing a context= large enough, rather than prism_lookup(name) -- the
// exact case prism_lookup's routing fix (same day) targets directly. Past
// 15, the caller wants prism_lookup for one symbol or prism_read for a
// file, not more context.
const searchContextCap = 15

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func (h *Handler) toolSearch(ctx context.Context, args map[string]any) (any, error) {
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
	var pathNote string
	if routed, note, listing, ok := h.pathScopeTerms(args); ok {
		// A term like "sigstore-go/" names a directory: scope the search
		// to it instead of searching the literal text.
		if routed == nil {
			return map[string]any{"linkedSearch": listing}, nil
		}
		args, pathNote = routed, note
	}
	if repo := stringArg(args, "repo", ""); repo != "" && repo != "all" {
		roots, err := h.linkedSearchRoots(repo)
		if err != nil {
			return nil, err
		}
		listing := h.linkedSearchListing(ctx, roots, args)
		if pathNote != "" {
			listing = "// " + pathNote + "\n" + listing
		}
		return map[string]any{"linkedSearch": listing}, nil
	} else if repo == "all" {
		if _, err := h.linkedSearchRoots(repo); err != nil {
			return nil, err
		}
	}
	// A caller label is metadata, never a retrieval term or scope override.
	if task, present := args["task"]; present {
		if _, ok := task.(string); !ok {
			return nil, errors.New("task must be a string label; query supplies the search terms")
		}
	}
	queries := stringsArg(args, "query")
	if len(queries) == 0 {
		// The schema declares query required; without this an empty string
		// returned an arbitrary slice of the index as if it were a result.
		return nil, errors.New("query is required (a name, a name fragment, or an array of them)")
	}
	termNote := pathNote
	if len(queries) > searchTermCap {
		// Never narrow the request quietly: a note at the end of a long
		// answer was missed (BUG-09). Refuse and say how to split it.
		return nil, fmt.Errorf("search takes up to %d terms; got %d. Split into two calls", searchTermCap, len(queries))
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
		return nil, fmt.Errorf("invalid scope %q; use text, symbols, or both", scope)
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

	// Multi-term: run each term through the same single-term path and group
	// the results under the term that produced them, so an agent can tell
	// which hit answered which question. One term keeps the flat shape
	// verbatim — every existing caller and test sees no change.
	if len(queries) > 1 {
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
		if termNote != "" {
			out["note"] = termNote
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
		h.attachSearchBodies(ctx, out, shape, budget)
		h.fitSearchAnswer(ctx, out, queries, shape, budget)
		if !regex && !sc.exhaustive && !sc.filesOnly && !sc.rollupOnly {
			h.appendBatchedSearchFallback(ctx, out, results, scope, sc, limit)
		}
		return out, nil
	}

	out, err := h.searchOne(ctx, queries[0], scope, limit, regex, sc)
	if err != nil {
		return nil, err
	}
	out["root"] = h.Root
	if disclosure := searchScopeDisclosure(scope, sc); disclosure != "" {
		out["scopeNote"] = disclosure
	}
	if termNote != "" {
		out["note"] = termNote
	}
	if h.attachScopeFilterNote(out, sc, searchResultEmpty(out)) {
		// The filter selected no file: no term was tested, so neither the
		// retry-the-terms guidance nor the token fallback applies.
	} else if searchResultEmpty(out) && !searchResultPartial(out) {
		h.attachEmptySearchGuidance(ctx, out, queries, sc)
		if !regex && !sc.exhaustive && !sc.filesOnly && !sc.rollupOnly {
			selected, omitted := searchFallbackTerms(queries[0])
			if len(selected) > 0 {
				fallbackScope := sc
				fallbackScope.context = 0
				fallbackScope.adaptive = false
				fallback := make([]map[string]any, 0, len(selected))
				for _, term := range selected {
					r, err := h.searchOne(ctx, term, scope, minInt(limit, 2), false, fallbackScope)
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
		}
	}
	// The flat single-term shape carries no "query"; body selection needs
	// the term to recognise the definition of a named symbol.
	h.attachSearchBodies(ctx, out, shape, budget, queries[0])
	h.fitSearchAnswer(ctx, out, queries, shape, budget)
	return out, nil
}

// withSearchQuery returns a shallow copy of a flat single-term search result
// labeled with its term, leaving the delivered shape untouched.
func withSearchQuery(out map[string]any, q string) map[string]any {
	if _, batched := out["results"]; batched {
		return out
	}
	if _, has := out["query"]; has {
		return out
	}
	labeled := make(map[string]any, len(out)+1)
	for k, v := range out {
		labeled[k] = v
	}
	labeled["query"] = q
	return labeled
}

// tokenFallbackTerms picks a few distinct code-like words from a failed
// literal phrase. The fallback is separately labeled and never replaces the
// original search or silently expands its path/glob filters.
func tokenFallbackTerms(q string) (selected, omitted []string) {
	words := strings.Fields(q)
	if len(words) < 2 || len(q) > 160 {
		return nil, nil
	}
	type candidate struct {
		term  string
		score int
	}
	var candidates []candidate
	seen := map[string]bool{}
	for i, word := range words {
		term := strings.Trim(word, "`'\"()[]{}.,:;!?")
		lower := strings.ToLower(term)
		if seen[lower] {
			continue
		}
		if len(term) < 3 || strings.ContainsAny(term, "/\\") {
			if term != "" {
				omitted = append(omitted, term)
			}
			continue
		}
		switch lower {
		case "class", "def", "func", "function", "method", "interface", "struct", "with", "for", "the", "and", "from":
			omitted = append(omitted, term)
			continue
		}
		seen[lower] = true
		score := minInt(len(term)/3, 4)
		if term != lower || strings.ContainsAny(term, "._") {
			score += 4
		}
		if i == len(words)-1 {
			score += 3
		}
		candidates = append(candidates, candidate{term: term, score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	for i, item := range candidates {
		if i < 3 {
			selected = append(selected, item.term)
		} else {
			omitted = append(omitted, item.term)
		}
	}
	return selected, omitted
}

func genericSyntaxSearch(q string) bool {
	q = strings.Join(strings.Fields(strings.TrimSpace(q)), " ")
	switch q {
	case "func (", "func(", "def", "class", "function", "public", "private":
		return true
	}
	return false
}

// searchResultEmpty reports whether one searchOne result carries no hits of
// any shape (symbols, text hits, or files).
// attachScopeFilterNote puts a path=/glob= filter miss at the top of a search
// result and reports whether the filters selected no file at all.
func (h *Handler) attachScopeFilterNote(out map[string]any, sc searchScope, empty bool) bool {
	rep := scopeFilterCheck(h.Root, sc.paths, sc.glob, empty)
	if rep.note == "" {
		return false
	}
	if existing, _ := out["scopeNote"].(string); existing != "" {
		out["scopeNote"] = rep.note + "; " + existing
	} else {
		out["scopeNote"] = rep.note
	}
	return rep.noFiles
}

func searchResultEmpty(m map[string]any) bool {
	if m == nil {
		return true
	}
	return len(anySlice(m["symbols"])) == 0 &&
		len(anySlice(m["textHits"])) == 0 &&
		len(anySlice(m["files"])) == 0
}

// attachEmptySearchGuidance annotates an all-empty search with what to do
// NEXT, at the moment it matters -- the same in-band-guidance pattern as the
// truncation warning's hitRollup pointer.
//
// Why: transcript analysis (2026-09-02, grove__b40b72d94e wide-bed cell)
// found the exact moment an agent abandoned prism for the rest of a task --
// one search where every guessed term (punctuation-laden call patterns like
// "e.Query(") came back empty. It never called prism again; 7 more files
// were then found by manual grep at ~2x the turns. The empty result was
// honest but terminal: nothing in it distinguished "these exact strings
// don't exist -- broaden and retry" from "this tool can't help here". Terms
// that HAD matched minutes earlier were never retried.
//
// The annotation does two things: says explicitly that empty means the
// TERMS missed (not that the tool is exhausted), and -- when the symbol
// index has near-miss candidates for the identifier tokens inside the
// failed terms -- lists them, so the retry is one obvious step instead of
// a fresh guess.
func (h *Handler) attachEmptySearchGuidance(ctx context.Context, out map[string]any, terms []string, sc searchScope) {
	// One line carries the completion evidence AND the retry: the
	// per-term "no matches — search completed" line is suppressed by the
	// renderer when this note is present (they said the same thing twice,
	// ~350 chars, on every empty search).
	guidance := "no matches — search completed, not truncated, not timed out in the requested scope; " +
		"NOT that the tool is done. Excluded files and unindexed symbols are not covered. Retry broader/shorter (drop punctuation and " +
		"qualifiers: \"e.Query(\" -> \"Query\") or reuse a term that matched earlier."
	// nearMissSymbols searches the whole index. On path/glob-scoped calls it
	// could suggest a test outside the requested files as if it were in scope.
	if len(sc.paths) == 0 && len(sc.glob) == 0 {
		if dym := h.nearMissSymbols(ctx, terms); len(dym) > 0 {
			out["didYouMean"] = dym
			guidance += " Closest indexed symbols below."
		}
	}
	h.hypLedger.recordEmptySearch(terms)
	if sn := h.hypLedger.scopeNote(); sn != "" {
		guidance += " " + sn
	}
	if existing, _ := out["note"].(string); existing != "" {
		out["note"] = existing + " — " + guidance
	} else {
		out["note"] = guidance
	}
}

// nearMissSymbols extracts the identifier tokens from failed search terms
// (dropping the punctuation that most often causes a zero-hit grep pattern)
// and asks the symbol index for their closest matches. Best-effort: no
// Grove, no tokens, or no matches all yield nil.
func (h *Handler) nearMissSymbols(ctx context.Context, terms []string) []string {
	if h.Grove == nil {
		return nil
	}
	ident := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	seenTok := map[string]bool{}
	seenOut := map[string]bool{}
	var out []string
	for _, t := range terms {
		var longest string
		for _, tok := range ident.FindAllString(t, -1) {
			if len(tok) > len(longest) {
				longest = tok
			}
		}
		lt := strings.ToLower(longest)
		if longest == "" || seenTok[lt] {
			continue
		}
		seenTok[lt] = true
		ms, err := h.Grove.SearchSymbols(ctx, longest, 3)
		if err != nil {
			continue
		}
		for _, m := range ms {
			q := m.QualifiedName
			if q == "" {
				q = m.Name
			}
			if q == "" || seenOut[q] {
				continue
			}
			seenOut[q] = true
			out = append(out, fmt.Sprintf("%s (%s:%d)", q, m.FilePath, m.Span.Start))
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}

// searchOne is the single-term search, unchanged in behaviour and shape from
// when prism_search took exactly one query.
type searchScope struct {
	paths      []string
	glob       []string
	filesOnly  bool
	exhaustive bool
	context    int
	rollupOnly bool
	adaptive   bool
	// contextAsked: the caller passed context= itself; keep that shape.
	contextAsked bool
	// budget: the default response budget applies (searchbudget.go); the
	// sampling warnings use their short form.
	budget bool
	// caseMode: "insensitive" (default), "sensitive", or "smart"
	// (case-sensitive when the term has an uppercase letter). Smart was the
	// default briefly and regressed the A/B: an agent's search for
	// brokenPipe missed isBrokenPipe (2026-11-22).
	caseMode string
	// word: whole-word matches only.
	word bool
	// noDefaultExcludes: also search .shale, .claude, .cursor and the
	// other agent-state directories.
	noDefaultExcludes bool
	// notSearched: default-excluded directories present at the root.
	notSearched []textsearch.ExcludedDir
	// includeSensitive: show the text of matches in sensitive files.
	includeSensitive bool
}

// withholdSensitive replaces the text of hits in sensitive files (.env,
// keys, credentials) unless asked: the match still counts and shows its
// path and line, as ripgrep would, but not the secret (SEC-03).
func withholdSensitive(r *textsearch.Result, include bool) {
	if include {
		return
	}
	for i := range r.Hits {
		if sensitive.Match(r.Hits[i].File) {
			r.Hits[i].Text = sensitive.Withheld
			r.Hits[i].Before, r.Hits[i].After = nil, nil
		}
	}
}

// caseSensitive applies the case mode to one term.
func (sc searchScope) caseSensitive(term string, regex bool) bool {
	switch sc.caseMode {
	case "sensitive":
		return true
	case "smart":
	default:
		return false
	}
	if regex {
		// \S, \W and similar escapes are not uppercase letters in the term.
		term = regexEscape.ReplaceAllString(term, "")
	}
	for _, r := range term {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

var regexEscape = regexp.MustCompile(`\\.`)

func searchScopeDisclosure(scope string, sc searchScope) string {
	var filters []string
	if len(sc.paths) > 0 {
		filters = append(filters, fmt.Sprintf("path=%q", sc.paths))
	}
	if len(sc.glob) > 0 {
		filters = append(filters, fmt.Sprintf("glob=%q", sc.glob))
	}
	var notes []string
	if len(filters) > 0 {
		notes = append(notes, "match lists restricted to "+strings.Join(filters, ", ")+"; other files were not searched")
	}
	if scope == "symbols" {
		notes = append(notes, "indexed-symbol search only; text search was not run. Use scope=text to check references")
	}
	return strings.Join(notes, "; ")
}

func (h *Handler) searchOne(ctx context.Context, q, scope string, limit int, regex bool, sc searchScope) (map[string]any, error) {
	if regex && scope != "symbols" {
		if _, err := regexp.Compile(q); err != nil {
			return map[string]any{
				"invalidPattern":  q,
				"warning":         "invalid regular expression: " + err.Error() + "; fix the pattern or use regex=false for a literal search",
				"resultsComplete": false,
			}, nil
		}
	}

	// scope="text": the agent asked for a PURE grep — exactly what rg
	// returns, no symbol search, no graph, minimal envelope. This is the
	// agent pricing its own request (measured: routing every locate through
	// the enriched path cost ~1.5× on ordinary bug fixes for zero benefit).
	if scope == "text" {
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
		return out, nil
	}

	// Grove supplies a candidate stream; Prism labels and re-tiers it before
	// capping so a test double or signature-only match cannot displace a
	// better name match from the delivered sample.
	// exhaustive=true lifts the SYMBOL cap too. Until 2026-09-06 only the
	// text pass honoured it: scope="symbols", exhaustive=true returned the
	// default 25 with no marker at all. Measured (ab_gate, grafana
	// CheckHealth): 25 of 53+ methods, the agent answered from the sample
	// as if it were the set and scored 0.73 recall. A capped answer to a
	// completeness question looks complete — so a capped symbol list now
	// carries a warning, and an exhaustive one is not capped.
	if limit <= 0 {
		// limit=-1 reached the slice below as syms[:-1] (review, 2026-09-06).
		limit = defaultSearchLimit
	}
	limit = minInt(limit, exhaustiveSymbolCap)
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
	// Merged full-text search: the same query as a literal, so a string
	// that names no symbol (an error message, a config key) still lands.
	// scope="symbols" skips it on request.
	if scope != "symbols" {
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
	if sc.filesOnly {
		// files_only means paths without lines in every scope. Until
		// v0.83.4 only scope=text honored it: a default-scope call returned
		// the symbol list, caller notes and match lines (3,036 vs 593 chars;
		// jackson pr5959 asked for test file names and got 18,246 chars).
		return filesOnlySearchResult(out, sc.exhaustive), nil
	}
	// Same structural hint as scope=text: the symbol list above says the
	// name exists, but not that changing it fans out — and the fan-out is
	// the part agents were measured never to ask for on their own.
	if len(sc.paths) == 0 && len(sc.glob) == 0 {
		if n := h.structuralNote(ctx, q); n != "" {
			out["resolvedNote"] = n
		}
	}
	return out, nil
}

// filesOnlySearchResult reduces a symbol/merged search result to its file
// paths: symbol files in rank order, then text-match files. Warnings about
// sampling and completeness are kept.
func filesOnlySearchResult(out map[string]any, exhaustive bool) map[string]any {
	seen := map[string]bool{}
	var files []string
	add := func(file string) {
		if file != "" && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	for _, raw := range anySlice(out["symbols"]) {
		if sym, ok := raw.(map[string]any); ok {
			file, _ := sym["filePath"].(string)
			add(file)
		}
	}
	for _, raw := range anySlice(out["textHits"]) {
		if g, ok := raw.(map[string]any); ok {
			file, _ := g["file"].(string)
			add(file)
			// exhaustive renders excerpts for the first textRenderFileCap
			// files only; every other file is in the trailing inventory.
			// Reading excerpt groups alone listed 10 of 31 files on h3
			// `push` (2026-09-28) under a "COMPLETE" note, and the agent
			// re-grepped for the test files it had been denied.
			for _, inv := range anySlice(g["inventory"]) {
				if e, ok := inv.(map[string]any); ok {
					file, _ := e["file"].(string)
					add(file)
				}
			}
		}
	}
	res := map[string]any{"files": files, "fileCount": len(files)}
	for _, key := range []string{"warning", "truncated", "totalHits", "filesMatched", "timedOut",
		"rejectedPaths", "symbolsTruncated", "countComplete", "resultsComplete"} {
		if v, ok := out[key]; ok {
			res[key] = v
		}
	}
	symbolsComplete := !boolArg(out, "symbolsTruncated")
	_, hadText := out["textHits"]
	textComplete := !hadText || boolArg(out, "resultsComplete") || (exhaustive && !boolArg(out, "truncated"))
	if symbolsComplete && textComplete && !boolArg(out, "timedOut") {
		res["note"] = strconv.Itoa(len(files)) + " files match — COMPLETE path inventory"
	}
	return res
}

// exhaustiveCapWarning is the in-band note when an exhaustive symbol search
// exceeds exhaustiveSymbolCap: the bound is stated, and so is the way past it.
func exhaustiveCapWarning(symCap int) string {
	return fmt.Sprintf(
		"exhaustive symbol search matched MORE than %d symbols — showing the first %d. "+
			"Narrow with path=/glob= (or a longer name) and re-run exhaustive to get every one.",
		symCap, symCap)
}

// filterSymbolsByScope applies the same path=/glob= narrowing to indexed
// symbols that textsearch applies to raw hits. Empty scope passes through.
func filterSymbolsByScope(syms []grove.SymbolRecord, sc searchScope) []grove.SymbolRecord {
	if len(sc.paths) == 0 && len(sc.glob) == 0 {
		return syms
	}
	// NOT syms[:0]: reusing the caller's backing array rewrites the very
	// slice we are filtering (caught by TestFilterSymbolsByScope — the
	// second call in one test saw data the first had clobbered).
	keep := make([]grove.SymbolRecord, 0, len(syms))
	for _, s := range syms {
		p := filepath.ToSlash(s.FilePath)
		ok := len(sc.paths) == 0
		for _, want := range sc.paths {
			w := strings.TrimSuffix(filepath.ToSlash(want), "/")
			if p == w || strings.HasPrefix(p, w+"/") {
				ok = true
				break
			}
		}
		if ok && len(sc.glob) > 0 {
			ok = false
			for _, g := range sc.glob {
				// Same glob semantics as the text pass (rg --glob): "**"
				// crosses directories, a slash-free glob matches the base name.
				if textsearch.MatchGlob(g, p) {
					ok = true
					break
				}
				if m, _ := filepath.Match(g, p); m {
					ok = true
					break
				}
			}
		}
		if ok {
			keep = append(keep, s)
		}
	}
	return keep
}

// defaultSearchLimit is prism_search's default and the value a non-positive
// limit= clamps to. exhaustiveSymbolCap bounds exhaustive=true on the
// symbol pass: a completeness answer, but one the transport can carry — a
// bigger set gets the cap plus a warning to narrow, never a silent cut.
const (
	defaultSearchLimit        = 25
	exhaustiveSymbolCap       = 2000
	searchSymbolPayloadBudget = 2000
	// defaultSearchContext: lines around each text hit when the caller sets
	// no context=. This makes the compact search result useful without a read.
	defaultSearchContext = 2
	// symbolFetchHardMax is a defensive backstop. Normal scoped searches are
	// filtered inside Grove and therefore stop after the first cap+1 fetch;
	// this bound prevents a future fallback from growing without end.
	symbolFetchHardMax = 1 << 13
)
