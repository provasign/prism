package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// searchEvidenceRun carries the state shared by the stages of
// compactSearchBodiesWith: decoded term groups, the source files read so far,
// the candidate sections, and the selection being built.
type searchEvidenceRun struct {
	h    *Handler
	ctx  context.Context
	out  map[string]any
	skip []grove.SymbolRecord
	opts searchBodyOptions

	groups           []map[string]any
	fallbackEvidence bool
	terms            []string

	files     map[string]*evidenceFile
	fileOrder []string

	candidates map[string]*searchEvidence
	order      []*searchEvidence

	referenceFiles     map[string]string
	fileTerms          map[string]map[int]bool
	rarest             int
	droppedDeclaration bool

	selected    []*searchEvidence
	chosen      map[*searchEvidence]bool
	seenFiles   map[string]bool
	maxSections int
}

// searchEvidenceStart decodes the term groups a search answer carries.
func searchEvidenceStart(ctx context.Context, h *Handler, out map[string]any, skip []grove.SymbolRecord, opts searchBodyOptions) *searchEvidenceRun {
	run := &searchEvidenceRun{
		h: h, ctx: ctx, out: out, skip: skip, opts: opts,
		groups:         []map[string]any{out},
		files:          map[string]*evidenceFile{},
		fileOrder:      []string{},
		candidates:     map[string]*searchEvidence{},
		referenceFiles: map[string]string{},
		rarest:         -1,
	}
	if batch, ok := out["results"].([]map[string]any); ok {
		run.groups = batch
	} else if searchResultEmpty(out) {
		// A single-term miss may already carry a bounded shorter-term fallback.
		// Treat that fallback as source evidence so the caller receives the
		// enclosing implementation in the same turn instead of having to repeat
		// discovery with grep or a second Prism call. The rendered inventory still
		// labels the exact term as a miss and the source evidence as a fallback.
		if fallback, ok := out["fallbackResults"].([]map[string]any); ok && len(fallback) > 0 {
			run.groups = fallback
			run.fallbackEvidence = true
		}
	}
	for _, group := range run.groups {
		run.terms = append(run.terms, stringArg(group, "query", ""))
	}
	return run
}

// searchEvidenceReadFile reads and caches one source file with its symbols.
func (r *searchEvidenceRun) searchEvidenceReadFile(file string) *evidenceFile {
	file = filepath.ToSlash(file)
	if entry, ok := r.files[file]; ok {
		return entry
	}
	if file == "" || len(r.files) >= 24 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(r.h.Root, filepath.FromSlash(file)))
	if err != nil || len(data) > 2<<20 {
		return nil
	}
	entry := &evidenceFile{lines: strings.Split(string(data), "\n")}
	if strings.HasSuffix(file, ".py") || strings.HasSuffix(file, ".pyi") {
		entry.docLines = pythonDocstringLines(entry.lines)
	}
	entry.symbols, _ = r.h.Grove.FileSymbols(r.ctx, file)
	r.files[file] = entry
	r.fileOrder = append(r.fileOrder, file)
	return entry
}

// searchEvidenceAdd records one hit line as evidence for its enclosing
// section, creating the section on first sight.
func (r *searchEvidenceRun) searchEvidenceAdd(file string, line, term int, related string, nameMatch bool) {
	file = filepath.ToSlash(file)
	entry := r.searchEvidenceReadFile(file)
	if entry == nil || line < 1 || line > len(entry.lines) {
		return
	}
	for _, delivered := range r.skip {
		if filepath.ToSlash(delivered.FilePath) == file && delivered.Span.Start <= line && line <= delivered.Span.End {
			return
		}
	}
	sym := tightestEnclosingSymbol(entry.symbols, line)
	key := fmt.Sprintf("%s:%d", file, line)
	if sym != nil {
		// Distant hits inside one very large method need separate windows.
		// Nearby hits still share a section and its term evidence.
		key = fmt.Sprintf("%s:%d-%d:%d", file, sym.Span.Start, sym.Span.End, (line-sym.Span.Start)/40)
	}
	candidate := r.candidates[key]
	if candidate == nil {
		candidate = &searchEvidence{file: file, terms: map[int]bool{}, related: map[string]bool{},
			relatedLines: map[string][]int{}, firstSeen: len(r.order)}
		if sym != nil {
			candidate.symbol, candidate.hasSymbol = *sym, true
		}
		r.candidates[key] = candidate
		r.order = append(r.order, candidate)
	}
	if term >= 0 {
		candidate.terms[term] = true
	}
	if related != "" {
		candidate.related[related] = true
		candidate.relatedLines[related] = append(candidate.relatedLines[related], line)
	}
	candidate.nameMatch = candidate.nameMatch || nameMatch
	if term >= 0 && sym != nil && related == "" {
		q := strings.TrimSpace(stringArg(r.groups[term], "query", ""))
		if declaresTerm(entry.lines, *sym, line, q) || nameMatch && q != "" && (sym.Name == q || sym.QualifiedName == q) {
			candidate.definition = true
		}
	}
	score := evidenceLineScore(entry.lines[line-1])
	if line-1 < len(entry.docLines) && entry.docLines[line-1] {
		// flask: `response = make_response(...)` in the helper's
		// docstring scored as an assignment-with-call and outranked
		// Flask.make_response. Docstring text scores like a comment.
		score = -3
	}
	if term >= 0 {
		score += 2
	}
	if related != "" && strings.Contains(entry.lines[line-1], "=") {
		score += 2
		if strings.HasPrefix(strings.TrimSpace(entry.lines[line-1]), related+"=") {
			score += 3 // named argument carrying the related value into a call
		}
		if parts := strings.SplitN(entry.lines[line-1], "=", 2); len(parts) == 2 && strings.Contains(parts[1], ".") {
			score += 4 // a field value passed or read, not just its declaration
		}
	}
	if nameMatch {
		score += 2
	}
	if candidate.bestLine == 0 || score > candidate.bestScore {
		candidate.bestLine, candidate.bestScore = line, score
	}
}

