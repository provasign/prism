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
	baseline := obligationSiteCount(r)
	var bestQN string
	var best *grove.ChangeImpactResult
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
		alt, err := h.Grove.ChangeImpact(ctx, qn)
		if err != nil || len(alt.Declarations) == 0 || !relatedImpactAnchors(r, alt) {
			continue
		}
		if n := obligationSiteCount(alt); n > baseline && (best == nil || n > obligationSiteCount(best)) {
			best, bestQN = alt, qn
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
	note := fmt.Sprintf("%s has a larger %s change set (%d sites vs %d for the queried anchor). "+
		"If the contract being changed is the interface/base declaration rather than this one "+
		"implementation, query %s instead to inspect its family.",
		bestQN, scope, obligationSiteCount(best), baseline, bestQN)
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
