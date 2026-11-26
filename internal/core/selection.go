package core

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/textsearch"
)

// selectParams are the inputs to the shared retrieve→expand→rank→budget
// pipeline behind prism_query and prism_explore.
type selectParams struct {
	terms           []string
	includeSet      map[string]bool
	explicitProfile string
	limit           int
	contextUsed     int64
	model           string
	budgetArg       int // >0 is honored exactly; 0 uses the fixed default
	paths           []string
	glob            []string
}

// selection is the pipeline output: the budgeted picks plus the intermediate
// sets that response assembly needs (seeds for empty-result notes, seedSyms +
// graphExtra for coverage gaps and blast radius).
type selection struct {
	picked           []ranking.BudgetedSymbol
	seedSyms         []grove.SymbolRecord
	anchorMatchKinds map[string]string
	familySyms       []grove.SymbolRecord
	graphExtra       []grove.SymbolRecord
	seeds            []grove.SymbolRecord
	budget           int
	// testCallers maps a seed's symbol ID to its verified test callers
	// (real test files, doubles excluded) -- pointer-only, see the
	// declaration in selectionExpandGraph for why this never enters the
	// budget/disclosure pipeline.
	testCallers map[string][]grove.SymbolRecord
	// relatedTests are bounded lexical leads used only when the call graph
	// finds no verified test caller. They are deliberately separate from
	// testCallers: a similar test name is useful validation guidance, but it
	// is not proof that the test reaches an anchor.
	relatedTests []relatedTestHint
	// contentOnlySeeds: seed IDs matched only by body content, never by
	// name — delivered at signature disclosure, not full windows (see the
	// declaration in selectContext).
	contentOnlySeeds map[string]bool
	// lexicalOwners are small enclosing types for field/constant anchors.
	// They are additive source context: a feature flag or policy constant often
	// names the concept while the required behavior lives in sibling methods.
	lexicalOwners []grove.SymbolRecord
	// textHits are matches no indexed symbol encloses. contentHits preserve
	// the compact symbols-delivery behavior; symbolHits retain all matched
	// source lines so source delivery can recover those outside final windows.
	textHits    []textsearch.Hit
	contentHits []textsearch.Hit
	symbolHits  map[string][]textsearch.Hit
	textBackend string
}

// selectContext runs retrieval (term-seeded or intent-ranked), graph and test
// expansion, scoring, and budgeted selection. It is the single pipeline both
// prism_query and prism_explore deliver from; only the delivery format differs.
func (h *Handler) selectContext(ctx context.Context, p selectParams) (*selection, error) {
	// Retrieval keys on explicit terms; sizing keys on budget; ranking uses
	// verified call edges and stable retrieval order.
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
	st := &selectionState{
		p:     p,
		scope: searchScope{paths: p.paths, glob: p.glob},
		// contentOnlySeeds marks seeds whose only claim is that a term appears
		// somewhere in their BODY (RawText), not in their name — a license
		// header, a doc comment, an incidental mention. They stay seeds (the
		// mention may matter) but must not earn full source windows: measured
		// 2026-09-02 (BACKLOG addendum #7, upai2v1g #93), a 22.5kB delivery
		// spent most of its budget dumping the full license-headed body of a
		// file whose only connection to the task was a content match, and
		// nothing it returned was ever used. Demoted to signature disclosure
		// at delivery.
		contentOnlySeeds: map[string]bool{},
	}

	if len(p.terms) > 0 {
		if err := h.selectionTermSeeds(ctx, st); err != nil {
			return nil, err
		}
		stamp("text-merge")
	} else {
		// No terms: fail closed with guidance rather than guess. This used
		// to fall back to embedding-based intent ranking; measured
		// (2026-08-01, 15 hand-verified concept queries, 5 real corpora) an
		// agent guessing ONE keyword through lexical search already wins or
		// ties that fallback in 12/15 cases, often by a wide margin — so the
		// fallback was adding an unreliable extra hop, not covering a real
		// gap. The actual fix for "I don't know any names yet" is to use
		// prism_search to locate an explicit anchor, THEN call this with terms.
		return nil, fmt.Errorf(
			"no terms given — prism_query expands explicit anchors; pass known class, function, file, " +
				"or error terms. If no anchor is known, use prism_search to locate one, then retry with terms")
	}
	stamp("seeds")
	seeds := st.seeds
	// Build candidates: the first interleave round seeds (distance 0), the
	// remainder are candidates. Fixed 5 breaks with more than five terms —
	// round-robin gets cut MID-ROUND and the last terms never seed at all
	// (measured 2026-08-26: two cells regressed the moment the oracle
	// started passing 8 terms). Every term contributes its best match; the
	// cap only guards against absurd term lists.
	seedCount := minInt(maxInt(5, len(p.terms)), minInt(10, len(seeds)))
	seedSyms := seeds[:seedCount]
	st.seedSyms = seedSyms
	st.candidateSyms = seeds[seedCount:]
	lexicalOwners := h.lexicalOwnerContexts(ctx, seeds, seedSyms, p.terms)

	profile := ranking.SelectProfile(profileName)

	h.selectionExpandGraph(ctx, st)
	stamp("graph-expand")
	candidates := h.selectionCandidates(st, profile, callCfg)
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

	textMerge := st.textMerge
	return &selection{
		picked:           picked,
		seedSyms:         seedSyms,
		anchorMatchKinds: seedMatchKinds(seedSyms, p.terms),
		familySyms:       st.familySyms,
		graphExtra:       st.graphExtra,
		seeds:            seeds,
		budget:           budget,
		textHits:         textMerge.rawHits,
		contentHits:      selectedContentHits(picked, st.contentOnlySeeds, textMerge.symbolHits),
		symbolHits:       textMerge.symbolHits,
		textBackend:      textMerge.backend,
		testCallers:      st.testCallers,
		relatedTests:     st.relatedTests,
		contentOnlySeeds: st.contentOnlySeeds,
		lexicalOwners:    lexicalOwners,
	}, nil
}

