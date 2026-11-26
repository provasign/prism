package core

// prism_verify — the diff-completeness verifier. Given the diff between a
// git base and the working tree (an agent's output, a PR), compute what the
// change set SHOULD have been and report what is missing:
//
//   - signature-changed / renamed symbols -> change_impact -> required
//     sites; required sites the diff did not touch are MISSED SITES,
//     line-precise where the graph has AST call sites.
//   - component dependencies whose entire evidence originates in changed
//     files (NEW cross-component dependencies introduced by this diff),
//     plus declared arch rules with the same tier-aware gating as
//     prism_arch_check.
//
// A model's "I updated all the callers" is a probabilistic assertion; this
// is the deterministic check of it. Verdict: complete | incomplete | clean.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/view"
)

type lineRange struct{ start, end int }

// gitRefRe constrains a caller-supplied git base to characters that appear in
// real revision specifiers (branches, tags, SHAs, HEAD~3, origin/main,
// v1.2.3^{commit}). It deliberately EXCLUDES a leading '-' and the '=' and
// whitespace that git-diff option injection needs.
var gitRefRe = regexp.MustCompile(`^[A-Za-z0-9_./^~@{}:-]+$`)

// validGitBase rejects a base ref before it is placed on a git command line.
// `base` is an agent/MCP-controlled parameter (prism_verify / prism_prepare);
// without this, a value like "--output=/path" is parsed by `git diff` as an
// option (it sits before the "--" separator) and truncates/overwrites an
// arbitrary file — an arbitrary-write primitive from untrusted input. The
// leading-'-' rejection is the load-bearing check; the charset is hardening.
func validGitBase(base string) bool {
	return base != "" && !strings.HasPrefix(base, "-") && gitRefRe.MatchString(base)
}

// gitPrefix returns the work-root's path relative to the git repository root
// ("" when they coincide). Diff and show paths are repo-root-relative; every
// symbol path Prism holds is work-root-relative — a corpus rooted in a
// subdirectory (guava/guava) silently mismatches every path without this
// (measured: verify passed "complete" on guava with 38 forgotten files
// because gitShow returned nil for every changed file).
func gitPrefix(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitChangedRanges parses `git diff --unified=0 <base>` into after-side
// changed line ranges per file (work-root-relative paths), then adds untracked
// non-ignored files as whole-file ranges. A pure deletion is recorded as a
// one-line touch marker at its after-side position.
// gitDiffError names why a git diff against base failed. Outside a
// repository git diff falls back to --no-index and exits 129 with its
// usage text; an unknown ref exits 128. Both used to surface as a bare
// "exit status N" (BUG-16).
func gitDiffError(root, base string, err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Errorf("git diff %s: %w", base, err)
	}
	if out, rerr := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Output(); rerr != nil || strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("not a git repository: %s (verify and test-plan compare the working tree with a git ref)", root)
	}
	if ee.ExitCode() == 128 {
		if verr := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}").Run(); verr != nil {
			return fmt.Errorf("unknown revision %q: pass a branch, tag or commit with --base", base)
		}
	}
	msg := strings.TrimSpace(string(ee.Stderr))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git diff %s: %s", base, msg)
}

func gitChangedRanges(root, base string) (map[string][]lineRange, error) {
	if !validGitBase(base) {
		return nil, fmt.Errorf("invalid git base ref")
	}
	// --end-of-options: belt-and-suspenders so a ref that slipped the charset
	// check can never be parsed as a git-diff option (arbitrary-write via
	// --output=). validGitBase is the primary guard; this is defense in depth.
	cmd := exec.Command("git", "-C", root, "diff", "--unified=0", "--no-color", "--end-of-options", base, "--", ".")
	out, err := cmd.Output()
	if err != nil {
		return nil, gitDiffError(root, base, err)
	}
	prefix := gitPrefix(root)
	changed := map[string][]lineRange{}
	var cur string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			cur = strings.TrimPrefix(strings.TrimPrefix(line, "+++ b/"), prefix)
		case strings.HasPrefix(line, "+++ /dev/null"):
			cur = "" // deleted file: no after side to touch-check
		case strings.HasPrefix(line, "@@ ") && cur != "":
			// @@ -l[,s] +c[,d] @@
			fields := strings.Fields(line)
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "+") {
					continue
				}
				nums := strings.SplitN(strings.TrimPrefix(f, "+"), ",", 2)
				c, _ := strconv.Atoi(nums[0])
				d := 1
				if len(nums) == 2 {
					d, _ = strconv.Atoi(nums[1])
				}
				if d == 0 { // pure deletion: mark the position
					if c < 1 {
						c = 1
					}
					changed[cur] = append(changed[cur], lineRange{c, c})
				} else {
					changed[cur] = append(changed[cur], lineRange{c, c + d - 1})
				}
				break
			}
		}
	}
	untracked, err := exec.Command("git", "-C", root, "ls-files", "--others",
		"--exclude-standard", "-z", "--", ".").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others: %w", err)
	}
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name == "" {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == ".grove" || strings.HasPrefix(rel, ".grove/") {
			continue // verifier/index state, created by the refresh above
		}
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if readErr != nil {
			return nil, fmt.Errorf("read untracked file %s: %w", rel, readErr)
		}
		end := strings.Count(string(content), "\n") + 1
		changed[rel] = []lineRange{{start: 1, end: end}}
	}
	return changed, nil
}

