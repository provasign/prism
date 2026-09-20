package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// searchEvidence is a bounded presentation of evidence already located by
// search. It does not infer a task or claim a data-flow edge. A short suffix
// of a supplied compound identifier is considered only inside exact-hit or
// indexed-reference source files, and is labeled as an unverified lead.
type searchEvidence struct {
	file         string
	symbol       grove.SymbolRecord
	hasSymbol    bool
	bestLine     int
	bestScore    int
	terms        map[int]bool
	related      map[string]bool
	relatedLines map[string][]int
	nameMatch    bool
	firstSeen    int
}

type evidenceFile struct {
	lines   []string
	symbols []grove.SymbolRecord
}

var evidenceCallName = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func evidenceDistance(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// Statement-opening keywords across the indexed languages. Each is a use of
// whatever it names, even when the line has no "(" or "=" for the shape
// rules below to catch (Python `for x in xs:`, shell `case "$x" in`, Lua
// `elseif x then`, Ruby `raise Err`).
var evidenceFlowPrefixes = []string{
	"if ", "elif ", "else if", "elsif ", "elseif ", "unless ", "until ",
	"for ", "while ", "with ", "except ", "case ", "switch ",
	"return ", "raise ", "throw ", "yield ", "defer ", "await ",
	"and ", "or ", "&& ", "|| ",
}

var evidenceStatementPrefixes = []string{"puts ", "print ", "echo ", "exit "}

var evidenceMemberAccess = regexp.MustCompile(`\w\.\w`)

// evidenceCommentLine recognizes comment and doc lines. A leading "*" is a
// block-comment continuation only when followed by a space, "/" or nothing;
// `*p = v` and `*count++` are pointer writes, not prose.
func evidenceCommentLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "#"), strings.HasPrefix(line, "//"), strings.HasPrefix(line, "--"),
		strings.HasPrefix(line, ":param"):
		return true
	case strings.HasPrefix(line, "*"):
		rest := strings.TrimLeft(line, "*")
		return rest == "" || rest[0] == ' ' || rest[0] == '/'
	}
	return false
}

// evidenceAnnotationLine recognizes declarations whose colon introduces a
// type (`x: Foo`, `def f(a, b):`, `ssl_context: SSLContext | None = None,`).
// It is deliberately blind to the colon's other jobs: Go `:=`, Rust/C++/PHP
// `::` paths, ternaries, and a typed binding whose initializer is a call
// (`let x: Foo = f();`), all of which are behavior, not declaration.
func evidenceAnnotationLine(line string) bool {
	colon := strings.IndexByte(line, ':')
	if colon < 0 || strings.Contains(line, ":=") || strings.Contains(line, "::") ||
		strings.Contains(line, " ? ") {
		return false
	}
	if eq := strings.IndexByte(line, '='); eq >= 0 && eq < colon {
		return false
	}
	return !strings.Contains(line[colon:], "(")
}

func evidenceLineScore(line string) int {
	line = strings.TrimSpace(line)
	if line == "" || evidenceCommentLine(line) {
		return -3
	}
	for _, prefix := range evidenceFlowPrefixes {
		if strings.HasPrefix(line, prefix) {
			return 4
		}
	}
	// Type annotations and parameter declarations name values but do not
	// show the behavior that consumes them. This matters in large classes
	// where declarations precede the operative assignments by many lines.
	if evidenceAnnotationLine(line) {
		return 0
	}
	// "(" signals an expression is being evaluated (call, condition, test).
	// "[[" is the same signal in shell-family syntax — common in templates
	// embedded as string constants (e.g. click's Zsh/Bash completion
	// scripts) — and must not be scored as if it were a bare declaration
	// just because it doesn't match the host language's own shape.
	looksLikeExpr := strings.Contains(line, "(") || strings.Contains(line, "[[")
	if strings.Contains(line, "=") && (strings.Contains(line, ".") || looksLikeExpr) {
		return 5
	}
	if looksLikeExpr {
		return 4
	}
	for _, prefix := range evidenceStatementPrefixes {
		if strings.HasPrefix(line, prefix) {
			return 3
		}
	}
	// Assignment, block open, or an operator statement (pipeline, channel
	// send, ternary, increment) with no call on the line.
	if strings.ContainsAny(line, "={") || strings.Contains(line, " | ") ||
		strings.Contains(line, " && ") || strings.Contains(line, " || ") ||
		strings.Contains(line, "<-") || strings.Contains(line, "->") ||
		strings.Contains(line, " ? ") || strings.Contains(line, "++") ||
		strings.Contains(line, "--") {
		return 3
	}
	// Member access alone is weak evidence of use, but it is not a declaration.
	if evidenceMemberAccess.MatchString(line) {
		return 2
	}
	return 0
}