type relatedTestHint struct {
	file     string
	line     int
	names    []string
	probes   []string
	score    int
	firstHit int
}

func hasAnyTestCallers(callers map[string][]grove.SymbolRecord) bool {
	for _, sites := range callers {
		if len(sites) > 0 {
			return true
		}
	}
	return false
}

// relatedTestHints recovers indirect integration-test leads without claiming
// a graph relationship. A test named missing_required_output is often the
// right validation target for missing_required_error even when it reaches the
// private formatter only through Parser::try_get_matches_from. Exact compound
// probes score strongly; otherwise a file must match two distinct identifier
// components. The result is pointer-only and tightly capped, so lexical test
// noise cannot displace production source.
func (h *Handler) relatedTestHints(ctx context.Context, terms []string, scope searchScope) []relatedTestHint {
	probes := relatedTestProbes(terms)
	if len(probes) == 0 || h.Grove == nil {
		return nil
	}
	type candidate struct {
		relatedTestHint
		seenNames  map[string]bool
		seenProbes map[string]bool
		bestWeight int
	}
	byFile := map[string]*candidate{}
	order := 0
	for _, probe := range probes {
		if err := ctx.Err(); err != nil {
			break
		}
		syms, err := h.Grove.SearchSymbols(ctx, probe.text, 64)
		if err != nil {
			continue
		}
		syms = filterSymbolsByScope(syms, scope)
		for _, sym := range syms {
			if !isVerifiedTestCaller(sym.FilePath) || !symbolNameContains(sym, probe.text) {
				continue
			}
			file := normalizePath(sym.FilePath)
			c := byFile[file]
			if c == nil {
				c = &candidate{relatedTestHint: relatedTestHint{file: file, line: sym.Span.Start, firstHit: order}, seenNames: map[string]bool{}, seenProbes: map[string]bool{}, bestWeight: probe.weight}
				if relatedTestPathAffinity(file, terms) {
					c.score += 2
				}
				byFile[file] = c
				order++
			}
			if !c.seenProbes[probe.label] {
				c.seenProbes[probe.label] = true
				c.probes = append(c.probes, probe.label)
				c.score += probe.weight
			}
			if sym.Name != "" && !c.seenNames[sym.Name] && len(c.names) < 3 {
				c.seenNames[sym.Name] = true
				c.names = append(c.names, sym.Name)
			}
			// Point at the strongest matching test symbol, not merely the
			// earliest generic "required" helper in the same file.
			if probe.weight > c.bestWeight && sym.Span.Start > 0 {
				c.line = sym.Span.Start
				c.bestWeight = probe.weight
			}
		}
	}
	var out []relatedTestHint
	for _, c := range byFile {
		// One compound match or two independent component matches. A lone
		// generic word such as "required" is too weak to steer validation.
		if c.score < 2 {
			continue
		}
		out = append(out, c.relatedTestHint)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].firstHit != out[j].firstHit {
			return out[i].firstHit < out[j].firstHit
		}
		return out[i].file < out[j].file
	})
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func relatedTestPathAffinity(file string, terms []string) bool {
	base := file
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	for _, pathPart := range identifierParts(base) {
		if len(pathPart) < 5 || pathPart == "test" || pathPart == "tests" {
			continue
		}
		for _, term := range terms {
			for _, termPart := range identifierParts(term) {
				shorter := minInt(len(pathPart), len(termPart))
				if shorter >= 5 && (strings.HasPrefix(pathPart, termPart) || strings.HasPrefix(termPart, pathPart)) {
					return true
				}
			}
		}
	}
	return false
}