// gitDeletedFiles lists files removed relative to base (work-root-relative).
//
// gitChangedRanges cannot see them: a deletion's after-side is /dev/null, so
// the file never becomes a key in its map. That made a deletion-only change
// read as verdict "clean" with exit 0 (measured: deleting a file whose
// functions were still called verified clean), and it hid the old path of a
// moved file, so `git mv` + a signature change was entirely unverified.
func gitDeletedFiles(root, base string) []string {
	if !validGitBase(base) {
		return nil
	}
	out, err := exec.Command("git", "-C", root, "diff", "--name-only",
		"--diff-filter=D", "--no-color", "--end-of-options", base, "--", ".").Output()
	if err != nil {
		return nil
	}
	prefix := gitPrefix(root)
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, strings.TrimPrefix(l, prefix))
		}
	}
	sort.Strings(files)
	return files
}

func gitShow(root, base, relPath string) []byte {
	if !validGitBase(base) {
		return nil
	}
	out, err := exec.Command("git", "-C", root, "show", "--end-of-options", base+":"+gitPrefix(root)+relPath).Output()
	if err != nil {
		return nil // new file at base, or not tracked
	}
	return out
}

func touched(changed map[string][]lineRange, file string, span grove.SpanInfo) bool {
	for _, r := range changed[file] {
		if r.start <= span.End && r.end >= span.Start {
			return true
		}
	}
	return false
}

func lineTouched(changed map[string][]lineRange, file string, line int) bool {
	for _, r := range changed[file] {
		if r.start <= line && line <= r.end {
			return true
		}
	}
	return false
}

// missedSite is one required-but-untouched change site.
type missedSite struct {
	Symbol        string `json:"symbol"`
	QualifiedName string `json:"qualifiedName"`
	File          string `json:"file"`
	Line          int    `json:"line"`
	Kind          string `json:"kind"`
	BecauseOf     string `json:"becauseOf"` // the signature change that requires this site
	Detail        string `json:"detail"`
}

// missedSiteMaps is the result shape. The struct slice went straight into
// the result until 2026-09-06, and the text renderer's anySlice does not
// know struct slices — so the MISSED SITES section was silently dropped
// from the text the agent reads while the verdict still said "incomplete".
// (The JSON fallback never triggered: the key was known, its value simply
// rendered as nothing.) Maps are what every renderer here understands.
func missedSiteMaps(ms []missedSite) []map[string]any {
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]any{
			"symbol": m.Symbol, "qualifiedName": m.QualifiedName,
			"file": m.File, "line": m.Line, "kind": m.Kind,
			"becauseOf": m.BecauseOf, "detail": m.Detail,
		})
	}
	return out
}

func (h *Handler) toolVerify(ctx context.Context, args map[string]any) (any, error) {
	base := stringArg(args, "base", "HEAD")
	strict := boolArg(args, "strict")

	// FAST PATH — removed_symbols: a cheap mid-loop residual-reference
	// check, no diff walk. Measured (BACKLOG addendum #8, 2026-09-02, two
	// ~210-call removal-task sessions): ~45-48 Bash greps per session —
	// ~22% of ALL tool calls — re-checked the same 8-12 removed
	// identifiers ~15 times, hand-rolling exactly this; prism_verify ran
	// once, at call 210 of 212, because the full gate is priced as an
	// exit check, not a loop check. This path answers "which of these
	// still have references?" in one call agents can afford mid-loop.
	if syms := stringsArg(args, "removed_symbols"); len(syms) > 0 {
		return h.verifyRemovedSymbols(ctx, syms)
	}

	// Refresh the index FIRST (delta — cheap). Verify compares each changed
	// file's base version against the live index, so a stale index shows no
	// contract change and the gate returns "complete" on genuinely broken
	// code (measured: a signature change without a reindex verified clean,
	// exit 0). prism_drift already refreshes for exactly this reason; verify
	// — the CI gate — must not be the one surface that trusts stale data.
	var staleNote string
	var refreshErrors []string
	if indexed, ierr := h.Grove.Index(ctx, h.Root); ierr != nil {
		staleNote = "index refresh failed (" + ierr.Error() +
			"); results computed against a possibly stale index"
	} else {
		refreshErrors = indexed.Errors
	}

	changed, deletedFiles, err := verifyDiff(h.Root, base)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 && len(deletedFiles) == 0 {
		return map[string]any{"verdict": "clean", "base": base,
			"gateFailure": false, "note": "no changes vs " + base}, nil
	}
	changedFiles := make([]string, 0, len(changed))
	for f := range changed {
		changedFiles = append(changedFiles, f)
	}
	sort.Strings(changedFiles)
	// Only a refresh error on a changed file can hide a contract change. A
	// symlinked doc the indexer skipped (urfave/cli docs/CODE_OF_CONDUCT.md)
	// turned 5 of 32 forced-run verifies into "review" on unrelated edits.
	if errs := refreshErrorsFor(refreshErrors, changed); len(errs) > 0 {
		staleNote = "index refresh incomplete: " + strings.Join(errs, "; ")
	}

	s := &verifyState{h: h, base: base, changed: changed,
		seen: map[string]bool{}, staleSeen: map[string]bool{}}
	// 1) Seed stage: which changed symbols altered their contract?
	s.verifyCollectSeeds(ctx, changedFiles, deletedFiles)

	if staleNote != "" {
		// A failed refresh means every comparison below ran against a
		// possibly stale index — the verdict must degrade to "review", not
		// read as an authoritative "complete" with a footnote.
		s.notes = append(s.notes, staleNote)
		s.unverifiedSeeds = append(s.unverifiedSeeds, staleNote)
	}
	sigChanges := verifySignatureChanges(s.seeds)
	// 2) Per-kind checks: for each contract change, the engine computes the
	// required set; anything the diff did not touch is a missed site.
	s.verifyCheckSeeds(ctx, changedFiles, deletedFiles)

	return s.verifyReport(ctx, strict, changedFiles, sigChanges), nil
}

