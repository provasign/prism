package mcp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

const impactEvidenceMaxBytes = 8192

// Above this size, resolved identities carry the complete answer and repeated
// signatures/call snippets become the dominant payload (Guava delegate: 42KB).
const wideImpactIdentityThreshold = 100

// Only supplementary evidence is bounded. The impact inventory stays intact.
func compactImpactLine(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 240 {
		return string(runes[:240]) + " [truncated; lookup for full source]"
	}
	return text
}

const callExpressionUnavailableNote = "call expression unavailable in indexed source; inspect this caller if needed"

func addImpactCallEvidence(entry map[string]any, caller grove.SymbolRecord, target string, methodTarget bool, budget *int, verified ...map[int]bool) {
	lines := strings.Split(caller.RawText, "\n")
	matched := map[int]bool{}
	for _, call := range caller.CallSites {
		if target != "" && leafOf(call.Callee) == target {
			if methodTarget && bareCallCannotBeMethod(caller.Language, call.Callee) {
				continue
			}
			if len(verified) > 0 && !verified[0][call.Line] {
				continue
			}
			matched[call.Line] = true
		}
	}
	ordered := make([]int, 0, len(matched))
	for line := range matched {
		ordered = append(ordered, line)
	}
	sort.Ints(ordered)
	var evidence []map[string]any
	unavailable, omitted := 0, 0
	for _, line := range ordered {
		idx := line - caller.Span.Start
		if caller.Span.Start <= 0 || idx < 0 || idx >= len(lines) || strings.TrimSpace(lines[idx]) == "" {
			unavailable++
			continue
		}
		text := compactImpactLine(lines[idx])
		if len(evidence) >= 2 || len(text) > *budget {
			omitted++
			continue
		}
		evidence = append(evidence, map[string]any{"line": line, "text": text})
		*budget -= len(text)
	}
	if len(evidence) > 0 {
		entry["evidence"] = evidence
	}
	var notes []string
	if len(ordered) == 0 || unavailable > 0 {
		notes = append(notes, callExpressionUnavailableNote)
	}
	if omitted > 0 {
		notes = append(notes, fmt.Sprintf("%d matching line(s) omitted by evidence limit; lookup this caller for all expressions", omitted))
	}
	if len(notes) > 0 {
		entry["evidenceNote"] = strings.Join(notes, "; ")
	}
}

// impactPlanSites is what a rename plan says about call sites, by file.
type impactPlanSites struct {
	lines      map[string]map[int]bool
	unresolved map[string]bool // "file:caller" the plan could not resolve
}

// renamePlanLines indexes the lines a rename plan would touch, by file.
// Ambiguous sites count: the plan could not rule them out, so they may
// still be calls to the target.
func renamePlanLines(plan *grove.RenamePlanResult) impactPlanSites {
	sites := impactPlanSites{lines: map[string]map[int]bool{}, unresolved: map[string]bool{}}
	if plan == nil {
		return sites
	}
	for _, group := range [][]grove.RenameEdit{plan.Edits, plan.Ambiguous} {
		for _, edit := range group {
			file := filepath.Clean(edit.FilePath)
			if sites.lines[file] == nil {
				sites.lines[file] = map[int]bool{}
			}
			sites.lines[file][edit.Line] = true
		}
	}
	for _, u := range plan.Unresolved {
		if i := strings.LastIndexByte(u, ':'); i > 0 {
			sites.unresolved[filepath.Clean(u[:i])+":"+u[i+1:]] = true
		}
	}
	return sites
}

// addPlanCheckedCallEvidence filters a caller's call lines by the rename
// plan only when the plan covers the caller's file and resolved the caller.
// Otherwise (plan failed, no site in the file, or the plan listed the caller
// as unresolved, as with qualified `new Outer.Inner(...)` calls) the
// name-matched evidence is kept rather than reporting the call expression as
// unavailable.
func addPlanCheckedCallEvidence(entry map[string]any, caller grove.SymbolRecord, target string, methodTarget bool, budget *int, plan impactPlanSites) {
	file := filepath.Clean(caller.FilePath)
	if lines, ok := plan.lines[file]; ok && !plan.unresolved[file+":"+caller.Name] {
		addImpactCallEvidence(entry, caller, target, methodTarget, budget, lines)
		return
	}
	addImpactCallEvidence(entry, caller, target, methodTarget, budget)
}

// bareCallCannotBeMethod reports a call written without a receiver in a
// language where calling a method always needs one (this./self./x.). Such a
// call is a same-named function, not the method: hono's Router.match evidence
// counted 110 local match('GET', ...) calls in a test as "omitted" call lines,
// and the agent re-grepped the repository to find the real ones. Java, C#,
// Kotlin, Swift, C++ and PHP allow an implicit receiver and keep bare calls.
func bareCallCannotBeMethod(lang, callee string) bool {
	if strings.ContainsAny(callee, ".:>") {
		return false
	}
	switch strings.ToLower(lang) {
	case "typescript", "tsx", "javascript", "python", "go", "rust":
		return true
	}
	return false
}