type relatedTestProbe struct {
	text   string
	label  string
	weight int
}

func relatedTestProbes(terms []string) []relatedTestProbe {
	edgeNoise := map[string]bool{
		"get": true, "set": true, "from": true, "to": true, "with": true,
		"error": true, "errors": true, "err": true, "validate": true,
		"validation": true, "check": true, "make": true, "new": true,
	}
	seen := map[string]bool{}
	var compound, component []relatedTestProbe
	add := func(dst *[]relatedTestProbe, text, label string, weight int) {
		key := strings.ToLower(text)
		if len(key) < 5 || seen[key] {
			return
		}
		seen[key] = true
		*dst = append(*dst, relatedTestProbe{text: text, label: label, weight: weight})
	}
	for _, term := range terms {
		parts := identifierParts(term)
		for len(parts) > 1 && edgeNoise[parts[0]] {
			parts = parts[1:]
		}
		for len(parts) > 1 && edgeNoise[parts[len(parts)-1]] {
			parts = parts[:len(parts)-1]
		}
		if len(parts) > 1 {
			label := strings.Join(parts, "_")
			add(&compound, label, label, 2)
			add(&compound, strings.Join(parts, ""), label, 2)
		}
		for _, part := range parts {
			if len(part) >= 5 && !edgeNoise[part] {
				add(&component, part, part, 1)
			}
		}
	}
	out := append(compound, component...)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func identifierParts(s string) []string {
	runes := []rune(s)
	var parts []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			parts = append(parts, strings.ToLower(string(runes[start:end])))
		}
		start = -1
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := runes[i-1]
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower)) {
			flush(i)
			start = i
		}
	}
	flush(len(runes))
	return parts
}

func symbolNameContains(sym grove.SymbolRecord, probe string) bool {
	probe = strings.ToLower(probe)
	return strings.Contains(strings.ToLower(sym.Name), probe) || strings.Contains(strings.ToLower(sym.QualifiedName), probe)
}

const (
	lexicalOwnerMax      = 2
	lexicalOwnerMaxLines = 320
	lexicalOwnerMaxBytes = 16000
)

// lexicalOwnerContexts recovers the behavioral neighborhood around a matched
// field or constant without guessing beyond the indexed enclosing type. This
// is intentionally capped and delivered only when the normal result has room,
// so it can add recall but cannot evict named anchors.
func (h *Handler) lexicalOwnerContexts(ctx context.Context, seeds, primary []grove.SymbolRecord, terms []string) []grove.SymbolRecord {
	seen := map[string]bool{}
	fileSymbols := map[string][]grove.SymbolRecord{}
	primaryFiles := map[string]bool{}
	for _, seed := range primary {
		primaryFiles[normalizePath(seed.FilePath)] = true
	}
	var owners []grove.SymbolRecord
	for _, seed := range seeds {
		if len(owners) >= lexicalOwnerMax {
			break
		}
		if seed.ParentSymbol == "" || isTestFilePath(seed.FilePath) || !seedNameMatchesAnyTerm(seed, terms) {
			continue
		}
		switch strings.ToLower(seed.Kind) {
		case "field", "const", "constant", "property":
		default:
			continue
		}
		fileSyms, ok := fileSymbols[seed.FilePath]
		if !ok {
			var err error
			fileSyms, err = h.Grove.FileSymbols(ctx, seed.FilePath)
			if err != nil {
				continue
			}
			fileSymbols[seed.FilePath] = fileSyms
		}
		candidate := lexicalOwnerSymbol(seed, fileSyms)
		if candidate == nil || primaryFiles[normalizePath(candidate.FilePath)] || seen[candidate.ID] {
			continue
		}
		seen[candidate.ID] = true
		owners = append(owners, *candidate)
	}
	return owners
}