// verifyReport assembles the result: dependency and architecture checks,
// the fail-closed verdict, test coverage and linked-repository consumers.
func (s *verifyState) verifyReport(ctx context.Context, strict bool, changedFiles []string, sigChanges []map[string]any) map[string]any {
	h := s.h
	// 3) Cross-component dependency CANDIDATES: induced edges whose every
	// evidence site sits inside code this diff touched (line-level, not
	// file-level — a pre-existing edge elsewhere in a touched file must not
	// fire). No base-graph comparison is performed, so these are candidates
	// for review, never asserted as "introduced".
	newDeps, archStatus, archIntroduced, depErr := h.verifyDependencies(ctx, s.changed)
	if depErr != nil {
		// FAIL CLOSED: without the graph, neither new cross-component
		// dependencies nor arch rules were checked. Silently reporting
		// "no-rules" and an empty dependency list read as a pass.
		s.unverifiedSeeds = append(s.unverifiedSeeds,
			"dependency and architecture checks did not run: "+depErr.Error())
	}

	// Verdict, fail-closed: incomplete beats review beats complete. A
	// contract change we could not verify can never yield "complete".
	verdict := verifyVerdict(len(s.missed), len(s.unverifiedSeeds), archStatus)
	h.Ledger.RecordCall("prism_verify")
	testCoverageOut := verifyTestCoverageOut(s.testCoverage)
	// Consumers of a changed contract in linked repositories.
	seedSyms := make([]grove.SymbolRecord, 0, len(s.seeds))
	for _, sd := range s.seeds {
		seedSyms = append(seedSyms, sd.sym)
	}
	linkedOut := h.verifyLinkedConsumers(ctx, seedSyms)
	result := map[string]any{
		"verdict":           verdict,
		"gateFailure":       verdict == "incomplete" || (strict && (verdict == "review" || linkedOut["linkedVerdict"] != nil)),
		"base":              s.base,
		"changedFiles":      changedFiles,
		"signatureChanges":  sigChanges,
		"missedSites":       missedSiteMaps(s.missed),
		"unverifiedSeeds":   s.unverifiedSeeds,
		"contentAdvisories": s.contentAdvisories,
		"newDependencies":   newDeps,
		"archStatus":        archStatus,
		"archIntroduced":    archIntroduced,
		"notes":             s.notes,
		// Informational only, never affects verdict/gateFailure — see
		// verifyState.testCoverage's declaration comment for why body-only
		// changes need this separately from the seed/missedSites pipeline.
		"testCoverage": testCoverageOut,
	}
	for k, v := range linkedOut {
		result[k] = v
	}
	return result
}

// paramList extracts a normalized parameter-list string from a signature:
// the parenthesized list that FOLLOWS the method name where the name is
// present (Go method signatures put the RECEIVER in the first parens —
// "func (p *Plugin) QueryData(ctx ...)" — so first-paren extraction compares
// receivers, which differ across every family member; measured: it zeroed
// the base-contract family on grafana-querydata), else the first-paren list.
// Whitespace-collapsed. Family members of one contract share it verbatim
// across modifiers and receivers; overloads differ in it.
func paramList(sig string) string {
	return paramListAfter(sig, methodLeafInSig(sig))
}

// paramListNamed is paramList anchored on a known method name — preferred
// when the caller has the symbol (immune to name-detection ambiguity).
func paramListNamed(sig, name string) string {
	if p := paramListAfter(sig, name); p != "" || name == "" {
		return p
	}
	return paramList(sig)
}

func paramListAfter(sig, name string) string {
	start := 0
	if name != "" {
		if k := strings.Index(sig, name+"("); k >= 0 {
			start = k + len(name)
		} else if k := strings.Index(sig, name+" ("); k >= 0 {
			start = k + len(name) + 1
		}
	}
	i := strings.Index(sig[start:], "(")
	if i < 0 {
		return ""
	}
	i += start
	// Match the closing paren of THIS list, not the last ')' in the
	// signature (Go return tuples, TS return types add later parens).
	depth := 0
	for j := i; j < len(sig); j++ {
		switch sig[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.Join(strings.Fields(sig[i+1:j]), " ")
			}
		}
	}
	return ""
}

// methodLeafInSig makes a best-effort guess at the identifier immediately
// preceding a '(' that is not the receiver: the LAST "ident(" occurrence
// wins for Go ("func (p *Plugin) Name(..."), and is simply the name for
// Java/TS/Python.
func methodLeafInSig(sig string) string {
	// The FIRST paren group directly preceded by an identifier is the
	// parameter list in every language; Go receiver parens are preceded by
	// "func " (a space), so they are skipped naturally.
	for k := 0; k < len(sig); k++ {
		if sig[k] != '(' {
			continue
		}
		e := k
		s := e
		for s > 0 && isIdentByte(sig[s-1]) {
			s--
		}
		if s < e {
			return sig[s:e]
		}
	}
	return ""
}