// searchEvidenceGather turns every text hit and symbol match into evidence.
func (r *searchEvidenceRun) searchEvidenceGather() {
	for term, group := range r.groups {
		for _, raw := range anySlice(group["textHits"]) {
			match, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := match["file"].(string)
			for _, item := range anySlice(match["hits"]) {
				if hit, ok := item.(map[string]any); ok {
					r.searchEvidenceAdd(file, intArg(hit, "line", 0), term, "", false)
				}
			}
		}
		for _, raw := range anySlice(group["symbols"]) {
			sym, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			span, _ := sym["span"].(map[string]any)
			file, _ := sym["filePath"].(string)
			kind, _ := sym["matchKind"].(string)
			r.searchEvidenceAdd(file, intArg(span, "start", 0), term, "", strings.HasPrefix(kind, "name-"))
		}
	}
}

// searchEvidenceReferenceHop follows one unambiguous indexed reference hop,
// which can reach a consumer file that shares no literal term with the
// request. Only consider a call appearing beside two independent exact terms
// in one source section. This is a reference link, not proof that a value
// flows into the consumer.
func (r *searchEvidenceRun) searchEvidenceReferenceHop() {
	if len(r.groups) <= 1 || stringArg(r.out, "scopeNote", "") != "" {
		return
	}
	attempts := 0
	for _, item := range r.order {
		if attempts >= 8 || len(r.referenceFiles) >= 4 {
			break
		}
		if len(item.terms) < 2 || !isProductionSourcePath(item.file) {
			continue
		}
		entry := r.files[item.file]
		for line := maxInt(1, item.bestLine-4); line <= minInt(len(entry.lines), item.bestLine+4); line++ {
			for _, match := range evidenceCallName.FindAllStringSubmatch(entry.lines[line-1], -1) {
				if attempts >= 8 || len(r.referenceFiles) >= 4 {
					break
				}
				name := match[1]
				if name == "if" || name == "for" || name == "while" || name == "return" ||
					name == "isinstance" || name == "len" || name == "str" || name == "bool" {
					continue
				}
				attempts++
				refs, err := r.h.Grove.References(r.ctx, name)
				if err != nil || refs.DefCount != 1 || refs.Ambiguous || refs.SkippedFiles > 0 {
					continue
				}
				for _, ref := range refs.Refs {
					file := filepath.ToSlash(ref.File)
					if !isProductionSourcePath(file) {
						continue
					}
					if r.referenceFiles[file] == "" {
						if r.files[file] != nil || len(r.referenceFiles) >= 4 || r.searchEvidenceReadFile(file) == nil {
							continue
						}
						r.referenceFiles[file] = name
					}
					r.searchEvidenceAdd(file, ref.Line, -1, name, false)
				}
			}
		}
		if len(r.referenceFiles) > 0 {
			break // one resolved symbol is enough for this answer
		}
	}
}

