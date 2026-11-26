package core

import (
	"context"
	"fmt"
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
	// definition: the section is the declaration of a symbol the term names
	// exactly (its declaration line was a hit, or it was a name-exact
	// symbol match). It outranks call sites of the same name.
	definition bool
	firstSeen  int
}

type evidenceFile struct {
	lines   []string
	symbols []grove.SymbolRecord
	// docLines marks Python docstring lines (0-based). Example code in a
	// docstring documents a name; it is not a use of it.
	docLines []bool
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
	return h.compactSearchBodiesWith(ctx, out, skip, searchBodyOptions{})
}

// searchBodyOptions shapes enclosing-body delivery. The zero value is the
// explicit include_bodies=true shape (up to two regions for one term, five
// for a batch). single is the default search budget (searchbudget.go): one
// region, the top-ranked non-test match, at most maxLines lines.
type searchBodyOptions struct {
	single     bool
	allowTests bool
	maxLines   int
}

// compactSearchBodiesBudgeted is the default-budget body: at most one bounded
// region, from production source unless the request targets tests.
func (h *Handler) compactSearchBodiesBudgeted(ctx context.Context, out map[string]any, allowTests bool) string {
	return h.compactSearchBodiesWith(ctx, out, nil,
		searchBodyOptions{single: true, allowTests: allowTests, maxLines: searchBudgetBodyLines})
}

func (h *Handler) compactSearchBodiesWith(ctx context.Context, out map[string]any, skip []grove.SymbolRecord, opts searchBodyOptions) string {
	if h.Grove == nil {
		return ""
	}
	run := searchEvidenceStart(ctx, h, out, skip, opts)
	run.searchEvidenceGather()
	if len(run.order) == 0 {
		return h.compactSearchBodiesLegacyWith(ctx, out, skip, opts)
	}
	run.searchEvidenceReferenceHop()
	run.searchEvidenceRelatedProbe()
	run.searchEvidenceRarest()
	run.searchEvidenceRank()
	if !run.searchEvidenceSelect() {
		return ""
	}
	regions, droppedConstructor := run.searchEvidenceRegions()
	if rendered := h.renderSearchBodiesUnlessShared(out, regions, opts.single); rendered != "" {
		return rendered
	}
	if droppedConstructor || run.droppedDeclaration {
		return "" // the legacy picker would deliver the same section
	}
	return h.compactSearchBodiesLegacyWith(ctx, out, skip, opts)
}

// constructorOfNamedType returns the type that sym constructs when a search
// term names that type exactly; nil otherwise.
func constructorOfNamedType(sym grove.SymbolRecord, fileSyms []grove.SymbolRecord, terms []string) *grove.SymbolRecord {
	parent := leafOf(sym.ParentSymbol)
	if parent == "" || (sym.Name != parent && !strings.EqualFold(sym.Kind, "constructor")) {
		return nil
	}
	for i := range fileSyms {
		owner := &fileSyms[i]
		switch strings.ToLower(owner.Kind) {
		case "class", "struct", "record", "type":
		default:
			continue
		}
		if owner.Name != parent && owner.QualifiedName != sym.ParentSymbol {
			continue
		}
		if owner.Span.Start > sym.Span.Start || owner.Span.End < sym.Span.End {
			continue
		}
		for _, term := range terms {
			term = strings.TrimSpace(term)
			if term == owner.Name || term == owner.QualifiedName {
				return owner
			}
		}
	}
	return nil
}

// declarationTerm matches a term written as a declaration and captures the
// name it declares: "func walk", "func (mx *Mux) Mount", "def add_url_rule",
// "def add(", "class Foo", "function bar", "fn baz", "async def qux".
var declarationTerm = regexp.MustCompile(`^\s*(?:func\s*(?:\([^)]*\)\s*)?|(?:async\s+)?def\s+|class\s+|function\s+|fn\s+)([A-Za-z_$][\w$]*)\s*(?:\[[^\]]*\])?\s*\(?\s*$`)

// declarationTermsMiss reports whether every term that matched item is a
// declaration term, and none declares item's symbol.
func declarationTermsMiss(item *searchEvidence, terms []string) bool {
	if len(item.terms) == 0 {
		return false
	}
	for index := range item.terms {
		if index < 0 || index >= len(terms) {
			return false
		}
		m := declarationTerm.FindStringSubmatch(terms[index])
		if m == nil || m[1] == item.symbol.Name {
			return false
		}
	}
	return true
}

// declaresTerm reports whether line is the declaration line of sym and sym is
// named exactly by term: the first line of sym's span that mentions its name
// is this line. Leading doc comments and annotations inside the span are
// skipped by the same rule.
func declaresTerm(lines []string, sym grove.SymbolRecord, line int, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" || sym.Name == "" {
		return false
	}
	if sym.Name != term && sym.QualifiedName != term {
		return false
	}
	for l := sym.Span.Start; l <= line && l <= len(lines); l++ {
		if l < 1 {
			continue
		}
		if strings.Contains(lines[l-1], sym.Name) {
			return l == line
		}
	}
	return false
}

