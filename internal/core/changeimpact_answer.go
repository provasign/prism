package core

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// changeImpactAnswer carries the state shared while toolChangeImpact turns a
// resolved (non-member) change set into its answer map.
type changeImpactAnswer struct {
	h                 *Handler
	r                 *grove.ChangeImpactResult
	args              map[string]any
	targetLeaf        string
	methodTarget      bool
	wideImpact        bool
	evidenceBudget    int
	verifiedCallLines impactPlanSites
	callPositions     map[string]callerPositions
	memberPositions   map[string]callerPositions
}

// changeImpactMemberAnswer renders a result anchored on a member (field,
// property, ...) rather than a callable.
func (h *Handler) changeImpactMemberAnswer(ctx context.Context, args map[string]any, r *grove.ChangeImpactResult) map[string]any {
	mout := memberImpactOutput(r, stringArg(args, "file", "") != "")
	h.addDegradedNote(ctx, mout, r)
	h.addUnindexedNote(ctx, mout, leafOf(r.Query))
	leaf := leafOf(r.Query)
	c, unsure := h.memberEditSites(r, leaf)
	h.attachEditSites(ctx, mout, r, c, leaf, unsure)
	h.addWorkspaceConsumers(ctx, mout, r)
	return mout
}

// changeImpactSelectSignature narrows r to the overload named by the
// signature argument, if any, and returns the note describing the pick.
func (h *Handler) changeImpactSelectSignature(ctx context.Context, args map[string]any, query, inferenceNote string, r *grove.ChangeImpactResult) (*grove.ChangeImpactResult, string) {
	signatureNote := ""
	if sig := stringArg(args, "signature", ""); sig != "" && inferenceNote == "" {
		var narrowed *grove.ChangeImpactResult
		narrowed, signatureNote = h.selectOverloadBySignature(ctx, query, stringArg(args, "file", ""), sig, r)
		if narrowed != nil {
			r = narrowed
		}
	}
	return r, signatureNote
}

func (h *Handler) changeImpactNewAnswer(ctx context.Context, args map[string]any, r *grove.ChangeImpactResult) *changeImpactAnswer {
	a := &changeImpactAnswer{h: h, r: r, args: args}
	// The member being changed, used to locate its call sites inside callers.
	targetLeaf := r.Query
	if i := strings.IndexByte(targetLeaf, '('); i >= 0 {
		targetLeaf = targetLeaf[:i]
	}
	a.targetLeaf = leafOf(strings.TrimSpace(targetLeaf))
	// A method anchor (declared on a type) is never reached by a bare call
	// in receiver-required languages; a free function is.
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		for _, d := range group {
			if d.ParentSymbol != "" {
				a.methodTarget = true
			}
		}
	}
	a.evidenceBudget = impactEvidenceMaxBytes
	a.wideImpact = obligationSiteCount(r) >= wideImpactIdentityThreshold
	if !a.wideImpact && len(r.Callers) > 0 {
		if plan, err := h.Grove.RenamePlan(ctx, r.Query, a.targetLeaf+"PrismEvidence"); err == nil {
			a.verifiedCallLines = renamePlanLines(plan)
		}
	}
	a.callPositions = callerCallPositions(r, a.methodTarget)
	a.memberPositions = memberCallPositions(r)
	return a
}

// compactWithScope renders symbols as answer entries; annotate marks the
// callers list, which carries call positions, evidence and test labels.
func (a *changeImpactAnswer) compactWithScope(syms []grove.SymbolRecord, annotate bool) []map[string]any {
	out := make([]map[string]any, 0, len(syms))
	for _, s := range syms {
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		entry := map[string]any{
			"name":          s.Name,
			"qualifiedName": qn,
			"filePath":      s.FilePath,
			"line":          s.Span.Start,
			"kind":          s.Kind,
		}
		if !a.wideImpact {
			entry["signature"] = s.Signature
		}
		// Locality hint: grove attributes a call made inside a closure to
		// the enclosing declaration, so name the nested scope that
		// actually holds it. Absent for the common non-nested case, which
		// therefore renders exactly as before.
		if annotate {
			if p, ok := a.callPositions[overloadCallerKey(s.FilePath, s.Span.Start, displayQN(s))]; ok {
				entry["calls"] = p.label()
			}
		} else if p, ok := a.memberPositions[overloadCallerKey(s.FilePath, s.Span.Start, displayQN(s))]; ok {
			entry["calls"] = "itself calls the change set " + p.label()
		}
		if annotate && !a.wideImpact {
			a.addCallEvidence(entry, s)
		}
		if annotate {
			if via := nestedScopeFor(s, a.targetLeaf); via != "" {
				entry["via"] = via
			}
			// Callers already include test files (a test calling the
			// code it exercises is an ordinary `calls` edge, never
			// specially excluded here) -- this was silent before,
			// leaving the agent to guess from the filename which
			// callers are production call sites and which are tests.
			if isVerifiedTestCaller("/" + filepath.ToSlash(s.FilePath)) {
				entry["isTest"] = true
			}
		}
		out = append(out, entry)
	}
	return out
}