// searchEvidenceRelatedProbe probes only files that already had an explicit
// hit. These suffixes do not widen the exact-match inventory or its
// completeness claim; an indexed reference file is labeled separately.
func (r *searchEvidenceRun) searchEvidenceRelatedProbe() {
	for _, spelling := range evidenceRelatedSpellings(r.terms) {
		for _, file := range r.fileOrder {
			if !isProductionSourcePath(file) {
				continue
			}
			entry := r.files[file]
			for i, line := range entry.lines {
				if strings.Contains(line, spelling) && evidenceLineScore(line) >= 3 {
					r.searchEvidenceAdd(file, i+1, -1, spelling, false)
				}
			}
		}
	}
}

// searchEvidenceRarest records the terms each file matched and, for a
// one-body batch, the rarest term.
func (r *searchEvidenceRun) searchEvidenceRarest() {
	r.fileTerms = map[string]map[int]bool{}
	for _, item := range r.order {
		if r.fileTerms[item.file] == nil {
			r.fileTerms[item.file] = map[int]bool{}
		}
		for term := range item.terms {
			r.fileTerms[item.file][term] = true
		}
	}
	// One body for a batch: the rarest term with any match is the most
	// specific clue (commons-lang pr1750: a 9-hit phrase inside the fixed
	// method vs a 57-hit helper whose 3-line definition took the slot).
	r.rarest = -1
	if !r.opts.single || len(r.groups) <= 1 {
		return
	}
	best, tied := 0, false
	for term, group := range r.groups {
		n := intArg(group, "totalHits", 0)
		if n == 0 {
			for _, raw := range anySlice(group["textHits"]) {
				if g, ok := raw.(map[string]any); ok {
					n += len(anySlice(g["hits"]))
				}
			}
		}
		n += len(anySlice(group["symbols"]))
		switch {
		case n == 0:
		case best == 0 || n < best:
			r.rarest, best, tied = term, n, false
		case n == best:
			tied = true
		}
	}
	if tied {
		r.rarest = -1 // no single most specific term
	}
}

// searchEvidenceScore ranks one candidate section.
func (r *searchEvidenceRun) searchEvidenceScore(item *searchEvidence) int {
	value := len(item.terms)*9 + len(r.fileTerms[item.file])*3 + item.bestScore
	if item.nameMatch {
		value += 3
	}
	if r.rarest >= 0 && item.terms[r.rarest] {
		value += 10
	}
	if item.definition && (r.rarest < 0 || item.terms[r.rarest]) {
		// jackson pr6019: "search readRootValue" named the definition
		// in its headline but delivered two callers' bodies, because
		// assignment-shaped call lines outscored the declaration line.
		// The definition of an exactly named symbol comes first.
		value += 20
	}
	if len(item.related) > 0 {
		value += 2
	}
	if r.opts.single && len(item.terms) > 1 {
		// One body only: the place where several of the caller's terms
		// meet outranks the plain definition of one of them (jansson
		// pr731: the error messages met in unpack, not in set_error).
		value += (len(item.terms) - 1) * 20
	}
	if refName := r.referenceFiles[item.file]; refName != "" {
		nearest := 1 << 30
		for spelling, uses := range item.relatedLines {
			if spelling == refName {
				continue
			}
			for _, refLine := range item.relatedLines[refName] {
				for _, useLine := range uses {
					nearest = minInt(nearest, evidenceDistance(refLine, useLine))
				}
			}
		}
		if nearest <= 10 {
			value += 6
		} else if nearest <= 30 {
			value += 3
		}
	}
	if len(item.terms) == 0 {
		value -= 8 // local spelling probes remain secondary to exact evidence
	}
	if len(r.groups) > 1 && item.hasSymbol && item.symbol.Kind == "field" {
		if item.nameMatch {
			value += 4 // the enclosing type is useful behavior context below
		} else {
			value -= 7
		}
	}
	if isTestFilePath(item.file) && item.hasSymbol {
		entry := r.files[item.file]
		for line := item.symbol.Span.Start; line <= item.symbol.Span.End && line <= len(entry.lines); line++ {
			if line > 0 && (strings.Contains(entry.lines[line-1], "skip(") ||
				strings.Contains(entry.lines[line-1], "#[ignore]") ||
				strings.Contains(entry.lines[line-1], "@Disabled")) {
				value += 5
				break
			}
		}
	}
	return value
}

