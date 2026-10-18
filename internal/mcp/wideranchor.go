package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// obligationSiteCount is the blast-radius size of a change_impact result:
// override/implementation family + callers + declaring-type sites.
func obligationSiteCount(r *grove.ChangeImpactResult) int {
	if r == nil {
		return 0
	}
	return len(impactSites(r, false))
}

// maxAnchorCandidates bounds how many same-named receiver types
// widerAnchorHint will probe when hunting the family-maximal anchor.
const maxAnchorCandidates = 8

// widerAnchorHint reports a same-leaf-named symbol whose CLOSED change-impact
// family is strictly larger than the given result's — the signature of having
// queried a concrete implementation when the contract that fans out is the
// interface method (measured: the direct change_impact arm scored 0.333 on
// grafana-querydata by anchoring a concrete QueryData with 4 callers while
// Service.QueryData held the 50-site closed family). Deterministic: pure graph
// probes, no redirection — the result still answers exactly what was asked,
// and the hint tells the agent what else to check. Returns nil when the
// queried anchor is already the widest.
func (h *Handler) widerAnchorHint(ctx context.Context, r *grove.ChangeImpactResult) map[string]any {
	// No declarations guard: a Go/TS interface member is not a separate
	// symbol, so its impact result carries declaringTypes and an EMPTY
	// Declarations list — exactly the queries that most need this hint.
	if r == nil || r.Query == "" {
		return nil
	}
	leaf := leafName(r.Query)
	cands, err := h.Grove.Resolve(ctx, leaf)
	if err != nil || len(cands) < 3 {
		return nil
	}
	tried := map[string]bool{r.Query: true}
	for _, d := range r.Declarations {
		tried[displayQN(d)] = true
	}
	var bestQN string
	var best *grove.ChangeImpactResult
	bestExtra := 0
	probes := 0
	for _, c := range cands {
		if probes >= maxAnchorCandidates {
			break
		}
		qn := c.Name
		if qn == "" || tried[qn] || c.TestDouble || leafName(qn) != leaf {
			continue
		}
		tried[qn] = true
		probes++
		alt, err := h.Grove.ChangeImpactScoped(ctx, qn, c.File)
		if err != nil || len(alt.Declarations) == 0 || !relatedImpactAnchors(r, alt) {
			continue
		}
		// Only a base contract that CONTAINS the queried anchor widens it, and
		// only by sites this result does not already list. Counting list
		// lengths sent the agent from the interface Routes.Match to its
		// implementation Mux.Match, whose longer list merely counted the
		// same callers plus the supers (chi, 2026-10-17).
		if !anchorContains(alt, r) {
			continue
		}
		if extra := additionalSites(alt, r); extra > bestExtra {
			best, bestQN, bestExtra = alt, qn, extra
		}
	}
	if best == nil {
		return nil
	}
	completeness, coverageNote := impactCoverage(best)
	scope := "CLOSED"
	if completeness != "closed" {
		scope = "indexed (partial)"
	}
	note := fmt.Sprintf("%s is the base contract of this anchor and its %s change set adds %d site(s) "+
		"this result does not list. If the contract being changed is the interface/base declaration "+
		"rather than this one implementation, query %s instead to inspect its family.",
		bestQN, scope, bestExtra, bestQN)
	if coverageNote != "" {
		note += " " + coverageNote
	}
	return map[string]any{
		"qualifiedName": bestQN,
		"totalSites":    len(impactSites(best, true)),
		"completeness":  completeness,
		"note":          note,
	}
}

// sameSymbol compares declarations by file and qualified name.
func sameSymbol(a, b grove.SymbolRecord) bool {
	return a.FilePath == b.FilePath && displayQN(a) == displayQN(b)
}

// anchorContains reports whether wide's change set lists narrow's queried
// declaration as one of its own family members (an override, implementation
// or alias): the relationship that makes wide the base contract. The reverse
// (narrow lists wide among its supers) means wide is an implementation and
// never a wider anchor.
func anchorContains(wide, narrow *grove.ChangeImpactResult) bool {
	for _, d := range narrow.Declarations {
		for _, f := range wide.Family {
			if sameSymbol(d, f) {
				return true
			}
		}
	}
	return false
}

// additionalSites counts the site identities in wide that narrow does not
// already list: file, line, kind and qualified name, declarations included.
func additionalSites(wide, narrow *grove.ChangeImpactResult) int {
	key := func(s grove.SymbolRecord) string {
		return fmt.Sprintf("%s:%d:%s:%s", s.FilePath, s.Span.Start, s.Kind, displayQN(s))
	}
	have := map[string]bool{}
	for _, s := range impactSites(narrow, true) {
		have[key(s)] = true
	}
	n := 0
	for _, s := range impactSites(wide, true) {
		if !have[key(s)] {
			n++
		}
	}
	return n
}

// Require a shared contract identity, not merely a matching member name.
// Callers are excluded: one caller can invoke two unrelated contracts.
func relatedImpactAnchors(a, b *grove.ChangeImpactResult) bool {
	for _, pair := range [][2]*grove.ChangeImpactResult{{a, b}, {b, a}} {
		for _, d := range pair[0].Declarations {
			for _, group := range [][]grove.SymbolRecord{pair[1].Declarations, pair[1].Supers, pair[1].Family} {
				for _, s := range group {
					if d.FilePath == s.FilePath && displayQN(d) == displayQN(s) {
						return true
					}
				}
			}
		}
	}
	return false
}

// One inventory for counting and verification, including super contracts.
func impactSites(r *grove.ChangeImpactResult, declarations bool) []grove.SymbolRecord {
	if r == nil {
		return nil
	}
	groups := [][]grove.SymbolRecord{r.Supers, r.Family, r.Callers, r.DeclaringTypes}
	if declarations {
		groups = append([][]grove.SymbolRecord{r.Declarations}, groups...)
	}
	seen := map[string]bool{}
	var out []grove.SymbolRecord
	for _, group := range groups {
		for _, s := range group {
			key := fmt.Sprintf("%s:%d:%s:%s", s.FilePath, s.Span.Start, s.Kind, displayQN(s))
			if !seen[key] {
				seen[key] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func leafName(qn string) string {
	if i := strings.LastIndexByte(qn, '.'); i >= 0 && i+1 < len(qn) {
		return qn[i+1:]
	}
	return qn
}

// hasProductionCaller reports whether the result lists a caller outside test
// files, whatever edge (direct, interface dispatch, base contract) reached it.
func hasProductionCaller(r *grove.ChangeImpactResult) bool {
	if r == nil {
		return false
	}
	for _, c := range r.Callers {
		if !isVerifiedTestCaller("/" + c.FilePath) {
			return true
		}
	}
	return false
}