// renderSearchBodiesUnlessShared renders the picked bodies. In the default
// single-body mode it withholds a body whose symbol name is also an
// exact-name match for a DIFFERENT symbol in the answer: that body is one of
// several and would be read as "the" code. (An explicit include_bodies=true
// asks for several bodies, each headed by its qualified name; those stay.)
// make_response in flask matches Flask.make_response and the helper
// make_response; the search delivered the helper's body under a header that
// never said another existed. Those regions become one line naming every
// candidate with its span, so the caller looks up the one it means. A class
// and its own constructor share a name but are one piece of code.
func (h *Handler) renderSearchBodiesUnlessShared(out map[string]any, regions []searchSourceRegion, single bool) string {
	if !single {
		return h.renderEnclosingSearchBodies(regions)
	}
	var keep []searchSourceRegion
	var notes []string
	noted := map[string]bool{}
	for _, region := range regions {
		others := sharedNameSymbols(out, region)
		if len(others) == 0 {
			others = h.indexedSharedName(out, region)
		}
		if len(others) == 0 {
			keep = append(keep, region)
			continue
		}
		name := region.symbol.Name
		if noted[name] {
			continue
		}
		noted[name] = true
		parts := []string{fmt.Sprintf("%s %s:%d-%d", region.symbol.QualifiedName, region.file, region.symbol.Span.Start, region.symbol.Span.End)}
		parts = append(parts, others...)
		sort.Strings(parts) // regions arrive in no fixed order
		notes = append(notes, fmt.Sprintf("// No body: %d different symbols share the name %q -- %s. Use op=lookup with the qualified name you mean.",
			len(parts), name, strings.Join(parts, "; ")))
	}
	rendered := ""
	if len(keep) > 0 {
		rendered = h.renderEnclosingSearchBodies(keep)
	}
	if len(notes) == 0 {
		return rendered
	}
	return "\n" + strings.Join(notes, "\n") + "\n" + rendered
}

// indexedSharedName is sharedNameSymbols for an answer that lists no
// symbols (scope=text): the index's other declarations of a searched name.
// urfave pr2355 (2026-10-20): `prism search PreParse --scope text` noted five
// PreParse symbols, then printed BoolWithInverseFlag.PreParse in full while
// the one the task needed, FlagBase.PreParse, was a single locator line.
// Only a body whose name is a searched term is checked; a caller's body
// that merely contains the term is not "the" symbol.
func (h *Handler) indexedSharedName(out map[string]any, region searchSourceRegion) []string {
	if h.Grove == nil || !region.hasSymbol || region.symbol.Name == "" || !searchedTerm(out, region.symbol.Name) {
		return nil
	}
	cands, err := h.Grove.Resolve(context.Background(), region.symbol.Name)
	if err != nil || len(cands) < 2 {
		return nil
	}
	sym := region.symbol
	var others []string
	for _, c := range cands {
		if c.TestDouble || (filepath.ToSlash(c.File) == region.file && c.Line == sym.Span.Start) ||
			sameTypeAndConstructor(sym.QualifiedName, c.Name, sym.Name) {
			continue
		}
		others = append(others, fmt.Sprintf("%s %s:%d", c.Name, filepath.ToSlash(c.File), c.Line))
	}
	return others
}

// searchedTerm reports whether name is one of the answer's search terms.
func searchedTerm(out map[string]any, name string) bool {
	groups := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		groups = append(groups, batch...)
	}
	for _, g := range groups {
		if q, _ := g["query"].(string); strings.TrimSpace(q) == name {
			return true
		}
	}
	return false
}

// sharedNameSymbols lists the answer's other exact-name symbols that share
// region's symbol name, as "qualifiedName file:start-end".
func sharedNameSymbols(out map[string]any, region searchSourceRegion) []string {
	if !region.hasSymbol || region.symbol.Name == "" {
		return nil
	}
	sym := region.symbol
	groups := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		groups = batch
	}
	var others []string
	seen := map[string]bool{}
	for _, group := range groups {
		for _, raw := range anySlice(group["symbols"]) {
			sm, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := sm["matchKind"].(string); kind != "name-exact" {
				continue
			}
			name, _ := sm["name"].(string)
			if name != sym.Name {
				continue
			}
			file, _ := sm["filePath"].(string)
			qn, _ := sm["qualifiedName"].(string)
			span, _ := sm["span"].(map[string]any)
			start, end := intArg(span, "start", 0), intArg(span, "end", 0)
			if filepath.ToSlash(file) == region.file && start == sym.Span.Start {
				continue // the delivered symbol itself
			}
			if sameTypeAndConstructor(sym.QualifiedName, qn, name) {
				continue
			}
			key := fmt.Sprintf("%s %s:%d-%d", qn, filepath.ToSlash(file), start, end)
			if !seen[key] {
				seen[key] = true
				others = append(others, key)
			}
		}
	}
	return others
}

// sameTypeAndConstructor reports a class and its own constructor
// (Foo and Foo.Foo, or Foo.__init__-style owners named like the type).
func sameTypeAndConstructor(a, b, name string) bool {
	return a == b+"."+name || b == a+"."+name
}

// pythonDocstringLines marks the lines of triple-quoted strings that stand
// alone as a statement (module, class, and function docstrings). A
// triple-quoted string that is assigned or passed (`X = """..."""`) is a
// value, often an embedded script or template, and stays scored as code.
func pythonDocstringLines(lines []string) []bool {
	marks := make([]bool, len(lines))
	delim, doc := "", false
	for i, line := range lines {
		if delim != "" {
			marks[i] = doc
			if strings.Contains(line, delim) {
				delim = ""
			}
			continue
		}
		at, quote := -1, ""
		for _, q := range []string{`"""`, `'''`} {
			if k := strings.Index(line, q); k >= 0 && (at < 0 || k < at) {
				at, quote = k, q
			}
		}
		if at < 0 {
			continue
		}
		prefix := strings.TrimLeft(strings.TrimSpace(line[:at]), "rRuUbB")
		doc = strings.TrimSpace(line[:at]) == "" || prefix == "" && len(strings.TrimSpace(line[:at])) <= 2
		if doc {
			marks[i] = true
		}
		if !strings.Contains(line[at+3:], quote) {
			delim = quote
		}
	}
	return marks
}