// searchEvidenceRank drops declaration-only misses and orders the rest.
func (r *searchEvidenceRun) searchEvidenceRank() {
	// A declaration-shaped term names one symbol: `func walk` matched the
	// text of `func walkXFF` and delivered walkXFF's body (5 sessions in chi
	// pr1148); `def add_url_rule` delivered ServerList. A section whose only
	// evidence is declaration terms naming other symbols is not a candidate,
	// so the declared symbol itself (Walk) can be chosen instead.
	kept := r.order[:0]
	for _, item := range r.order {
		if item.hasSymbol && declarationTermsMiss(item, r.terms) {
			r.droppedDeclaration = true
			continue
		}
		kept = append(kept, item)
	}
	r.order = kept
	order := r.order
	sort.SliceStable(order, func(i, j int) bool {
		if r.searchEvidenceScore(order[i]) != r.searchEvidenceScore(order[j]) {
			return r.searchEvidenceScore(order[i]) > r.searchEvidenceScore(order[j])
		}
		return order[i].firstSeen < order[j].firstSeen
	})
}

// searchEvidenceOverlaps reports whether item overlaps an already selected
// section of the same file.
func (r *searchEvidenceRun) searchEvidenceOverlaps(item *searchEvidence) bool {
	for _, earlier := range r.selected {
		if earlier.file != item.file {
			continue
		}
		if earlier.hasSymbol && item.hasSymbol &&
			earlier.symbol.Span.Start <= item.symbol.Span.End &&
			item.symbol.Span.Start <= earlier.symbol.Span.End {
			// Separate distant windows of the same oversized method.
			if earlier.symbol.Span.Start == item.symbol.Span.Start &&
				earlier.symbol.Span.End == item.symbol.Span.End &&
				evidenceDistance(earlier.bestLine, item.bestLine) > 30 {
				continue
			}
			return true
		}
	}
	return false
}

// searchEvidenceChoose appends ranked sections up to limit.
func (r *searchEvidenceRun) searchEvidenceChoose(test, distinct bool, limit int) {
	for _, item := range r.order {
		if len(r.selected) >= limit {
			return
		}
		if r.chosen[item] || isTestFilePath(item.file) != test ||
			!isProductionSourcePath(item.file) && !test || distinct && r.seenFiles[item.file] || r.searchEvidenceOverlaps(item) {
			continue
		}
		r.selected = append(r.selected, item)
		r.chosen[item], r.seenFiles[item.file] = true, true
	}
}

// searchEvidenceSelect picks the sections to deliver. It reports false when
// there is nothing to deliver.
func (r *searchEvidenceRun) searchEvidenceSelect() bool {
	opts := r.opts
	r.chosen = map[*searchEvidence]bool{}
	r.seenFiles = map[string]bool{}
	r.maxSections = 5
	if len(r.groups) == 1 {
		r.maxSections = 2
	}
	if opts.single {
		r.maxSections = 1
	}
	r.searchEvidenceChoose(false, true, minInt(3, r.maxSections))
	if opts.single && len(r.selected) == 0 && opts.allowTests {
		r.searchEvidenceChoose(true, true, 1)
	}
	// A short related spelling in a file with an exact hit can expose a
	// consumer outside the directly matched symbol. Reserve one slot only
	// when that line is executable, and keep its uncertainty explicit.
	if len(r.groups) > 1 && !opts.single {
		for _, item := range r.order {
			if len(r.selected) >= r.maxSections || r.chosen[item] || len(item.terms) > 0 ||
				len(item.related) == 0 || item.bestScore < 9 ||
				!isProductionSourcePath(item.file) || r.searchEvidenceOverlaps(item) {
				continue
			}
			r.selected = append(r.selected, item)
			r.chosen[item], r.seenFiles[item.file] = true, true
			break
		}
	}
	r.searchEvidenceChoose(false, false, r.maxSections)
	if len(r.groups) > 1 && !opts.single {
		r.searchEvidenceChoose(true, true, r.maxSections+1)
	}
	if len(r.selected) == 0 { // a documentation or config search still needs context
		for _, item := range r.order {
			if opts.single && !opts.allowTests && isTestFilePath(item.file) {
				continue // the default budget never spends its one body on a test
			}
			r.selected = append(r.selected, item)
			break
		}
		if len(r.selected) == 0 {
			return false
		}
	}
	return true
}

// searchEvidenceRegions builds the source regions for the selected sections.
// droppedConstructor reports a constructor whose named type was too large.
func (r *searchEvidenceRun) searchEvidenceRegions() (regions []searchSourceRegion, droppedConstructor bool) {
	fullLines, fullBytes := searchFullBodyMaxLines, searchFullBodyMaxBytes
	if r.opts.maxLines > 0 {
		fullLines, fullBytes = r.opts.maxLines, r.opts.maxLines*80
	}
	regions = make([]searchSourceRegion, 0, len(r.selected))
	for _, item := range r.selected {
		region, ok := r.searchEvidenceRegion(item, fullLines, fullBytes)
		if !ok {
			droppedConstructor = true
			continue
		}
		duplicate := false
		for _, earlier := range regions {
			if earlier.file == region.file && earlier.start == region.start && earlier.end == region.end {
				duplicate = true // a constructor replaced by its class
			}
		}
		if duplicate {
			continue
		}
		regions = append(regions, region)
	}
	return regions, droppedConstructor
}