// A suffix such as ssl_context from proxy_ssl_context is a useful probe, but
// it is not independent evidence of a semantic relationship. Keep it local.
func evidenceRelatedSpellings(terms []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, term := range terms {
		cut := strings.IndexByte(term, '_')
		if cut < 1 || len(term)-cut-1 < 8 {
			continue
		}
		suffix := term[cut+1:]
		if seen[suffix] || !identLike.MatchString(suffix) {
			continue
		}
		seen[suffix] = true
		out = append(out, suffix)
		if len(out) == 2 {
			break
		}
	}
	return out
}

func (h *Handler) compactSearchBodiesEnclosingExcept(ctx context.Context, out map[string]any, skip []grove.SymbolRecord) string {
	if h.Grove == nil {
		return ""
	}
	groups := []map[string]any{out}
	fallbackEvidence := false
	if batch, ok := out["results"].([]map[string]any); ok {
		groups = batch
	} else if searchResultEmpty(out) {
		// A single-term miss may already carry a bounded shorter-term fallback.
		// Treat that fallback as source evidence so the caller receives the
		// enclosing implementation in the same turn instead of having to repeat
		// discovery with grep or a second Prism call. The rendered inventory still
		// labels the exact term as a miss and the source evidence as a fallback.
		if fallback, ok := out["fallbackResults"].([]map[string]any); ok && len(fallback) > 0 {
			groups = fallback
			fallbackEvidence = true
		}
	}
	var terms []string
	for _, group := range groups {
		terms = append(terms, stringArg(group, "query", ""))
	}
	files := map[string]*evidenceFile{}
	fileOrder := []string{}
	readFile := func(file string) *evidenceFile {
		file = filepath.ToSlash(file)
		if entry, ok := files[file]; ok {
			return entry
		}
		if file == "" || len(files) >= 24 {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(h.Root, filepath.FromSlash(file)))
		if err != nil || len(data) > 2<<20 {
			return nil
		}
		entry := &evidenceFile{lines: strings.Split(string(data), "\n")}
		entry.symbols, _ = h.Grove.FileSymbols(ctx, file)
		files[file] = entry
		fileOrder = append(fileOrder, file)
		return entry
	}
	candidates := map[string]*searchEvidence{}
	var order []*searchEvidence
	add := func(file string, line, term int, related string, nameMatch bool) {
		file = filepath.ToSlash(file)
		entry := readFile(file)
		if entry == nil || line < 1 || line > len(entry.lines) {
			return
		}
		for _, delivered := range skip {
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
		candidate := candidates[key]
		if candidate == nil {
			candidate = &searchEvidence{file: file, terms: map[int]bool{}, related: map[string]bool{},
				relatedLines: map[string][]int{}, firstSeen: len(order)}
			if sym != nil {
				candidate.symbol, candidate.hasSymbol = *sym, true
			}
			candidates[key] = candidate
			order = append(order, candidate)
		}
		if term >= 0 {
			candidate.terms[term] = true
		}
		if related != "" {
			candidate.related[related] = true
			candidate.relatedLines[related] = append(candidate.relatedLines[related], line)
		}
		candidate.nameMatch = candidate.nameMatch || nameMatch
		score := evidenceLineScore(entry.lines[line-1])
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
	for term, group := range groups {
		for _, raw := range anySlice(group["textHits"]) {
			match, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := match["file"].(string)
			for _, item := range anySlice(match["hits"]) {
				if hit, ok := item.(map[string]any); ok {
					add(file, intArg(hit, "line", 0), term, "", false)
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
			add(file, intArg(span, "start", 0), term, "", strings.HasPrefix(kind, "name-"))
		}
	}
	if len(order) == 0 {
		return h.compactSearchBodiesLegacy(ctx, out, skip)
	}
	// One unambiguous indexed reference hop can reach a consumer file that
	// shares no literal term with the request. Only consider a call appearing
	// beside two independent exact terms in one source section. This is a
	// reference link, not proof that a value flows into the consumer.
	referenceFiles := map[string]string{}
	if len(groups) > 1 && stringArg(out, "scopeNote", "") == "" {
		attempts := 0
		for _, item := range order {
			if attempts >= 8 || len(referenceFiles) >= 4 {
				break
			}
			if len(item.terms) < 2 || !isProductionSourcePath(item.file) {
				continue
			}
			entry := files[item.file]
			for line := maxInt(1, item.bestLine-4); line <= minInt(len(entry.lines), item.bestLine+4); line++ {
				for _, match := range evidenceCallName.FindAllStringSubmatch(entry.lines[line-1], -1) {
					if attempts >= 8 || len(referenceFiles) >= 4 {
						break
					}
					name := match[1]
					if name == "if" || name == "for" || name == "while" || name == "return" ||
						name == "isinstance" || name == "len" || name == "str" || name == "bool" {
						continue
					}
					attempts++
					refs, err := h.Grove.References(ctx, name)
					if err != nil || refs.DefCount != 1 || refs.Ambiguous || refs.SkippedFiles > 0 {
						continue
					}
					for _, ref := range refs.Refs {
						file := filepath.ToSlash(ref.File)
						if !isProductionSourcePath(file) {
							continue
						}
						if referenceFiles[file] == "" {
							if files[file] != nil || len(referenceFiles) >= 4 || readFile(file) == nil {
								continue
							}
							referenceFiles[file] = name
						}
						add(file, ref.Line, -1, name, false)
					}
				}
			}
			if len(referenceFiles) > 0 {
				break // one resolved symbol is enough for this answer
			}
		}
	}
	// Probe only files that already had an explicit hit. These suffixes do not
	// widen the exact-match inventory or its completeness claim; an indexed
	// reference file is labeled separately below.
	for _, spelling := range evidenceRelatedSpellings(terms) {
		for _, file := range fileOrder {
			if !isProductionSourcePath(file) {
				continue
			}
			entry := files[file]
			for i, line := range entry.lines {
				if strings.Contains(line, spelling) && evidenceLineScore(line) >= 3 {
					add(file, i+1, -1, spelling, false)
				}
			}
		}
	}
	fileTerms := map[string]map[int]bool{}
	for _, item := range order {
		if fileTerms[item.file] == nil {
			fileTerms[item.file] = map[int]bool{}
		}
		for term := range item.terms {
			fileTerms[item.file][term] = true
		}
	}
	score := func(item *searchEvidence) int {
		value := len(item.terms)*9 + len(fileTerms[item.file])*3 + item.bestScore
		if item.nameMatch {
			value += 3
		}
		if len(item.related) > 0 {
			value += 2
		}
		if refName := referenceFiles[item.file]; refName != "" {
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
		if len(groups) > 1 && item.hasSymbol && item.symbol.Kind == "field" {
			if item.nameMatch {
				value += 4 // the enclosing type is useful behavior context below
			} else {
				value -= 7
			}
		}
		if isTestFilePath(item.file) && item.hasSymbol {
			entry := files[item.file]
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
	sort.SliceStable(order, func(i, j int) bool {
		if score(order[i]) != score(order[j]) {
			return score(order[i]) > score(order[j])
		}
		return order[i].firstSeen < order[j].firstSeen
	})
	var selected []*searchEvidence
	chosen := map[*searchEvidence]bool{}
	seenFiles := map[string]bool{}
	maxSections := 5
	if len(groups) == 1 {
		maxSections = 2
	}
	overlaps := func(item *searchEvidence) bool {
		for _, earlier := range selected {
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
	choose := func(test, distinct bool, cap int) {
		for _, item := range order {
			if len(selected) >= cap {
				return
			}
			if chosen[item] || isTestFilePath(item.file) != test ||
				!isProductionSourcePath(item.file) && !test || distinct && seenFiles[item.file] || overlaps(item) {
				continue
			}
			selected = append(selected, item)
			chosen[item], seenFiles[item.file] = true, true
		}
	}
	choose(false, true, minInt(3, maxSections))
	// A short related spelling in a file with an exact hit can expose a
	// consumer outside the directly matched symbol. Reserve one slot only
	// when that line is executable, and keep its uncertainty explicit.
	if len(groups) > 1 {
		for _, item := range order {
			if len(selected) >= maxSections || chosen[item] || len(item.terms) > 0 ||
				len(item.related) == 0 || item.bestScore < 9 ||
				!isProductionSourcePath(item.file) || overlaps(item) {
				continue
			}
			selected = append(selected, item)
			chosen[item], seenFiles[item.file] = true, true
			break
		}
	}
	choose(false, false, maxSections)
	if len(groups) > 1 {
		choose(true, true, maxSections+1)
	}
	if len(selected) == 0 { // a documentation or config search still needs context
		selected = append(selected, order[0])
	}
	regions := make([]searchSourceRegion, 0, len(selected))
	for _, item := range selected {
		region := searchSourceRegion{file: item.file, hit: item.bestLine, window: true}
		if item.hasSymbol {
			region.symbol, region.hasSymbol = item.symbol, true
		}
		radius := 9
		if len(item.related) > 0 && len(groups) > 1 {
			radius = 12
		}
		region.start, region.end = maxInt(1, item.bestLine-radius), item.bestLine+radius
		if item.hasSymbol {
			region.start = maxInt(region.start, item.symbol.Span.Start)
			region.end = minInt(region.end, item.symbol.Span.End)
			if item.symbol.Span.End-item.symbol.Span.Start+1 <= 20 && len(item.terms) > 0 {
				region.start, region.end, region.window = item.symbol.Span.Start, item.symbol.Span.End, false
			}
		}
		if item.hasSymbol && item.symbol.Kind == "field" && item.nameMatch {
			if owner := lexicalOwnerSymbol(item.symbol, files[item.file].symbols); owner != nil {
				region.symbol, region.hasSymbol = *owner, true
				region.start, region.end, region.window = owner.Span.Start, owner.Span.End, false
				region.ownerContext = true
			}
		}
		if len(item.terms) > 0 {
			if fallbackEvidence {
				region.evidence = "shorter fallback term matched after the supplied term returned no exact matches"
			} else {
				region.evidence = fmt.Sprintf("exact match for %d supplied term(s)", len(item.terms))
			}
		} else if name := referenceFiles[item.file]; name != "" {
			region.evidence = fmt.Sprintf("indexed reference to %s plus a related spelling; relationship unverified", name)
		} else {
			region.evidence = "related spelling in a file with an exact match; relationship unverified"
		}
		if region.ownerContext {
			region.evidence += "; small enclosing type included for behavioral context"
		}
		if len(item.related) > 0 && len(item.terms) > 0 {
			region.evidence += "; nearby related spelling, relationship unverified"
		}
		if isTestFilePath(item.file) {
			region.evidence += "; test"
		}
		regions = append(regions, region)
	}
	if rendered := h.renderEnclosingSearchBodies(regions); rendered != "" {
		return rendered
	}
	return h.compactSearchBodiesLegacy(ctx, out, skip)
}