// blockText returns a declaration block's source text: the symbol's RawText
// when the record carries it, else the span lines sliced from the base
// version (git show) or the working tree.
func blockText(root, base, relPath string, sym *grove.SymbolRecord, fromBase bool) string {
	if sym == nil {
		return ""
	}
	if sym.RawText != "" {
		return sym.RawText
	}
	var content []byte
	if fromBase {
		content = gitShow(root, base, relPath)
	} else {
		// os.ReadFile through the root check, not `cat` (unix-only, and
		// empty on Windows; SEC-07).
		if abs, _, err := safePathWithinRoot(root, relPath); err == nil {
			if out, err := os.ReadFile(abs); err == nil {
				content = out
			}
		}
	}
	if content == nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	s, e := sym.Span.Start-1, sym.Span.End
	if s < 0 || s >= len(lines) {
		return ""
	}
	if e > len(lines) {
		e = len(lines)
	}
	return strings.Join(lines[s:e], "\n")
}

type memberChange struct {
	name      string
	beforeSig string
	afterSig  string
}

// memberAdditionsRemovals returns the member names a declaration block's
// body gained and lost, sorted. It reads members the way
// memberContractChanges does: one signature line per member name.
// splitInlineBody puts each member of a one-line body on its own line, so
// the line-based member diff sees `interface{ A(); B() }` as two members.
func splitInlineBody(text string) string {
	r := strings.NewReplacer("{", "{\n", "}", "\n}", ";", "\n")
	return r.Replace(text)
}

