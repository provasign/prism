package core

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// enrichNoMethodError turns grove's terminal "type X declares no method Y"
// into a correctable one: the members X actually declares (from prism's own
// index) plus the one failure mode measured to cause it in practice --
// querying AFTER editing/deleting the member instead of before. Returns nil
// when nothing useful can be added (unparseable query, type not found), so
// the caller falls back to the original error unchanged.
func (h *Handler) enrichNoMethodError(ctx context.Context, query string, orig error) error {
	q := query
	if i := strings.IndexByte(q, '('); i >= 0 {
		q = q[:i]
	}
	dot := strings.LastIndexByte(q, '.')
	if dot <= 0 {
		return nil
	}
	typeName := strings.TrimSpace(q[:dot])
	if typeName == "" || h.Grove == nil {
		return nil
	}
	ms, err := h.Grove.SearchSymbols(ctx, typeName, 10)
	if err != nil {
		return nil
	}
	var typeFile string
	for _, m := range ms {
		if m.Name == typeName {
			typeFile = m.FilePath
			break
		}
	}
	if typeFile == "" {
		return nil
	}
	syms, err := h.Grove.FileSymbols(ctx, typeFile)
	if err != nil {
		return nil
	}
	prefix := typeName + "."
	var members []string
	for _, s := range syms {
		if strings.HasPrefix(s.QualifiedName, prefix) {
			members = append(members, strings.TrimPrefix(s.QualifiedName, prefix))
			if len(members) >= 12 {
				break
			}
		}
	}
	if len(members) == 0 {
		return fmt.Errorf("%w — if you already edited or deleted this member, that is why: "+
			"change-impact reads the CURRENT index, so it must run BEFORE the edit; "+
			"query the new name, or re-run after re-indexing", orig)
	}
	return fmt.Errorf("%w — %s currently declares: %s. If you already edited or deleted "+
		"the member you meant, that is why: change-impact reads the CURRENT index, so it "+
		"must run BEFORE the edit",
		orig, typeName, strings.Join(members, ", "))
}

// wideMemberAmbiguityThreshold: past this many same-named candidates, the
// right move is one exhaustive text search, not one change_impact call per
// candidate. Measured (2026-09-06, grafana-querydata-impact transcript):
// "QueryData" was ambiguous across 41 candidates. The agent read "re-run
// with one of these" literally, issued 12 separate change_impact calls
// guessing receiver names, spent ~44KB doing it, and still missed 6 of 51
// required sites — files reachable by a plain ".QueryData(" text search
// (pkg/expr/nodes.go, two sqleng/sql_engine.go files) that no receiver-name
// guess would ever reach, because the interface is external (no local
// declaration for change_impact to anchor a family query on).
var ambiguousCandidateCount = regexp.MustCompile(`is ambiguous — (\d+) candidates`)

const crossLanguageAmbiguityMarker = "ambiguous across language families"

const wideMemberAmbiguityThreshold = 8

func enrichAmbiguousImpactError(query string, orig error) error {
	m := ambiguousCandidateCount.FindStringSubmatch(orig.Error())
	if m == nil {
		return nil
	}
	count, convErr := strconv.Atoi(m[1])
	if convErr != nil || count < wideMemberAmbiguityThreshold {
		return nil // few enough that querying each candidate individually is fine
	}
	head := query
	if i := strings.IndexByte(head, '('); i >= 0 {
		head = head[:i]
	}
	if dot := strings.LastIndexByte(head, '.'); dot >= 0 {
		head = head[dot+1:]
	}
	return fmt.Errorf("%w\n\nWIDE MEMBER (%d candidates): do NOT issue one change_impact call per "+
		"candidate — that misses call sites through interfaces this engine cannot anchor a family "+
		"query on (an interface with no local declaration, e.g. an external SDK contract). Instead "+
		"run prism_search(query=[\"%s(\", \".%s(\"], scope=\"text\", exhaustive=true) for the complete "+
		"declaration and call-site inventory by text, THEN change_impact only the specific receivers "+
		"that inventory names as ambiguous", orig, count, head, head)
}