func lexicalOwnerSymbol(seed grove.SymbolRecord, fileSyms []grove.SymbolRecord) *grove.SymbolRecord {
	parentLeaf := leafOf(seed.ParentSymbol)
	for i := range fileSyms {
		candidate := &fileSyms[i]
		switch strings.ToLower(candidate.Kind) {
		case "class", "interface", "struct", "enum", "type":
		default:
			continue
		}
		if candidate.Name != parentLeaf && candidate.QualifiedName != seed.ParentSymbol {
			continue
		}
		if candidate.RawText == "" || len(candidate.RawText) > lexicalOwnerMaxBytes ||
			strings.Count(candidate.RawText, "\n")+1 > lexicalOwnerMaxLines {
			return nil
		}
		return candidate
	}
	return nil
}

func seedMatchKinds(seeds []grove.SymbolRecord, terms []string) map[string]string {
	kinds := make(map[string]string, len(seeds))
	for _, seed := range seeds {
		bestKind, bestTier := "unclassified", 0
		for _, term := range terms {
			kind, tier := symbolMatchTier(seed, strings.ToLower(strings.TrimSpace(term)))
			if tier > bestTier {
				bestKind, bestTier = kind, tier
			}
		}
		kinds[seed.ID] = bestKind
	}
	return kinds
}

func selectedContentHits(picked []ranking.BudgetedSymbol, contentOnly map[string]bool, bySymbol map[string][]textsearch.Hit) []textsearch.Hit {
	seen := map[string]bool{}
	var hits []textsearch.Hit
	for _, pick := range picked {
		if !contentOnly[pick.Symbol.ID] {
			continue
		}
		for _, hit := range bySymbol[pick.Symbol.ID] {
			key := hit.File + ":" + strconv.Itoa(hit.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			hits = append(hits, hit)
		}
	}
	return hits
}

// sourceSections is nil for symbols delivery. In source delivery it holds the
// FINAL per-file sections, after trimming, truncation, and cache-pointer
// substitution. Only a line present in that final output counts as delivered.
func (s *selection) deliverableTextHits(sourceSections map[string]string) []textsearch.Hit {
	if sourceSections == nil {
		hits := make([]textsearch.Hit, 0, len(s.textHits)+len(s.contentHits))
		hits = append(hits, s.textHits...)
		return append(hits, s.contentHits...)
	}
	seen := map[string]bool{}
	var hits []textsearch.Hit
	add := func(hit textsearch.Hit) {
		if sourceSectionShowsLine(sourceSections[normalizePath(hit.File)], hit) {
			return
		}
		key := hit.File + ":" + strconv.Itoa(hit.Line)
		if seen[key] {
			return
		}
		seen[key] = true
		hits = append(hits, hit)
	}
	for _, hit := range s.textHits {
		add(hit)
	}
	for _, pick := range s.picked {
		for _, hit := range s.symbolHits[pick.Symbol.ID] {
			add(hit)
		}
	}
	return hits
}

func sourceSectionShowsLine(section string, hit textsearch.Hit) bool {
	// A numbered source line may itself be clamped; in that case the matched
	// text is not actually visible and still needs its own text-hit excerpt.
	return hit.Line > 0 && strings.Contains(section, "\n"+strconv.Itoa(hit.Line)+SourceLineSep+hit.Text)
}

// interleaveUniqueTermSeeds gives every explicit term its best still-unseen
// match before taking a second match from any term. Deduplication happens here,
// after each term has ranked its own results. Deduplicating while building the
// per-term lists lets an earlier broad qualified-name match consume a later
// exact-name match (for example, "CliRunner" sees CliRunner.isolation before
// the explicit term "isolation" gets its turn), demoting the named method from
// a full seed body to a signature-only candidate.
func interleaveUniqueTermSeeds(perTermSeeds [][]grove.SymbolRecord) []grove.SymbolRecord {
	seen := map[string]bool{}
	var seeds []grove.SymbolRecord
	for i := 0; ; i++ {
		any := false
		for _, termSeeds := range perTermSeeds {
			if i >= len(termSeeds) {
				continue
			}
			any = true
			seed := termSeeds[i]
			if !seen[seed.ID] {
				seen[seed.ID] = true
				seeds = append(seeds, seed)
			}
		}
		if !any {
			return seeds
		}
	}
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

// explicitTestTerm keeps a test symbol eligible only when the caller's term
// itself expresses test intent. An exact common method name such as `skip`
// inside a test is not a deliberate test request merely because it is exact;
// explicit terms such as TestFoo, test_foo, or widget.spec remain eligible.
func explicitTestTerm(term string) bool {
	term = strings.ToLower(term)
	return strings.Contains(term, "test") || strings.Contains(term, "spec")
}