func memberAdditionsRemovals(beforeText, afterText string) (added, removed []string) {
	if beforeText == "" || afterText == "" {
		return nil, nil
	}
	names := func(text string) map[string]bool {
		out := map[string]bool{}
		for _, ln := range strings.Split(text, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "#") {
				continue
			}
			if leaf := methodLeafInSig(t); leaf != "" && strings.Contains(t, leaf+"(") {
				out[leaf] = true
			}
		}
		return out
	}
	b, a := names(beforeText), names(afterText)
	for n := range a {
		if !b[n] {
			added = append(added, n)
		}
	}
	for n := range b {
		if !a[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// memberContractChanges line-diffs two declaration-block bodies and returns
// the members whose parameter lists changed: lines shaped like a member
// signature ("ident(") that appear on one side but not the other, paired by
// leaf name. Additions and removals without a pair are ignored — a NEW
// member has no dependents yet, and a REMOVED one is a rename_plan concern.
func memberContractChanges(beforeText, afterText string) []memberChange {
	if beforeText == "" || afterText == "" {
		return nil
	}
	sigLines := func(text string) map[string]string { // leaf -> normalized line
		out := map[string]string{}
		for _, ln := range strings.Split(text, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "#") {
				continue
			}
			leaf := methodLeafInSig(t)
			if leaf == "" || !strings.Contains(t, leaf+"(") {
				continue
			}
			// One line per leaf: overloaded members inside one block would
			// collide — keep the first and let the paramList comparison
			// decide; a mismatch still seeds the member, which is safe
			// (base-contract match filters by the OLD list).
			if _, dup := out[leaf]; !dup {
				out[leaf] = strings.Join(strings.Fields(t), " ")
			}
		}
		return out
	}
	b, a := sigLines(beforeText), sigLines(afterText)
	var out []memberChange
	for leaf, bl := range b {
		al, ok := a[leaf]
		if !ok || bl == al {
			continue
		}
		bp, ap := paramListNamed(bl, leaf), paramListNamed(al, leaf)
		if bp == "" || bp == ap {
			continue
		}
		out = append(out, memberChange{name: leaf, beforeSig: bl, afterSig: al})
	}
	return out
}

// paramsMatch compares two normalized parameter lists position-by-position.
// Exact string equality passes; otherwise both lists must have the same
// arity and each position must match, where a base parameter that
// references one of the declaration's type variables (or, absent declared
// ones, a lone single-uppercase-letter token — the T/K/V convention) is a
// wildcard for that position.
func paramsMatch(base, cand, lang string, typeVars []string) bool {
	if base == cand {
		return true
	}
	if cand == "" {
		return false
	}
	bp, cp := splitParams(base), splitParams(cand)
	if len(bp) != len(cp) {
		return false
	}
	isVar := func(tok string) bool {
		for _, tv := range typeVars {
			if tok == tv {
				return true
			}
		}
		return len(typeVars) == 0 && len(tok) == 1 && tok[0] >= 'A' && tok[0] <= 'Z'
	}
	for i := range bp {
		if bp[i] == cp[i] {
			continue
		}
		// Parameter NAMES differ freely across an override family
		// (`SerializerProvider serializers` vs `SerializerProvider
		// provider` — measured to exclude every jackson-serialize
		// override); compare the TYPE portion only.
		if paramType(bp[i], lang) == paramType(cp[i], lang) {
			continue
		}
		wild := false
		for _, tok := range strings.FieldsFunc(bp[i], func(r rune) bool {
			return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}) {
			if isVar(tok) {
				wild = true
				break
			}
		}
		if !wild {
			return false
		}
	}
	return true
}

// paramType reduces one normalized parameter to its type portion, by
// language convention: Java/C-family "Type name" -> drop the trailing
// identifier; Go "name Type" -> drop the leading identifier; TS/Swift
// "name: Type" -> the text after the colon; otherwise the parameter as-is
// (Python parameters carry no types worth comparing).
func paramType(p, lang string) string {
	p = strings.TrimSpace(p)
	if i := strings.Index(p, ":"); i >= 0 {
		return strings.TrimSpace(p[i+1:])
	}
	fields := strings.Fields(p)
	if len(fields) < 2 {
		return p
	}
	switch lang {
	case "go":
		return strings.Join(fields[1:], " ")
	case "java", "kotlin", "csharp", "cpp", "c":
		return strings.Join(fields[:len(fields)-1], " ")
	}
	return p
}

// splitParams splits a normalized parameter list on top-level commas
// (generics <>, nested parens, and brackets do not split).
func splitParams(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(list[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(list[start:]))
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}

// changeImpactFor resolves the required change set for a symbol. Methods go
// through change_impact (override family + callers, completeness-reported);
// bare functions fall back to the resolved caller set, reported as
// completeness "callers-only".
func (h *Handler) changeImpactFor(ctx context.Context, sym grove.SymbolRecord) (*grove.ChangeImpactResult, error) {
	var candidates []string
	if sym.QualifiedName != "" && strings.Contains(sym.QualifiedName, ".") {
		candidates = append(candidates, sym.QualifiedName)
	}
	if sym.ParentSymbol != "" {
		candidates = append(candidates, sym.ParentSymbol+"."+sym.Name)
	}
	candidates = append(candidates, sym.Name)
	var lastErr error
	for _, q := range candidates {
		r, err := h.Grove.ChangeImpactScoped(ctx, q, sym.FilePath)
		if err == nil && len(r.Declarations) > 0 {
			return r, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	// Never recover a failed scoped query by unioning global namesakes.
	// The base graph can still resolve a removed or renamed declaration.
	if lastErr == nil {
		lastErr = fmt.Errorf("no scoped impact declaration for %s in %s", displayQN(sym), sym.FilePath)
	}
	return nil, lastErr
}

var versionedTestSourceDir = regexp.MustCompile(`(?:^|/)src/test-(?:jdk|java|kotlin|scala|groovy|python|php|go|rust|js|ts)[0-9]*(?:/|$)`)

// isTestFilePath matches test files across the supported languages.
func isTestFilePath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasPrefix(base, "test_") ||
		strings.Contains(base, "_test.") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "tests.java") ||
		strings.HasSuffix(base, "test.cs") || strings.HasSuffix(base, "tests.cs") ||
		strings.HasSuffix(base, "test.php") || strings.HasSuffix(base, "tests.php") ||
		strings.HasPrefix(p, "test/") || strings.HasPrefix(p, "tests/") ||
		strings.HasPrefix(p, "__tests__/") ||
		strings.Contains(p, "/test/") || strings.Contains(p, "/tests/") ||
		strings.Contains(p, "/__tests__/") || strings.Contains(p, "src/test/") ||
		versionedTestSourceDir.MatchString(p)
}

// isVerifiedTestCaller reports whether a caller found via InboundCallers is
// a REAL test -- isTestFilePath alone is not enough, because that also
// matches mocks/fakes/stubs living in a test-shaped path (e.g. a hand-
// written _test.go helper that isn't itself a test function). A mock
// calling the real implementation it wraps is not evidence a test covers
// that implementation; only a genuine test file is.
func isVerifiedTestCaller(path string) bool {
	if !isTestFilePath(path) {
		return false
	}
	lp := strings.ToLower(path)
	return !strings.Contains(lp, "mock") && !strings.Contains(lp, "fake") &&
		!strings.Contains(lp, "stub") && !strings.Contains(lp, "/testdata/")
}

func displayQN(s grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

// staleRef is one surviving occurrence of a renamed symbol's OLD name.
type staleRef struct {
	file, enclosing, oldName, newName string
	line                              int
}

// staleOldNameRefs finds references to a renamed symbol's OLD name that
// survive in the post-edit tree. The reference layer matches by NAME, so it
// sees exactly what resolution-based impact cannot: call sites left pointing
// at a name that no longer exists.
//
// Conservative by construction — a rename is only actionable when the old
// name is genuinely gone:
//   - silent unless the symbol was renamed (before != nil, names differ)
//   - silent if the old name still RESOLVES to a declaration (an overload,
//     a same-named sibling, or a deliberately kept alias — then a surviving
//     reference is legitimate, not stale)
//   - comment/string occurrences are excluded by References itself
func (h *Handler) staleOldNameRefs(ctx context.Context, sym grove.SymbolRecord, before *grove.SymbolRecord) []staleRef {
	if h.Grove == nil || before == nil {
		return nil
	}
	oldName := before.Name
	newDisplay := sym.Name
	if oldName == "" || oldName == sym.Name {
		// A member of a renamed TYPE keeps its own leaf name (networkx:
		// `_dispatchable.__call__ renamed` — leaf `__call__` unchanged,
		// parent `_dispatch` -> `_dispatchable`). The broken references are
		// to the old PARENT, so fall back to it.
		oldParent, newParent := leafOf(before.ParentSymbol), leafOf(sym.ParentSymbol)
		if oldParent == "" || oldParent == newParent {
			return nil
		}
		oldName = oldParent
		newDisplay = newParent
		// re-anchor the callability test on the parent type
		if p, err := h.Grove.Resolve(ctx, newParent); err == nil && len(p) > 0 {
			sym.Kind, before.Kind = p[0].Kind, p[0].Kind
		} else {
			sym.Kind, before.Kind = "class", "class"
		}
	}
	// If something still declares the old name, surviving references are fine.
	if cands, err := h.Grove.Resolve(ctx, oldName); err == nil {
		for _, c := range cands {
			if !c.TestDouble {
				return nil
			}
		}
	}
	// Only CALL POSITION breaks. Measured 2026-08-25 (electrum): the bare
	// reference layer flagged 25 sites on the COMPLETE gold diff, because
	// `seed_type` survives legitimately as a variable, a kwarg and a dict
	// key — renaming the FUNCTION does not break any of those. A gate that
	// fires on a correct change is worse than no gate, so the filter is
	// mandatory, not a refinement.
	if !isCallable(sym.Kind) && !isCallable(before.Kind) {
		return nil
	}
	res, err := h.Grove.References(ctx, oldName)
	if err != nil {
		return nil
	}
	callRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldName) + `\s*\(`)
	out := make([]staleRef, 0, len(res.Refs))
	for _, r := range res.Refs {
		line := sourceLineAt(h.Root, r.File, r.Line)
		if line == "" || !callRe.MatchString(stripCommentsAndStringsLine(line)) {
			continue
		}
		out = append(out, staleRef{file: r.File, line: r.Line,
			enclosing: r.Enclosing, oldName: oldName, newName: newDisplay})
		if len(out) == 25 {
			break
		}
	}
	return out
}

// isCallable: kinds whose rename breaks CALL-position references. Classes
// count — measured on networkx, whose renamed `_dispatch` class is used as
// a decorator (`@_dispatch(...)`), so every stale use is a call the old
// name can no longer satisfy.
func isCallable(kind string) bool {
	switch kind {
	case "function", "method", "constructor", "class", "struct":
		return true
	}
	return false
}

// sourceLineAt reads one 1-based line from a repo-relative file; "" on any
// failure (a gate must degrade to silence, never to a false accusation).
func sourceLineAt(root, rel string, line int) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || len(b) > 4<<20 {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// stripCommentsAndStringsLine blanks quoted spans and trailing line comments
// so `"seed_type("` in a docstring cannot read as a call site.
func stripCommentsAndStringsLine(s string) string {
	var b strings.Builder
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			b.WriteByte(' ')
			continue
		case r == '\'' || r == '"' || r == '`':
			quote = r
			b.WriteByte(' ')
			continue
		case r == '#':
			return b.String()
		case r == '/' && i+1 < len(s) && s[i+1] == '/':
			return b.String()
		}
		b.WriteRune(r)
	}
	return b.String()
}