// searchEvidenceRegion bounds one selected section. It reports false when the
// section is a constructor whose named type is too large to deliver.
func (r *searchEvidenceRun) searchEvidenceRegion(item *searchEvidence, fullLines, fullBytes int) (searchSourceRegion, bool) {
	opts := r.opts
	if item.hasSymbol {
		// A search for a class name also hits its constructor's line,
		// which outscores the class declaration, and delivered the
		// constructor: the agent then read the whole class in its next
		// call (12 of 61 several-symbol answers, 7 of 171 single-symbol
		// ones, 2026-09-20..10-05). Deliver the named type when it fits,
		// otherwise no body.
		if owner := constructorOfNamedType(item.symbol, r.files[item.file].symbols, r.terms); owner != nil {
			if owner.Span.End-owner.Span.Start+1 > fullLines || len(owner.RawText) > fullBytes {
				return searchSourceRegion{}, false
			}
			item.symbol = *owner
		}
	}
	region := searchSourceRegion{file: item.file, hit: item.bestLine, window: true, maxLines: opts.maxLines}
	if item.hasSymbol {
		region.symbol, region.hasSymbol = item.symbol, true
	}
	radius := 9
	if len(item.related) > 0 && len(r.groups) > 1 {
		radius = 12
	}
	region.start, region.end = maxInt(1, item.bestLine-radius), item.bestLine+radius
	if item.hasSymbol {
		region.start = maxInt(region.start, item.symbol.Span.Start)
		region.end = minInt(region.end, item.symbol.Span.End)
		// Full body instead of a ±radius window for an EXACT term match
		// (len(item.terms) > 0, never a fuzzy "related spelling" guess)
		// whose enclosing symbol isn't huge. Measured 2026-09-23: 95.5%
		// of a session's tokens are cache_read (context re-billed every
		// turn), so a windowed hit the agent immediately re-Reads in
		// full costs a whole extra turn (~50K tokens) to save nothing —
		// the window was never going to be cheaper once re-read. Bound
		// matches the shared full-body
		// threshold (maxBodyLines/maxBodyBytes) so this isn't a new,
		// separately-tuned cap; a query with 20+ hits still windows
		// every one of them (selected is already capped upstream), so
		// this can't blow up a broad search.
		spanLines := item.symbol.Span.End - item.symbol.Span.Start + 1
		if len(item.terms) > 0 && spanLines <= fullLines &&
			len(item.symbol.RawText) <= fullBytes {
			region.start, region.end, region.window = item.symbol.Span.Start, item.symbol.Span.End, false
		}
	}
	if item.hasSymbol && item.symbol.Kind == "field" && item.nameMatch {
		if owner := lexicalOwnerSymbol(item.symbol, r.files[item.file].symbols); owner != nil &&
			(opts.maxLines == 0 || owner.Span.End-owner.Span.Start+1 <= opts.maxLines) {
			region.symbol, region.hasSymbol = *owner, true
			region.start, region.end, region.window = owner.Span.Start, owner.Span.End, false
			region.ownerContext = true
		}
	}
	region.evidence = r.searchEvidenceLabel(item, region.ownerContext)
	return region, true
}

// searchEvidenceLabel states why a section was delivered and how certain the
// link is.
func (r *searchEvidenceRun) searchEvidenceLabel(item *searchEvidence, ownerContext bool) string {
	var evidence string
	if len(item.terms) > 0 {
		if r.fallbackEvidence {
			evidence = "shorter fallback term matched after the supplied term returned no exact matches"
		} else {
			evidence = fmt.Sprintf("exact match for %d supplied term(s)", len(item.terms))
		}
	} else if name := r.referenceFiles[item.file]; name != "" {
		evidence = fmt.Sprintf("indexed reference to %s plus a related spelling; relationship unverified", name)
	} else {
		evidence = "related spelling in a file with an exact match; relationship unverified"
	}
	if ownerContext {
		evidence += "; small enclosing type included for behavioral context"
	}
	if len(item.related) > 0 && len(item.terms) > 0 {
		evidence += "; nearby related spelling, relationship unverified"
	}
	if isTestFilePath(item.file) {
		evidence += "; test"
	}
	return evidence
}
