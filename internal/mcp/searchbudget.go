package mcp

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/textsearch"
)

// Default search response budget.
//
// Measured 2026-09-26 (17 heavy benchmark cells, v0.83.x): 38% of the prism
// arm's extra tokens were large search results carried in context for every
// later turn. A default search returned 11-18k chars: +/-2 context lines for
// every hit (test files included) and up to five enclosing bodies, often of
// test functions or callers rather than the target. Native Grep returned
// 0.1-3.5k for the same questions. The cheap cells answered from one batched
// lookup and went straight to Edit.
//
// So, when the caller left the shape to Prism (no context=, include_bodies=,
// max_results=, exhaustive, files_only or rollup_only), a search answers in a
// bounded shape:
//   - a term with more than searchBudgetContextHits hits prints bare match
//     lines (no context), at most searchBudgetTermLines lines, with an
//     explicit count of the rest;
//   - hits in test files collapse to per-file counts unless the request
//     targets tests (test paths/globs or test-ish terms);
//   - every matching file stays named (the v0.83.3 file inventory), and a
//     file with no displayed line gets its single best match line while the
//     inventory is small (searchBudgetBestLineFiles);
//   - at most one bounded enclosing body: the top-ranked non-test match;
//   - the whole rendered answer targets searchBudgetTokens.
//
// Explicit arguments always override this default.
const (
	searchBudgetContextHits   = 8
	searchBudgetTermLines     = 25
	searchBudgetBestLineFiles = 40
	searchBudgetBestLineChars = 120
	searchBudgetBestLineShort = 72
	searchBudgetRollupLines   = 5
	searchBudgetBodyLines     = 60
	searchBudgetTestFileList  = 20
	searchBudgetSymbolFloor   = 8
	searchBudgetBestLineWait  = 2 * time.Second
)

// searchBudgetTokens is the rendered-answer target: ~1.2k tokens (~4.8k
// chars) for one term, +300 per extra term, at most 2k tokens (~8k chars).
func searchBudgetTokens(terms int) int {
	if terms < 1 {
		terms = 1
	}
	return minInt(1200+300*(terms-1), 2000)
}

// searchBudget is the per-call decision: whether the default budget applies
// and whether the request is about tests.
type searchBudget struct {
	on             bool
	testsTargeted  bool
	regex          bool
	sc             searchScope
	explicitBodies bool
}

var testishTerm = regexp.MustCompile(`Test|(^|[^A-Za-z])test|_test|[Aa]ssert|[Mm]ock|[Ff]ixture|\bspec\b`)

// searchTargetsTests reports whether a search asks about tests: a path or
// glob with a test-shaped segment, or a term that names test code.
func searchTargetsTests(sc searchScope, terms []string) bool {
	for _, p := range append(append([]string{}, sc.paths...), sc.glob...) {
		p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
		if isTestFilePath(p) {
			return true
		}
		for _, seg := range strings.Split(p, "/") {
			seg = strings.Trim(seg, "*?[]{}!")
			if seg == "" {
				continue
			}
			if strings.HasPrefix(seg, "test") || strings.HasSuffix(seg, "test") || strings.HasSuffix(seg, "tests") ||
				strings.Contains(seg, "_test") || strings.Contains(seg, ".test") || strings.Contains(seg, "test.") ||
				strings.Contains(seg, "tests.") || seg == "spec" || seg == "specs" || strings.Contains(seg, ".spec") ||
				seg == "__tests__" {
				return true
			}
		}
	}
	for _, t := range terms {
		if testishTerm.MatchString(t) {
			return true
		}
	}
	return false
}

// applySearchBudget bounds a default search answer in place. out is either a
// flat single-term result or a batch with "results".
func (h *Handler) applySearchBudget(ctx context.Context, out map[string]any, queries []string, b searchBudget) {
	results := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		results = batch
	}
	terms := make([]string, len(results))
	retrieved := make([][]map[string]any, len(results))
	for i, r := range results {
		terms[i], _ = r["query"].(string)
		if terms[i] == "" && len(results) == 1 && len(queries) > 0 {
			terms[i] = queries[0]
		}
		retrieved[i] = budgetTermHits(r, b.testsTargeted)
		budgetRollup(r)
	}
	tokens := searchBudgetTokens(len(results))
	if b.explicitBodies {
		tokens = 4000 // explicit include_bodies=true keeps the wider shape
	}
	fitTextPresentation(out, tokens, true)
	if h.attachInventoryBestLines(ctx, results, terms, retrieved, b) {
		fitTextPresentation(out, tokens, true)
		h.attachInventoryBestLines(ctx, results, terms, retrieved, b)
	}
	trimBestLinesToFit(out, results, tokens)
	fitSymbolPresentation(out, tokens, true)
}