// verifyRemovedSymbols reports exact identifier mentions in searchable files.
// A bounded or interrupted scan must not claim that a removed name is clean.
func (h *Handler) verifyRemovedSymbols(ctx context.Context, syms []string) (any, error) {
	type residual struct {
		Symbol    string           `json:"symbol"`
		MatchedAs string           `json:"matchedAs,omitempty"`
		Count     int              `json:"count"`
		Sites     []map[string]any `json:"sites,omitempty"`
	}
	const perSymbolSiteCap = 20
	var out []residual
	clean := 0
	for _, s := range syms {
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("removed_symbols contains an empty identifier")
		}
		// A qualified name (Type.method) never appears at a call site, which
		// reads x.method(): matching it literally reported "clean" while every
		// caller remained (gin ResponseWriter.Status, 2026-10-22). Match the
		// final identifier instead and say so; some of its lines may name a
		// different symbol of the same name.
		needle := removedSymbolIdentifier(s)
		r := textsearch.Search(ctx, h.Root, needle, textsearch.Options{Exhaustive: true})
		// Exhaustive search still has a 10,000-hit per-file cap, a 100,000-hit
		// total cap, and a deadline. A file at the per-file cap may hide an
		// exact mention after thousands of longer-name matches.
		if r.TimedOut || r.Truncated || len(r.RejectedPaths) > 0 || len(r.Hits) >= 10000 {
			return nil, fmt.Errorf("removed-symbol check for %q is incomplete (timed out: %t, truncated: %t, returned hits: %d); inspect or retry", s, r.TimedOut, r.Truncated, len(r.Hits))
		}
		// Search is a literal substring pass: Foo also finds Foo1 and FooImpl.
		// Keep only whole identifier mentions, including those in comments and
		// docs, without treating a surviving longer name as a removed symbol.
		// Search previews long lines; read the full line before applying the
		// boundary rule so a mention past the preview is not missed.
		identifier := regexp.MustCompile(`(^|[^\pL\pN\pM_$])` + regexp.QuoteMeta(needle) + `($|[^\pL\pN\pM_$])`)
		res := residual{Symbol: s}
		if needle != s {
			res.MatchedAs = needle
		}
		linesByFile := map[string][]string{}
		for _, hit := range r.Hits {
			lines, ok := linesByFile[hit.File]
			if !ok {
				content, err := os.ReadFile(filepath.Join(h.Root, filepath.FromSlash(hit.File)))
				if err != nil {
					return nil, fmt.Errorf("removed-symbol check for %q cannot read %s: %w", s, hit.File, err)
				}
				lines = strings.Split(string(content), "\n")
				linesByFile[hit.File] = lines
			}
			if hit.Line < 1 || hit.Line > len(lines) {
				return nil, fmt.Errorf("removed-symbol check for %q cannot read %s:%d", s, hit.File, hit.Line)
			}
			line := strings.TrimRight(lines[hit.Line-1], "\r")
			span := identifier.FindStringIndex(line)
			if span == nil {
				continue
			}
			res.Count++
			if len(res.Sites) >= perSymbolSiteCap {
				continue
			}
			// Keep the actual identifier visible even when the search backend
			// returned only a prefix preview of a long line.
			if len(line) > 250 {
				start := max(0, span[0]-80)
				end := min(len(line), span[1]+80)
				line = line[start:end]
				if start > 0 {
					line = "…" + line
				}
				if end < len(lines[hit.Line-1]) {
					line += "…"
				}
			}
			res.Sites = append(res.Sites, map[string]any{
				"file": hit.File, "line": hit.Line, "text": line})
		}
		if res.Count > perSymbolSiteCap {
			res.Sites = append(res.Sites, map[string]any{
				"note": fmt.Sprintf("+%d more sites", res.Count-perSymbolSiteCap)})
		}
		if res.Count == 0 {
			clean++
		}
		out = append(out, res)
	}
	verdict := "exact identifier mentions remain"
	if clean == len(syms) {
		verdict = "clean — no exact identifier mentions remain"
	}
	// Linked repositories: the same whole-identifier pass, counted per
	// repository. A removal that is clean here and still named by a
	// consumer is not clean.
	var linkedLines []string
	linkedMentions := 0
	if h.Workspace != nil {
		for _, r := range h.Workspace.Roots {
			if why := r.Unusable(); why != "" {
				linkedLines = append(linkedLines, fmt.Sprintf("%s (%s): NOT SEARCHED: %s", r.ID, h.Workspace.DisplayPath(r), shortReason(why)))
			}
		}
		for _, r := range h.Workspace.Linked() {
			var parts []string
			for _, sym := range syms {
				c := textsearch.Count(ctx, r.Path, `\b`+regexp.QuoteMeta(removedSymbolIdentifier(sym))+`\b`,
					textsearch.Options{Regex: true, CaseSensitive: true})
				if c.TotalHits > 0 {
					linkedMentions += c.TotalHits
					parts = append(parts, fmt.Sprintf("%s %d line(s) in %d file(s)", sym, c.TotalHits, c.FilesMatched))
				}
			}
			if len(parts) > 0 {
				linkedLines = append(linkedLines, fmt.Sprintf("%s (%s): %s", r.ID, h.Workspace.DisplayPath(r), strings.Join(parts, "; ")))
			}
		}
		if linkedMentions > 0 {
			if clean == len(syms) {
				verdict = "clean in this repository; linked repositories still name the removed identifiers"
			} else {
				verdict += "; linked repositories name them too"
			}
		}
	}
	result := map[string]any{
		"mode":        "removed_symbols",
		"verdict":     verdict,
		"gateFailure": clean != len(syms) || linkedMentions > 0,
		"clean":       clean,
		"checked":     len(syms),
		"residuals":   out,
		"note": "whole-identifier text pass over searchable working-tree files, including comments and docs. " +
			"Mentions are not proof of live code references; inspect reported sites. Full verify is optional when " +
			"build/typecheck does not cover affected callers.",
	}
	if len(linkedLines) > 0 {
		result["linked"] = linkedLines
	}
	return result, nil
}