// addCallEvidence attaches the call-expression evidence of one caller entry.
func (a *changeImpactAnswer) addCallEvidence(entry map[string]any, s grove.SymbolRecord) {
	if p, ok := a.callPositions[overloadCallerKey(s.FilePath, s.Span.Start, displayQN(s))]; ok {
		// A same-named call the compiler did not report, or whose
		// arguments the signature cannot take, is another method.
		switch {
		case len(p.positions) == 0:
		case p.byName:
			addPlanCheckedCallEvidence(entry, s, a.targetLeaf, a.methodTarget, &a.evidenceBudget, a.verifiedCallLines, p.lines())
		default:
			addImpactCallEvidence(entry, s, a.targetLeaf, a.methodTarget, &a.evidenceBudget, p.lines())
		}
	} else {
		addPlanCheckedCallEvidence(entry, s, a.targetLeaf, a.methodTarget, &a.evidenceBudget, a.verifiedCallLines)
	}
}

func (a *changeImpactAnswer) compact(syms []grove.SymbolRecord) []map[string]any {
	return a.compactWithScope(syms, false)
}

// baseOutput builds the symbol lists, counts and relay fields every answer
// carries.
func (a *changeImpactAnswer) baseOutput() map[string]any {
	r := a.r
	out := map[string]any{
		"query":              r.Query,
		"declarations":       a.compact(r.Declarations),
		"supers":             a.compact(r.Supers),
		"family":             a.compact(r.Family),
		"callers":            a.compactWithScope(r.Callers, true),
		"totalSites":         len(impactSites(r, true)) + len(r.ReExports),
		"familyCompleteness": r.Completeness,
		"callerCoverage":     impactCallerCoverage(r),
	}
	if ev := impactCallerEvidence(r); ev != "" {
		out["callerEvidence"] = ev
	}
	addIndexedCompletenessSafety(out)
	if relay := impactRelaySites(r, 40); len(relay) > 0 {
		if rex := reExportRelayLabels(r); len(rex) > 0 && len(relay)+len(rex) <= 40 {
			relay = append(relay, rex...)
			sort.Strings(relay)
		}
		out["relaySites"] = relay
		out["relayNote"] = impactRelayNote
	}
	if rex := impactReExportOutput(r); len(rex) > 0 {
		out["reExports"] = rex
	}
	if rel := impactRelatedOutput(r); len(rel) > 0 {
		out["related"] = rel
	}
	return out
}

// addNotes adds the resolution, evidence, coverage and contract notes.
func (a *changeImpactAnswer) addNotes(ctx context.Context, out map[string]any, res impactResolution, signatureNote string) {
	r := a.r
	if res.inheritedNote != "" {
		out["inheritedNote"] = res.inheritedNote
	}
	if signatureNote != "" {
		out["signatureNote"] = signatureNote
	}
	// The signal counts direct edges to the declaration. When the answer also
	// lists super contracts and a production caller, that caller reaches the
	// declaration through the contract (chi Mux.Match: GetHead and
	// SupressNotFound call it via Routes.Match), so "no production code calls
	// it" would contradict the caller list printed next to it. A per-overload
	// claim with no supers stays: it is exactly what it says.
	if testOnly := a.h.testOnlySignal(ctx, r.Declarations); len(testOnly) > 0 && !(len(r.Supers) > 0 && hasProductionCaller(r)) {
		out["testOnly"] = testOnly
	}
	if res.inferenceNote != "" {
		out["methodFamilyNote"] = res.inferenceNote
	}
	if res.modulePathNote != "" {
		out["resolvedNote"] = res.modulePathNote
	}
	if a.wideImpact {
		out["evidenceNote"] = "Large indexed result delivered as compact file:line identities; repeated signatures and call expressions are omitted. Every returned site is shown, but graph coverage and receiver uncertainty remain as reported."
	} else if len(r.Callers) > 0 {
		out["evidenceNote"] = indexedEvidenceNote
	}
	completeness, coverageNote := impactCoverage(r)
	if coverageNote != "" {
		out["coverageNote"] = coverageNote
	}
	a.h.hypLedger.recordClosedImpact(completeness,
		len(impactSites(r, true)))
	if sn := a.h.hypLedger.scopeNote(); sn != "" {
		out["scopeNote"] = sn
	}
	if stringArg(a.args, "file", "") == "" {
		if note := changeImpactDeclFilesNote(r); note != "" {
			out["ambiguityNote"] = note
		}
	}
	if len(r.DeclaringTypes) > 0 {
		out["declaringTypes"] = a.compact(r.DeclaringTypes)
		out["declaringTypesNote"] = "these type declaration blocks contain member " +
			"signatures that may need coordinated edits for a contract change " +
			"(Go/TS interface members are not separate symbols); inspect each and " +
			"include it as a potential site in your answer"
	}
	if completeness != "" {
		out["completeness"] = completeness
	}
	if r.HasHeuristicRefs {
		out["hasHeuristicRefs"] = true
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
		out["warning"] = "the queried method belongs to an external supertype's contract (" +
			strings.Join(r.OverridesExternal, ", ") + "); changing its signature breaks that " +
			"contract, and this change-set is the project-local closure only — call sites " +
			"typed against the external supertype are not included"
	}
	// Anchor-selection guard: a precisely-computed answer to the wrong anchor
	// is still wrong. If a same-named symbol holds a strictly larger closed
	// family (interface vs concrete implementation), say so.
	if hint := a.h.widerAnchorHint(ctx, r); hint != nil {
		out["widerAnchor"] = hint
	}
}