// searchBudgetBestLineFloor is how many inventory best lines a term keeps
// however tight the budget: the densest production files first.
const searchBudgetBestLineFloor = 10

// trimBestLinesToFit drops inventory best lines, least useful first
// (non-source files, then tests, then fewest hits), until the answer fits
// or every term is down to searchBudgetBestLineFloor lines. The files stay
// named with their counts.
func trimBestLinesToFit(out map[string]any, results []map[string]any, tokens int) {
	fits := func() bool {
		text, ok := renderSearchAsText(out)
		return !ok || ranking.EstimateTokens(text) <= tokens
	}
	if fits() {
		return
	}
	type entry struct {
		result int
		e      map[string]any
		rank   int
	}
	var all []entry
	perTerm := make([]int, len(results))
	for ri, r := range results {
		inv, ok := r["fileInventory"].(map[string]any)
		if !ok {
			continue
		}
		for _, raw := range anySlice(inv["files"]) {
			e, ok := raw.(map[string]any)
			if !ok || e["line"] == nil {
				continue
			}
			file, _ := e["file"].(string)
			// Keep behavior first: source files, then lines that branch on
			// the term, then denser files.
			rank := minInt(intArg(e, "hits", 0), 999)
			rank += (bestLineScore(fmt.Sprint(e["text"])) + 8) * 1000
			switch {
			case isProductionSourcePath(file):
				rank += 1 << 20
			case isTestFilePath(file):
				rank += 1 << 18
			}
			all = append(all, entry{ri, e, rank})
			perTerm[ri]++
		}
	}
	// Shorter lines before fewer lines.
	for _, item := range all {
		if text := fmt.Sprint(item.e["text"]); len(text) > searchBudgetBestLineShort {
			item.e["text"] = trimLineTo(text, searchBudgetBestLineShort)
		}
	}
	if fits() {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	for _, item := range all {
		if perTerm[item.result] <= searchBudgetBestLineFloor {
			continue
		}
		delete(item.e, "line")
		delete(item.e, "text")
		perTerm[item.result]--
		if fits() {
			return
		}
	}
}

// budgetTermHits applies the per-term line rules: bare lines past
// searchBudgetContextHits hits, test-file hits collapsed to counts, and at
// most searchBudgetTermLines lines with the rest counted.
func budgetTermHits(r map[string]any, testsTargeted bool) []map[string]any {
	groups := anySlice(r["textHits"])
	if len(groups) == 0 {
		return nil
	}
	var all []map[string]any
	for _, raw := range groups {
		if g, ok := raw.(map[string]any); ok {
			file, _ := g["file"].(string)
			for _, hraw := range anySlice(g["hits"]) {
				if hit, ok := hraw.(map[string]any); ok && file != "" {
					all = append(all, map[string]any{"file": file, "line": hit["line"], "text": hit["text"]})
				}
			}
		}
	}
	total := len(all)
	if t, ok := r["totalHits"].(int); ok && t > total {
		total = t
	}
	if total <= searchBudgetContextHits {
		return all
	}
	if inv, ok := r["fileInventory"].(map[string]any); ok && intArg(inv, "total", 0) <= searchBudgetBestLineFiles &&
		len(anySlice(inv["files"])) == intArg(inv, "total", 0) && !sampleCoversInventory(all, inv) {
		// Sampled lines plus a small inventory: one line per file carries
		// the same information once. Every file gets its best match line in
		// the inventory (attachInventoryBestLines) and the sample is dropped.
		var notes []any
		for _, raw := range groups {
			if g, ok := raw.(map[string]any); ok && g["file"] == nil {
				if note, _ := g["note"].(string); strings.Contains(note, "more files") {
					continue // the inventory names those files
				}
				notes = append(notes, g)
			}
		}
		r["textHits"] = notes
		inv["bestLines"] = true
		return all
	}
	var kept []any
	var tests []textsearch.FileCount
	for _, raw := range groups {
		g, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		file, _ := g["file"].(string)
		if file != "" && !testsTargeted && isTestFilePath(file) {
			if c := textHitsFileCounts([]any{g}); len(c) > 0 {
				tests = append(tests, c...)
			}
			continue
		}
		for _, hraw := range anySlice(g["hits"]) {
			if hit, ok := hraw.(map[string]any); ok {
				delete(hit, "before")
				delete(hit, "after")
			}
		}
		kept = append(kept, g)
	}
	if len(tests) > 0 && !hasFileGroup(kept) {
		// Every match is in a test file: the lines are the answer.
		return budgetTermHitsKeepTests(r, groups, all)
	}
	if len(tests) > 0 {
		counts := make([]map[string]any, 0, len(tests))
		for _, c := range tests {
			counts = append(counts, map[string]any{"file": c.File, "hits": c.Count})
		}
		r["testFileCounts"] = counts
	}
	// Per-term line cap: one line per file first (rank order), then second
	// lines, so a dense file cannot hide the others.
	type ref struct{ group, hit int }
	var order []ref
	for pass := 0; ; pass++ {
		added := false
		for gi, raw := range kept {
			g, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if pass < len(anySlice(g["hits"])) {
				order = append(order, ref{gi, pass})
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(order) > searchBudgetTermLines {
		keep := map[int]map[int]bool{}
		for _, item := range order[:searchBudgetTermLines] {
			if keep[item.group] == nil {
				keep[item.group] = map[int]bool{}
			}
			keep[item.group][item.hit] = true
		}
		var trimmed []any
		for gi, raw := range kept {
			g, ok := raw.(map[string]any)
			if !ok {
				trimmed = append(trimmed, raw)
				continue
			}
			hits := anySlice(g["hits"])
			if len(hits) == 0 {
				trimmed = append(trimmed, g)
				continue
			}
			var sel []any
			for hi, hraw := range hits {
				if keep[gi][hi] {
					sel = append(sel, hraw)
				}
			}
			if len(sel) == 0 {
				continue
			}
			c := make(map[string]any, len(g))
			for k, v := range g {
				c[k] = v
			}
			c["hits"] = sel
			trimmed = append(trimmed, c)
		}
		kept = trimmed
		r["budgetHidden"] = len(order) - searchBudgetTermLines
	}
	r["textHits"] = kept
	return all
}

func hasFileGroup(groups []any) bool {
	for _, raw := range groups {
		if g, ok := raw.(map[string]any); ok && g["file"] != nil {
			return true
		}
	}
	return false
}

// budgetTermHitsKeepTests is budgetTermHits for a term whose matches are all
// in test files: bare lines, no collapse.
func budgetTermHitsKeepTests(r map[string]any, groups []any, all []map[string]any) []map[string]any {
	r["textHits"] = groups
	budgetTermHits(r, true)
	return all
}

// sampleCoversInventory reports whether every inventory file has a line in
// the retrieved sample (then the sample itself is the per-file view).
func sampleCoversInventory(sample []map[string]any, inv map[string]any) bool {
	has := map[string]bool{}
	for _, hit := range sample {
		file, _ := hit["file"].(string)
		has[file] = true
	}
	for _, raw := range anySlice(inv["files"]) {
		if e, ok := raw.(map[string]any); ok {
			if file, _ := e["file"].(string); !has[file] {
				return false
			}
		}
	}
	return true
}

var rollupMoreNote = regexp.MustCompile(`^\+(\d+) more symbols with (\d+) hit`)

// budgetRollup keeps the densest searchBudgetRollupLines enclosing symbols
// and folds the rest into the "+N more symbols" line.
func budgetRollup(r map[string]any) {
	entries := anySlice(r["hitRollup"])
	if len(entries) == 0 {
		return
	}
	rollupKeep := searchBudgetRollupLines
	if r["fileInventory"] != nil {
		rollupKeep = 3 // the inventory already says where the matches are
	}
	var out []any
	syms, droppedSyms, droppedHits := 0, 0, 0
	for _, raw := range entries {
		e, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if note, _ := e["note"].(string); note != "" {
			if m := rollupMoreNote.FindStringSubmatch(note); m != nil {
				n, _ := strconv.Atoi(m[1])
				hits, _ := strconv.Atoi(m[2])
				droppedSyms += n
				droppedHits += hits
				continue
			}
			out = append(out, e)
			continue
		}
		syms++
		if syms > rollupKeep {
			droppedSyms++
			droppedHits += intArg(e, "hits", 0)
			continue
		}
		out = append(out, e)
	}
	if droppedSyms > 0 {
		more := map[string]any{"note": pluralNote(droppedSyms, droppedHits)}
		// Keep the notes after the symbol lines, the more-symbols line first.
		var head, tail []any
		for _, raw := range out {
			if e, ok := raw.(map[string]any); ok && e["note"] != nil {
				tail = append(tail, raw)
			} else {
				head = append(head, raw)
			}
		}
		out = append(append(head, more), tail...)
	}
	r["hitRollup"] = out
}

// attachInventoryBestLines gives each file in a small inventory that has no
// displayed line its single best match line, so the agent can tell a
// relevant file from noise without another call. Measured (jackson pr5977):
// the fix file ObjectArraySerializer.java was named with "(7)" and no code.
// Reports whether anything was attached.
func (h *Handler) attachInventoryBestLines(ctx context.Context, results []map[string]any, terms []string, retrievedHits [][]map[string]any, b searchBudget) bool {
	attached := false
	for ri, r := range results {
		inv, ok := r["fileInventory"].(map[string]any)
		if !ok {
			continue
		}
		total, _ := inv["total"].(int)
		files := anySlice(inv["files"])
		if total > searchBudgetBestLineFiles || len(files) == 0 {
			continue
		}
		shown := map[string]bool{}
		retrieved := map[string][]map[string]any{}
		for _, raw := range anySlice(r["textHits"]) {
			if g, ok := raw.(map[string]any); ok {
				file, _ := g["file"].(string)
				if len(anySlice(g["hits"])) > 0 {
					shown[file] = true
				}
			}
		}
		for _, hit := range retrievedHits[ri] {
			file, _ := hit["file"].(string)
			retrieved[file] = append(retrieved[file], hit)
		}
		var missing []string
		var need []map[string]any
		for _, raw := range files {
			e, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := e["file"].(string)
			if shown[file] || e["line"] != nil || (!b.testsTargeted && isTestFilePath(file)) {
				if shown[file] {
					delete(e, "line")
					delete(e, "text")
				}
				continue
			}
			if hits := retrieved[file]; len(hits) > 0 {
				best := bestMatchLine(hits)
				e["line"], e["text"] = intArg(best, "line", 0), trimBestLine(fmt.Sprint(best["text"]))
				attached = true
				continue
			}
			missing = append(missing, file)
			need = append(need, e)
		}
		if len(missing) == 0 {
			continue
		}
		q := terms[ri]
		if q == "" {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, searchBudgetBestLineWait)
		res := textsearch.Search(sctx, h.Root, q, textsearch.Options{
			MaxHits: len(missing) * 20, MaxPerFile: 20, Timeout: searchBudgetBestLineWait,
			Regex: b.regex, Paths: missing,
		})
		cancel()
		byFile := map[string][]map[string]any{}
		for _, hit := range res.Hits {
			byFile[hit.File] = append(byFile[hit.File], map[string]any{"line": hit.Line, "text": hit.Text})
		}
		for i, file := range missing {
			if hits := byFile[file]; len(hits) > 0 {
				best := bestMatchLine(hits)
				need[i]["line"], need[i]["text"] = intArg(best, "line", 0), trimBestLine(fmt.Sprint(best["text"]))
				attached = true
			}
		}
	}
	return attached
}

// bestMatchLine ranks match lines by the evidence score, with a bonus for a
// branch or loop on the term (the line where behavior depends on it).
func bestMatchLine(hits []map[string]any) map[string]any {
	best, bestScore := hits[0], -1<<30
	for _, hit := range hits {
		if score := bestLineScore(fmt.Sprint(hit["text"])); score > bestScore {
			best, bestScore = hit, score
		}
	}
	return best
}

func bestLineScore(text string) int {
	text = strings.TrimSpace(text)
	score := evidenceLineScore(text)
	for _, p := range []string{"if ", "if(", "elif ", "else if", "} else if", "while ", "for ", "switch ", "case ", "unless "} {
		if strings.HasPrefix(text, p) {
			return score + 2
		}
	}
	return score
}

func trimBestLine(s string) string {
	return trimLineTo(strings.TrimSpace(strings.TrimRight(s, "\r\n")), searchBudgetBestLineChars)
}

func trimLineTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - 1
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// renderTestFileCounts prints the collapsed test-file hits of one term.
func renderTestFileCounts(b *strings.Builder, raw any, inInventory map[string]bool) bool {
	counts := anySlice(raw)
	if len(counts) == 0 {
		return true
	}
	hits := 0
	var listed []string
	for _, c := range counts {
		e, ok := c.(map[string]any)
		if !ok {
			return false
		}
		hits += intArg(e, "hits", 0)
		file, _ := e["file"].(string)
		if !inInventory[file] {
			listed = append(listed, fmt.Sprintf("%s (%v)", file, e["hits"]))
		}
	}
	fmt.Fprintf(b, "// test files: %d hit(s) in %d file(s), lines omitted (paths=<file> shows them)", hits, len(counts))
	if len(listed) == 0 {
		b.WriteString("; named in the file list below\n")
		return true
	}
	sort.Strings(listed)
	b.WriteString(":\n")
	lastDir := "\x00"
	for i, item := range listed {
		if i >= searchBudgetTestFileList {
			fmt.Fprintf(b, "  +%d more test files\n", len(listed)-i)
			break
		}
		dir := path.Dir(item)
		if dir != lastDir {
			b.WriteString(dir + "/\n")
			lastDir = dir
		}
		fmt.Fprintf(b, "  %s\n", path.Base(item))
	}
	return true
}

// narrowReadHint names the read that fetches exactly the omitted part of a
// span [from,to] when [shownFrom,shownTo] was delivered, each side clamped to
// one compact read window.
func narrowReadHint(file string, from, to, shownFrom, shownTo int) string {
	clamp := func(lo, hi int) (int, int) {
		if hi-lo+1 > compactReadLimit {
			hi = lo + compactReadLimit - 1
		}
		return lo, hi
	}
	var sides [][2]int
	if shownFrom > from {
		lo, hi := clamp(from, shownFrom-1)
		sides = append(sides, [2]int{lo, hi})
	}
	if shownTo < to {
		lo, hi := clamp(shownTo+1, to)
		sides = append(sides, [2]int{lo, hi})
	}
	switch len(sides) {
	case 0:
		return "nothing omitted"
	case 1:
		return fmt.Sprintf("rest: op=read file=%q from=%d to=%d", file, sides[0][0], sides[0][1])
	}
	return fmt.Sprintf("rest: op=read ranges=[{file:%q,from:%d,to:%d},{file:%q,from:%d,to:%d}]",
		file, sides[0][0], sides[0][1], file, sides[1][0], sides[1][1])
}

// searchBudgetFor decides whether the default response budget applies: only
// when the caller left the shape to Prism. args are the normalized legacy
// prism_search fields.
func searchBudgetFor(args map[string]any, sc searchScope, queries []string, regex bool) searchBudget {
	_, explicitContext := args["context"]
	_, explicitLimit := args["limit"]
	return searchBudget{
		on:             !explicitContext && !explicitLimit && !sc.exhaustive && !sc.filesOnly && !sc.rollupOnly,
		testsTargeted:  searchTargetsTests(sc, queries),
		regex:          regex,
		sc:             sc,
		explicitBodies: args["include_bodies"] == true,
	}
}

// searchBodies delivers the enclosing source for a search result: one
// bounded non-test body under the default budget, the wider explicit
// include_bodies=true shape otherwise.
func (h *Handler) searchBodies(ctx context.Context, out map[string]any, b searchBudget) string {
	// Measured on 465 attached bodies (sessions 2026-09-20..10-05): agents
	// edited from the body 18% of the time when search listed one symbol,
	// 7% with 2-5 and 2% with 6+, while re-fetching rose from 3% to 20%.
	// With several candidates the one picked body was usually not the one
	// wanted (a class name delivered its constructor; `func walk` delivered
	// walkXFF), so the answer stays a locator. An explicit
	// include_bodies=true still gets bodies.
	if !b.explicitBodies {
		if n := listedSymbolCount(out); n >= 2 {
			return fmt.Sprintf("\n// No source attached: %d symbols match. Use op=lookup with the one you mean (qualified name if several share it).\n", n)
		}
	}
	if b.on && !b.explicitBodies {
		return h.compactSearchBodiesBudgeted(ctx, out, b.testsTargeted)
	}
	return h.compactSearchBodiesEnclosing(ctx, out)
}

// budgetPickFits reports whether a small-result body picked by the compact
// renderer can serve as the default budget's single body.
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

// budgetSampleWarning is the one-line sampling notice of the default budget.
func budgetSampleWarning(r textsearch.Result, inventory bool) string {
	if inventory {
		return fmt.Sprintf("SAMPLE of %s matches in %d %s; every matching file is listed below with its hit count. "+
			"exhaustive=true lists every line; paths=<file> shows one file",
			textMatchCount(r, false), r.FilesMatched, textMatchFileWord(r.FilesMatched))
	}
	return fmt.Sprintf("SAMPLE: showing %d of %s matches in %d %s. exhaustive=true lists every line; paths=/glob= narrows",
		len(r.Hits), textMatchCount(r, false), r.FilesMatched, textMatchFileWord(r.FilesMatched))
}

// listedSymbolCount counts the symbol matches a search answer lists, across
// every term of a batch.
func listedSymbolCount(out map[string]any) int {
	groups := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		groups = batch
	}
	n := 0
	for _, group := range groups {
		n += len(anySlice(group["symbols"]))
	}
	return n
}