// removedSymbolIdentifier returns the identifier a removed symbol is written
// as at its use sites: the final segment of a qualified name ("Type.method",
// "pkg::Type::method", "Type#method"), or the name itself.
func removedSymbolIdentifier(s string) string {
	i := strings.LastIndexAny(s, ".:#")
	if i < 0 || i == len(s)-1 {
		return s
	}
	last := s[i+1:]
	for _, r := range last {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$') {
			return s
		}
	}
	return last
}

// prismOwnedFile reports files prism init writes into a repository.
func prismOwnedFile(path string) bool {
	p := filepath.ToSlash(path)
	for _, pre := range []string{".grove/", ".claude/", ".prism/", ".codex/", ".cursor/", ".windsurf/", ".gemini/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	switch p {
	case ".mcp.json", "prism.yaml", "CLAUDE.md", "AGENTS.md", "GEMINI.md", ".cursorrules", ".windsurfrules",
		".github/copilot-instructions.md", "opencode.json", ".prism-read-tracker.json":
		return true
	}
	return false
}

// refreshErrorsFor keeps index errors that name a changed file. Grove
// reports per-file errors as "<repo-relative path>: <reason>"; an error that
// does not start with a path is kept, since it cannot be ruled out.
func refreshErrorsFor(errs []string, changed map[string][]lineRange) []string {
	var keep []string
	for _, e := range errs {
		file, _, ok := strings.Cut(e, ": ")
		if !ok || strings.ContainsAny(file, " \t") {
			keep = append(keep, e)
			continue
		}
		if _, hit := changed[filepath.ToSlash(file)]; hit {
			keep = append(keep, e)
		}
	}
	return keep
}

// valueOnlyCaller reports whether site is one of impact's callers rather
// than a declaration, family member or super.
func valueOnlyCaller(impact *grove.ChangeImpactResult, site grove.SymbolRecord) bool {
	if impact == nil {
		return false
	}
	for _, group := range [][]grove.SymbolRecord{impact.Declarations, impact.Family, impact.Supers} {
		for _, m := range group {
			if m.ID == site.ID {
				return false
			}
		}
	}
	for _, c := range impact.Callers {
		if c.ID == site.ID {
			return true
		}
	}
	return false
}