// crossLanguageImpactChoice converts Grove's cross-language ambiguity into a
// successful, actionable MCP response. Returning the raw error is a dead end
// for agents, while merging the language families (or guessing the first one)
// would make the change set actively unsafe. Resolve supplies structured file
// identities without parsing Grove's human-readable candidate list.
func (h *Handler) crossLanguageImpactChoice(ctx context.Context, query string, orig error) (map[string]any, bool) {
	if orig == nil || !strings.Contains(orig.Error(), crossLanguageAmbiguityMarker) {
		return nil, false
	}
	candidates, err := h.Grove.Resolve(ctx, query)
	if err != nil || len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].File != candidates[j].File {
			return candidates[i].File < candidates[j].File
		}
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].Line < candidates[j].Line
	})
	seen := map[string]bool{}
	alternatives := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.File + "\x00" + candidate.Name + "\x00" + strconv.Itoa(candidate.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		entry := map[string]any{
			"name":     candidate.Name,
			"kind":     candidate.Kind,
			"language": candidate.Language,
			"filePath": candidate.File,
			"line":     candidate.Line,
		}
		if candidate.TestDouble {
			entry["testDouble"] = true
			entry["testLabel"] = candidate.TestLabel
		}
		alternatives = append(alternatives, entry)
	}
	if len(alternatives) == 0 {
		return nil, false
	}
	return map[string]any{
		"query":             query,
		"status":            "needs_file_scope",
		"requiresFileScope": true,
		"candidateCount":    len(alternatives),
		"alternatives":      alternatives,
		"ambiguityNote": "The query matches declarations in multiple language families. " +
			"No change sets were merged and Prism did not guess. Re-run prism_change_impact " +
			"with file set to one alternative's filePath (or a unique path fragment).",
	}, true
}

func (h *Handler) toolChangeImpact(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	args, query, err := h.scopePathLineQuery(ctx, args, query)
	if err != nil {
		return nil, err
	}
	res, err := h.resolveImpact(ctx, args, query)
	if err != nil {
		return nil, err
	}
	if res.choice != nil {
		h.Ledger.RecordCall("prism_change_impact")
		return res.choice, nil
	}
	r := res.result
	h.Ledger.RecordCall("prism_change_impact")
	if r.MemberKind != "" {
		return h.changeImpactMemberAnswer(ctx, args, r), nil
	}
	r, signatureNote := h.changeImpactSelectSignature(ctx, args, query, res.inferenceNote, r)
	a := h.changeImpactNewAnswer(ctx, args, r)
	out := a.baseOutput()
	a.addNotes(ctx, out, res, signatureNote)
	a.addCallSiteNotes(ctx, out)
	a.finish(ctx, out, query, res.familyPickNote)
	return out, nil
}

// sameFileNamesakesNote names the members that share a bare name with the
// package-level function the file-scoped query resolved to. The bare name
// means the function there by rule, but an agent asking about echo's
// HTTPStatusCoder.StatusCode as "StatusCode in httperror.go" got the
// function's change set marked complete with no word that the interface
// method of the same name exists (replay, 2026-10-15).
func (h *Handler) sameFileNamesakesNote(ctx context.Context, query, file string, r *grove.ChangeImpactResult) string {
	if file == "" || r == nil || strings.ContainsAny(query, ".(") || len(r.Declarations) == 0 {
		return ""
	}
	for _, d := range r.Declarations {
		if d.ParentSymbol != "" {
			return ""
		}
	}
	syms, err := h.Grove.FileSymbols(ctx, file)
	if err != nil {
		return ""
	}
	var others []string
	seen := map[string]bool{}
	for _, s := range syms {
		if s.Name != query || s.ParentSymbol == "" || seen[s.QualifiedName] {
			continue
		}
		switch s.Kind {
		case "method", "function", "constructor":
		default:
			continue
		}
		seen[s.QualifiedName] = true
		others = append(others, s.QualifiedName)
	}
	if len(others) == 0 {
		return ""
	}
	sort.Strings(others)
	return fmt.Sprintf("this is the change set of the package-level function %s; %s also declares %s — for one of those, call change_impact with its full name",
		query, file, strings.Join(others, ", "))
}

// compilerBackedImpactLangs have a type-checked native pass in grove.
var compilerBackedImpactLangs = map[string]bool{
	"go": true, "java": true, "typescript": true, "tsx": true, "javascript": true, "rust": true,
}

