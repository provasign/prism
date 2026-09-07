package mcp

import (
	"fmt"
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

func addImpactCallEvidence(entry map[string]any, caller grove.SymbolRecord, target string, budget *int) {
	lines := strings.Split(caller.RawText, "\n")
	matched := map[int]bool{}
	for _, call := range caller.CallSites {
		if target != "" && leafOf(call.Callee) == target {
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
		notes = append(notes, "call expression unavailable in indexed source; inspect this caller if needed")
	}
	if omitted > 0 {
		notes = append(notes, fmt.Sprintf("%d matching line(s) omitted by evidence limit; lookup this caller for all expressions", omitted))
	}
	if len(notes) > 0 {
		entry["evidenceNote"] = strings.Join(notes, "; ")
	}
}