// testCoverageEntry is one changed function body and the verified tests that
// call it (verify's informational testCoverage section).
type testCoverageEntry struct {
	symbol, file string
	line         int
	coveredBy    []string
	checked      bool
}

// verifyDependencies is verify's step 3. Cross-component dependency
// CANDIDATES: induced edges whose every evidence site sits inside code this
// diff touched (line-level, not file-level: a pre-existing edge elsewhere in
// a touched file must not fire). No base-graph comparison is performed, so
// these are candidates for review, never asserted as "introduced". Then the
// declared architecture rules, for violations the diff touches. An error
// means neither check ran (archStatus "unchecked").
func (h *Handler) verifyDependencies(ctx context.Context, changed map[string][]lineRange) (newDeps []map[string]any, archStatus string, archIntroduced []view.Violation, err error) {
	symbols, edges, err := h.Grove.SnapshotGraph(ctx)
	archStatus = "no-rules"
	if err != nil {
		return nil, "unchecked", nil, err
	}
	{
		v := view.Build(symbols, edges, view.Options{MaxSites: 1 << 30})
		for _, e := range v.Edges {
			all := len(e.Sites) > 0
			for _, s := range e.Sites {
				if !touched(changed, s.FromFile, grove.SpanInfo{Start: s.FromLine, End: s.FromLine}) {
					all = false
					break
				}
			}
			if all {
				sites := e.Sites
				if len(sites) > 3 {
					sites = sites[:3]
				}
				newDeps = append(newDeps, map[string]any{
					"from": e.From, "to": e.To, "weight": e.Weight,
					"minTier": view.MinTier(e.Tiers), "sites": sites,
					"caveat": "candidate — all evidence is in changed code; no base-graph comparison performed",
				})
			}
		}
		if len(h.Cfg.ArchDeny) > 0 {
			if rules, rerr := view.ParseRules(h.Cfg.ArchDeny); rerr == nil {
				archStatus = "pass"
				for _, viol := range v.CheckRules(rules) {
					// Only violations this diff touches; pre-existing debt is
					// prism arch's job, not the diff verdict's.
					introduced := false
					for _, s := range viol.Edge.Sites {
						if lineTouched(changed, s.FromFile, s.FromLine) || touched(changed, s.FromFile, grove.SpanInfo{Start: s.FromLine, End: s.FromLine}) {
							introduced = true
							break
						}
					}
					if introduced {
						archIntroduced = append(archIntroduced, viol)
						if viol.MinTier != "heuristic" {
							archStatus = "fail"
						} else if archStatus == "pass" {
							archStatus = "review"
						}
					}
				}
			}
		}
	}

	return newDeps, archStatus, archIntroduced, nil
}

// verifyVerdict is fail-closed: incomplete beats review beats complete. A
// contract change that could not be verified never yields "complete".
func verifyVerdict(missed, unverified int, archStatus string) string {
	switch {
	case missed > 0 || archStatus == "fail":
		return "incomplete"
	case unverified > 0 || archStatus == "review":
		return "review"
	}
	return "complete"
}

// verifyTestCoverageOut renders the testCoverage section.
func verifyTestCoverageOut(entries []testCoverageEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, tc := range entries {
		e := map[string]any{"symbol": tc.symbol, "file": tc.file, "line": tc.line}
		if len(tc.coveredBy) > 0 {
			e["coveredBy"] = tc.coveredBy
		} else {
			e["warning"] = "no verified test caller found in the graph for this changed function — " +
				"confirm you ran a test that actually exercises it, not just a passing suite"
		}
		out = append(out, e)
	}
	return out
}

// verifyLinkedConsumers lists the consumers of changed contracts in linked
// repositories. The targets are the BASE symbols (the names and signatures
// consumers were written against): after a rename or a removal the new name
// has no consumers, the old one does. They are reported, not judged: the
// verdict covers this repository's diff, and the other repositories were
// not rebuilt.
func (h *Handler) verifyLinkedConsumers(ctx context.Context, seeds []grove.SymbolRecord) map[string]any {
	out := map[string]any{}
	if !h.workspaceConfigured() || len(seeds) == 0 {
		return out
	}
	ans := h.linkedAnswer(ctx, h.targetsOf(seeds))
	ans.attach(out)
	switch unchecked := ans.sites + ans.matches; {
	case unchecked > 0:
		out["linkedVerdict"] = fmt.Sprintf("NOT checked: %d line(s) in linked repositories reference a contract this diff changed or removed (listed below)", unchecked)
	case ans.incomplete:
		out["linkedVerdict"] = "linked repositories were NOT all searched (see workspaceScope)"
	}
	return out
}

// verifyDiff is the change verify checks: the changed line ranges and the
// deleted files since base, without the files prism init owns (the forced
// probe reported "8 changed files" for a one-file edit, 2026-09-29).
func verifyDiff(root, base string) (map[string][]lineRange, []string, error) {
	changed, err := gitChangedRanges(root, base)
	if err != nil {
		return nil, nil, err
	}
	for f := range changed {
		if prismOwnedFile(f) {
			delete(changed, f)
		}
	}
	var deleted []string
	for _, f := range gitDeletedFiles(root, base) {
		if !prismOwnedFile(f) {
			deleted = append(deleted, f)
		}
	}
	return changed, deleted, nil
}