// applyCompilerBackedCompleteness states plainly that a closed impact result
// is complete when the compiler-backed pass ran. The generic boundary ("Do
// not claim global completeness…", safeToClaimComplete=false) was on every
// answer, and agents re-checked complete answers with grep (exp branch
// 2026-09-29: capability-first steering).
func applyCompilerBackedCompleteness(out map[string]any, r *grove.ChangeImpactResult) {
	// Name-matched callers do not block this: name matching adds sites the
	// compiler could not see (a file outside every tsconfig), so they make
	// the set over-inclusive, never incomplete. They are named in
	// callerEvidence. One such caller used to withhold "complete" from an
	// answer whose other 13 callers were compiler-resolved (hono
	// Router.match), and the agent re-grepped the repository.
	if _, degraded := out["degradedAnalysis"]; degraded {
		return
	}
	lang := ""
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		if len(group) > 0 {
			lang = group[0].Language
			break
		}
	}
	c, _ := out["completeness"].(string)
	// A free function has no family to close: its change set is complete
	// when the compiler resolved every call and value reference to it, which
	// the Go and TypeScript passes record (chi URLParam answered
	// "callers-only" while Context.URLParam answered complete).
	if c == "callers-only" && (lang == "go" || lang == "typescript" || lang == "tsx") && len(r.NameMatchedCallers) == 0 {
		c = "closed"
		out["completeness"] = "closed"
	}
	if c != "closed" {
		return
	}
	if !compilerBackedImpactLangs[lang] {
		return
	}
	out["completenessScope"] = "repository (compiler-backed " + lang + ")"
	out["safeToClaimComplete"] = true
	out["scopeBoundary"] = compilerScopeBoundary
	if _, ok := out["evidenceNote"]; ok {
		out["evidenceNote"] = compilerEvidenceNote
	}
}

// addDegradedNote warns when the compiler-backed analysis for the answer's
// language did not run on this index: the sites above are name-based and may
// be incomplete or over-inclusive (TypeScript field renames measured 1 of 44
// sites confirmed name-based vs 44 of 44 with the compiler). The fix command
// lets the agent repair it and re-index.
func (h *Handler) addDegradedNote(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult) {
	// Name-matched callers do not close anything: "completeness: closed"
	// next to "compiler-backed analysis did not run" contradicted itself
	// (hono Router.match without tsc, 2026-10-14).
	defer func() {
		if _, degraded := out["degradedAnalysis"]; degraded {
			if c, _ := out["completeness"].(string); c == "closed" {
				out["completeness"] = "partial"
			}
			if _, ok := out["callerCoverage"]; ok {
				out["callerCoverage"] = "partial"
			}
		}
	}()
	lang := ""
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		if len(group) > 0 {
			lang = group[0].Language
			break
		}
	}
	if lang == "" {
		return
	}
	if lang == "tsx" || lang == "javascript" {
		lang = "typescript"
	}
	// Only languages whose completed native pass nativePassCompleted can
	// recognize get the status warnings below. Python has no native pass, so
	// warning there flagged every Python answer as possibly incomplete.
	checked := nativePassChecked[lang]
	st, err := h.Grove.Status(ctx)
	if err != nil || st == nil {
		if checked {
			out["degradedAnalysis"] = lang + " compiler-backed analysis status is unavailable; caller coverage may be incomplete. Run prism index and check prism status"
		}
		return
	}
	for _, diagnostic := range st.Native {
		if !checked {
			break
		}
		if diagnostic == "native analyzers disabled" || strings.HasPrefix(diagnostic, lang+": ") && strings.Contains(diagnostic, "disabled by config") {
			out["degradedAnalysis"] = lang + " compiler-backed analysis was disabled for this index; caller coverage may be incomplete. Enable native analysis and run prism index"
			return
		}
	}
	if lang == "go" {
		if pkgs, first := h.partialGoPackages(st.Native, r); len(pkgs) > 0 {
			// BUG-03: a package with type errors is analysed partially; its
			// sites can be wrong (a Close() on another type) or missing.
			out["partialTypeCheck"] = pkgs
			out["degradedAnalysis"] = "go type-checked " + strings.Join(pkgs, ", ") +
				" only partially (" + first + "); sites there may be wrong or missing. Fix the type errors, then prism index"
			return
		}
	}
	for _, issue := range st.Readiness {
		if issue.Language == lang {
			out["degradedAnalysis"] = lang + " compiler-backed analysis did not run on this index (" + issue.Problem +
				"); these sites are name-matched and may be incomplete. Fix: " + issue.Fix
			return
		}
	}
	if checked && !nativePassCompleted(lang, st.Native) {
		out["degradedAnalysis"] = lang + " compiler-backed completion is not confirmed by this index; caller coverage may be incomplete. Re-index with native analysis enabled and check prism status"
	}
}