// changeImpactDeclFilesNote warns when declarations span several files.
// Same-named types in distinct files all seed one merged closure — and
// a merged answer whose callers belong to the OTHER type reads as
// authoritative nonsense (measured 2026-09-02, BACKLOG addendum #5:
// "Engine.Query" merged two unrelated Engines; all 13 callers belonged
// to the one the agent was not asking about, and it re-derived the
// answer manually). When declarations span multiple files and the call
// was not already file-scoped, say so and name the fix.
func changeImpactDeclFilesNote(r *grove.ChangeImpactResult) string {
	declFiles := map[string]bool{}
	for _, d := range r.Declarations {
		declFiles[d.FilePath] = true
	}
	if len(declFiles) <= 1 {
		return ""
	}
	files := make([]string, 0, len(declFiles))
	for f := range declFiles {
		files = append(files, f)
	}
	sort.Strings(files)
	return fmt.Sprintf(
		"declarations span %d files (%s) — if these are DIFFERENT types sharing a name "+
			"(not overloads of one type), this result merges their closures and the "+
			"caller/family lists mix both. Re-run with file=\"<path fragment>\" to scope "+
			"to the one you mean.", len(declFiles), strings.Join(files, ", "))
}

// addCallSiteNotes explains the call positions and tags each caller with the
// overload(s) it calls.
func (a *changeImpactAnswer) addCallSiteNotes(ctx context.Context, out map[string]any) {
	if len(a.callPositions) > 0 {
		byName := 0
		for _, p := range a.callPositions {
			if p.byName && len(p.positions) > 0 {
				byName++
			}
		}
		note := "Each caller lists where it calls the change set (line:col of the called name); the compiler resolved these."
		if byName > 0 {
			note = fmt.Sprintf("Each caller lists where it calls the change set (line:col of the called name). %d caller(s) marked \"by name\" were matched by name only (no compiler resolution): check those positions before editing.", byName)
		}
		out["callPositionsNote"] = note
	}
	tags, lines := a.h.overloadCallerTags(ctx, a.r)
	if len(tags) == 0 {
		return
	}
	if lines != nil {
		out["callSitesByOverload"] = lines.render()
		if lines.fromCompiler && len(lines.mixed) == 0 {
			out["callSitesSource"] = "javac resolved each call (line:column of the method name) to its overload; nothing here is inferred"
		}
		if len(lines.unresolved) > 0 {
			out["unresolvedCallsNote"] = fmt.Sprintf("%d same-named call(s) javac resolved to none of these overloads (another method of that name, or not compiled) are NOT in callSitesByOverload: %s",
				len(lines.unresolved), strings.Join(lines.unresolved, ", "))
		}
	}
	if callers, ok := out["callers"].([]map[string]any); ok {
		for _, c := range callers {
			file, _ := c["filePath"].(string)
			line, _ := c["line"].(int)
			qn, _ := c["qualifiedName"].(string)
			if t := tags[overloadCallerKey(file, line, qn)]; len(t) > 0 {
				c["overloads"] = strings.Join(t, ", ")
			}
		}
		out["overloadNote"] = "Each caller is tagged with the overload(s) it calls, as the compiler resolved them."
	}
}

// finish adds the degraded/unindexed notes, edit sites, workspace consumers
// and the resolved-ambiguity note.
func (a *changeImpactAnswer) finish(ctx context.Context, out map[string]any, query, familyPickNote string) {
	h, r := a.h, a.r
	h.addDegradedNote(ctx, out, r)
	applyCompilerBackedCompleteness(out, r)
	h.addUnindexedNote(ctx, out, a.targetLeaf)
	markNameMatchedSites(out, r)
	h.attachEditSites(ctx, out, r, h.impactEditSites(r, a.callPositions, a.memberPositions), a.targetLeaf, nil)
	h.addWorkspaceConsumers(ctx, out, r)
	if familyPickNote == "" {
		familyPickNote = h.sameFileNamesakesNote(ctx, query, stringArg(a.args, "file", ""), r)
	}
	if familyPickNote != "" {
		out["resolvedAmbiguity"] = familyPickNote
	}
}