// partialGoPackages returns the import paths of packages the last index
// type-checked only partially ("go: project import P type-checked
// partially: FILE:LINE:COL: error") that hold a declaration, family member
// or caller of r, and the first such error.
func (h *Handler) partialGoPackages(native []string, r *grove.ChangeImpactResult) ([]string, string) {
	dirs := map[string]bool{}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family, r.Callers} {
		for _, s := range group {
			dirs[path.Dir(normalizePath(s.FilePath))] = true
		}
	}
	var pkgs []string
	first := ""
	seen := map[string]bool{}
	for _, d := range native {
		rest, ok := strings.CutPrefix(d, "go: project import ")
		if !ok {
			continue
		}
		pkg, detail, ok := strings.Cut(rest, " type-checked partially: ")
		if !ok || seen[pkg] {
			continue
		}
		file := detail
		if i := strings.Index(file, ".go:"); i >= 0 {
			file = file[:i+3]
		}
		written := file // as the diagnostic spells it, for cutting the error text
		rel := file
		root := h.Root
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real // /var vs /private/var on macOS
		}
		if real, err := filepath.EvalSymlinks(file); err == nil {
			file = real
		}
		if r, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		if !dirs[path.Dir(normalizePath(rel))] {
			continue
		}
		seen[pkg] = true
		pkgs = append(pkgs, pkg)
		if first == "" {
			first = strings.TrimSpace(strings.TrimPrefix(detail, written))
			first = strings.TrimLeft(first, ":0123456789 ")
			if len(first) > 160 {
				first = first[:160] + "…"
			}
		}
	}
	return pkgs, first
}

var nativePassChecked = map[string]bool{"go": true, "java": true, "typescript": true, "rust": true}

func nativePassCompleted(lang string, diagnostics []string) bool {
	for _, diagnostic := range diagnostics {
		switch lang {
		case "go":
			if strings.HasPrefix(diagnostic, "go: resolved ") && strings.Contains(diagnostic, "native call edge") {
				return true
			}
		case "java":
			if strings.HasPrefix(diagnostic, "java: javac attributed ") {
				return true
			}
		case "rust":
			// rust-analyzer's call edges (grove v0.63.8+). "rust: resolved N
			// native call edge(s)" prints without it and proves nothing.
			if strings.HasPrefix(diagnostic, "rust: rust-analyzer resolved ") {
				return true
			}
		case "typescript":
			// "not applicable": a plain JavaScript project with no typescript;
			// tree-sitter is the complete analysis there, not a degraded one.
			if strings.HasPrefix(diagnostic, "js-ts: resolved ") ||
				strings.HasPrefix(diagnostic, "js-ts: skipped: not applicable:") {
				return true
			}
		}
	}
	return false
}

// impactRelaySites is an answer-shaped, de-duplicated inventory for result
// sets small enough to relay without materially duplicating a wide payload.
// Detailed groups remain authoritative for overloads and evidence.
func impactRelaySites(r *grove.ChangeImpactResult, limit int) []string {
	sites := impactSites(r, true)
	if len(sites) == 0 || len(sites) > limit {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(sites))
	for _, site := range sites {
		name := site.Name
		if name == "" {
			name = displayQN(site)
		}
		path := normalizePath(site.FilePath)
		if path == "" && name == "" {
			continue
		}
		// Preserve overloads and same-named sites. impactSites intentionally
		// distinguishes these by file+line+kind; the answer-shaped relay must
		// retain the same identity or its advertised canonical inventory is
		// silently incomplete.
		identity := fmt.Sprintf("%s:%d:%s:%s", path, site.Span.Start, site.Kind, name)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		label := fmt.Sprintf("%s:%d:%s", path, site.Span.Start, name)
		if site.Kind != "" {
			label += " [" + site.Kind + "]"
		}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// markNameMatchedSites puts the doubt where it belongs once the compiler-backed
// answer is complete: on the site found only by name, not on the answer. A
// whole-answer label ("heuristic", or any softer word) told the agent the
// inventory might be wrong when it was complete, and it re-grepped the
// repository (hono Router.match: 13 of 14 callers compiler-resolved, the
// 14th in a file outside every tsconfig).
func markNameMatchedSites(out map[string]any, r *grove.ChangeImpactResult) {
	if safe, _ := out["safeToClaimComplete"].(bool); !safe || len(r.NameMatchedCallers) == 0 {
		return
	}
	named := map[string]bool{}
	for _, c := range r.Callers {
		for _, id := range r.NameMatchedCallers {
			if c.ID == id {
				named[fmt.Sprintf("%s:%d:%s", normalizePath(c.FilePath), c.Span.Start, c.Name)] = true
			}
		}
	}
	if relay, ok := out["relaySites"].([]string); ok {
		for i, label := range relay {
			if named[strings.SplitN(label, " [", 2)[0]] {
				relay[i] = label + " [found by name in a file the compiler does not analyze]"
			}
		}
	}
	delete(out, "callerCoverage")
	delete(out, "hasHeuristicRefs")
	out["callerEvidence"] = fmt.Sprintf("Every caller is listed: %d resolved by the compiler or AST, %d found by name (marked in relaySites).",
		len(r.Callers)-len(named), len(named))
}

// impactCallerEvidence says how many callers the compiler or the AST resolved
// and names the ones reached only by name matching, instead of labelling the
// whole answer by its weakest edge. Empty when every caller is resolved.
func impactCallerEvidence(r *grove.ChangeImpactResult) string {
	if r == nil || len(r.Callers) == 0 {
		return ""
	}
	// Only a compiler resolves a call. grove's "AST-narrowed" also covers a
	// receiver it could not type (an untyped parameter's .get bound to the
	// only get in the repository), so counting those as resolved claimed
	// "6 of 8 resolved" over positions marked by name (hono, 2026-10-14).
	compiled := 0
	for _, c := range r.Callers {
		if len(r.CallerLines[c.ID]) > 0 {
			compiled++
		}
	}
	named := impactNameOnlyCallers(r)
	switch {
	case compiled > 0 && compiled < len(r.Callers):
		note := fmt.Sprintf("%d of %d callers resolved by the compiler; the others are matched by name and receiver inference", compiled, len(r.Callers))
		if named != "" {
			note += "; name-matched only (not resolved by a compiler; check each, some may be unrelated namesakes): " + named
		}
		return note
	case named != "":
		// No compiler ran: the language note already says every caller is
		// inferred; name the weakest ones.
		return "name-matched only (not resolved by a compiler; check each, some may be unrelated namesakes): " + named
	}
	return ""
}

// impactNameOnlyCallers lists the callers grove matched by name alone.
func impactNameOnlyCallers(r *grove.ChangeImpactResult) string {
	if len(r.NameMatchedCallers) == 0 {
		return ""
	}
	byID := make(map[string]grove.SymbolRecord, len(r.Callers))
	for _, c := range r.Callers {
		byID[c.ID] = c
	}
	var named []string
	for _, id := range r.NameMatchedCallers {
		if c, ok := byID[id]; ok {
			named = append(named, fmt.Sprintf("%s:%d:%s", normalizePath(c.FilePath), c.Span.Start, displayQN(c)))
		}
	}
	if len(named) == 0 {
		return ""
	}
	sort.Strings(named)
	return strings.Join(named, ", ")
}

// addIndexedCompletenessSafety keeps task-shaped graph tools honest about the
// boundary shared by change-impact, missing-implementations, and rename-plan.
// Their engine completeness is useful inside the indexed project, but none can
// prove the absence of runtime, generated, or external consumers.
func addIndexedCompletenessSafety(out map[string]any) {
	out["completenessScope"] = "indexed-project-only"
	out["safeToClaimComplete"] = false
	out["scopeBoundary"] = indexedScopeBoundary
}

// danglingRerun is Grove's closing "re-run with one of these" phrase, with
// any parenthetical after it.
var danglingRerun = regexp.MustCompile(`(?m)[ \t]*re-run with one of these[^\n]*$`)

// ambiguousImpactCalls lists each candidate of a short ambiguity as a call the
// agent can send back unchanged. A bare "re-run with one of these" was where
// agents abandoned change_impact (7 of 17 ambiguous answers, Aug-Sep 2026).
func ambiguousImpactCalls(err error) string {
	var b strings.Builder
	for _, m := range ambiguousCandidateLine.FindAllStringSubmatch(err.Error(), -1) {
		fmt.Fprintf(&b, "  {\"op\":\"change_impact\",\"args\":{\"name\":%q,\"symbol_file\":%q}}\n", m[1], m[2])
	}
	return strings.TrimRight(b.String(), "\n")
}

const (
	impactRelayNote      = "Copy relaySites when reporting the affected-site inventory; do not manually reconstruct a partial list from the grouped evidence below."
	indexedScopeBoundary = "Do not claim global completeness from this result. It closes the indexed project graph only; " +
		"external consumers, generated code, reflection/runtime dispatch, and ambiguous receiver evidence remain outside proof."
	compilerScopeBoundary = "Complete for this repository: the compiler-backed analysis ran and the family is closed; every caller is listed. " +
		"Not covered: reflection/runtime dispatch and consumers outside this repository."
	compilerEvidenceNote = "Call expressions below are shown as evidence for each caller."
	indexedEvidenceNote  = "Indexed call expressions below are name-matched within reported callers, not independent receiver-resolution proof. Snippet limits never remove sites. Inspect ambiguous receivers, omitted evidence, or behavior needed by the task."
)

// impactResolution is how a change-impact query became a result: the
// result, and the note naming the fallback that produced it, or a choice
// returned instead of a result.
type impactResolution struct {
	result         *grove.ChangeImpactResult
	choice         map[string]any // a cross-language choice, answered as-is
	inferenceNote  string         // an external method family was inferred
	inheritedNote  string         // the inherited declaration answered
	familyPickNote string         // one override family covered the candidates
	modulePathNote string         // a module path, not Type.method
}

// resolveImpact turns a query into a change set. It tries, in order: the
// name as given; the inherited declaration (an override not written yet);
// a cross-language choice; a module path; for a wide bare member, the local
// contract owner, the override families or an inferred external family; a
// signature among a few candidates; and one family covering them. An error
// that survives every step is explained by explainImpactError.
func (h *Handler) resolveImpact(ctx context.Context, args map[string]any, query string) (impactResolution, error) {
	var res impactResolution
	file := stringArg(args, "file", "")
	r, err := h.Grove.ChangeImpactScoped(ctx, query, file)
	if err == nil {
		if qerr := h.qualifierMismatch(query, r); qerr != nil {
			return res, qerr
		}
		res.result = r
		return res, nil
	}
	// CON-10: a file path that did not resolve as a name gets a usage
	// error naming the forms that work, not "no type named <stem>".
	if ferr := h.fileNotSymbolError(query); ferr != nil {
		return res, ferr
	}
	if strings.Contains(err.Error(), "declares no method") {
		// The override being added does not exist yet: answer with the
		// inherited declaration's change set instead of a dead end.
		if ir, note, ok := h.inheritedImpact(ctx, query, file); ok {
			res.result, res.inheritedNote = ir, note
			return res, nil
		}
	}
	if choice, ok := h.crossLanguageImpactChoice(ctx, query, err); ok {
		res.choice = choice
		return res, nil
	}
	r, err = h.resolveImpactFallbacks(ctx, args, query, err, &res)
	if err != nil {
		return res, h.explainImpactError(ctx, query, err)
	}
	res.result = r
	return res, nil
}

// resolveImpactFallbacks tries the module-path, wide-member, signature and
// family steps of resolveImpact after the name itself did not resolve.
func (h *Handler) resolveImpactFallbacks(ctx context.Context, args map[string]any, query string, err error, res *impactResolution) (*grove.ChangeImpactResult, error) {
	var r *grove.ChangeImpactResult
	// A wide bare member or a qualified external interface has no local
	// declaration to anchor. Infer the compatible local method family once
	// and union file-scoped resolved impacts inside this call, instead of
	// making the agent guess dozens of receivers across dozens of turns.
	wide := ambiguousCandidateCount.FindStringSubmatch(err.Error())
	isWide := false
	if len(wide) == 2 {
		count, _ := strconv.Atoi(wide[1])
		isWide = count >= wideMemberAmbiguityThreshold
	}
	if strings.Contains(err.Error(), "no type named") && strings.Contains(query, ".") {
		// werkzeug.utils.redirect: a module path, not Type.method.
		if mr, note, ok := h.modulePathImpact(ctx, query); ok {
			r, err, res.modulePathNote = mr, nil, note
		}
	}
	externalMissing := err != nil && strings.Contains(err.Error(), "no type named") && strings.Contains(query, ".")
	signature := stringArg(args, "signature", "")
	switch {
	case err == nil:
		// resolved above
	case externalMissing:
		if inferred, note, inferErr := h.inferExternalMethodImpact(ctx, query, signature); inferErr == nil {
			r, err, res.inferenceNote = inferred, nil, note
		}
	case isWide:
		r, err = h.resolveWideMember(ctx, query, signature, err, res)
	case signature != "" && len(wide) == 2:
		// A few candidates: the signature may pick one of grove's own.
		if picked, ok := h.pickAmbiguousBySignature(ctx, err, query, signature); ok {
			r, err = h.Grove.ChangeImpactScoped(ctx, picked.QualifiedName, picked.FilePath)
		}
	}
	if err != nil && len(wide) == 2 && !isWide {
		if fr, note, ok := h.pickAmbiguousByFamily(ctx, err); ok {
			r, err, res.familyPickNote = fr, nil, note
		}
	}
	return r, err
}

// resolveWideMember answers a bare member name with many candidates.
func (h *Handler) resolveWideMember(ctx context.Context, query, signature string, err error, res *impactResolution) (*grove.ChangeImpactResult, error) {
	// A bare name the project itself declares in an interface is a
	// local contract: guessing one "external" family from every
	// same-shaped method merges unrelated contracts (gin wide bed
	// 2026-09-27: Binding.Name and BindingUri.Name, both local, came
	// back as one family plus runtime.Func.Name callers). Ask for the
	// owner instead; the external guess stays for names no local
	// interface declares.
	if owners := h.localContractOwners(ctx, query); len(owners) > 0 {
		return nil, localContractError(query, owners, err)
	}
	families, ok, capped := h.partitionLocalFamiliesCapped(ctx, query)
	switch {
	case capped:
		// Too many candidates to partition: keep the ambiguity rather
		// than infer one "external" family from parameter shapes,
		// which silently dropped local families.
		return nil, err
	case ok:
		// Nominal language: the graph already knows the families.
		// One family is the answer; several are listed, never guessed.
		if len(families) == 1 && families[0].result != nil {
			res.familyPickNote = fmt.Sprintf("the name matched %d declarations; they are one override family rooted at %s, so this is its change set",
				families[0].members, displayQN(families[0].root))
			return families[0].result, nil
		}
		return nil, familiesAmbiguityError(query, families)
	}
	if inferred, note, inferErr := h.inferExternalMethodImpact(ctx, query, signature); inferErr == nil {
		res.inferenceNote = note
		return inferred, nil
	}
	return nil, err
}

// explainImpactError turns an unresolved change-impact error into the
// answer an agent can act on.
func (h *Handler) explainImpactError(ctx context.Context, query string, err error) error {
	// "declares no method" is the dead-end that abandons agents:
	// transcript analysis (2026-09-02, grove wide-bed cell) caught an
	// agent asking change_impact about a method it had ALREADY deleted
	// two edits earlier (the steering says before-edit; it called it
	// after), getting this error, and never touching prism again for
	// the task's remaining 7 files. The error was honest but terminal.
	// Make the retry obvious in-band: list what the type actually
	// declares (from prism's own index, no grove change needed) and
	// name the already-edited-it failure mode explicitly.
	if strings.Contains(err.Error(), "declares no method") {
		if enriched := h.enrichNoMethodError(ctx, query, err); enriched != nil {
			return enriched
		}
	}
	if strings.Contains(err.Error(), "is ambiguous —") {
		if enriched := enrichAmbiguousImpactError(query, err); enriched != nil {
			return enriched
		}
		if calls := ambiguousImpactCalls(err); calls != "" {
			// Grove ends with a bare "re-run with one of these"; the
			// calls below say it with the exact call, so the phrase goes.
			msg := strings.TrimRight(danglingRerun.ReplaceAllString(err.Error(), ""), " \n")
			return fmt.Errorf("%s\nThese are different functions; pick one and call it by its full name:\n%s", msg, calls)
		}
	}
	if note := h.unindexedNote(ctx, leafOf(query)); note != "" {
		return fmt.Errorf("%w\n%s", err, note)
	}
	return err // grove already prefixes; re-wrapping tripled the message
}
