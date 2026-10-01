package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// pickAmbiguousByFamily resolves a short ambiguity when one candidate's change
// set already contains every other candidate: an override family declared
// under one name (click Group.get_command and its subclass
// CommandCollection.get_command). Sonnet 5.5 agents hit "ambiguous — 2
// candidates" on 2 of their 3 change_impact calls (2026-09-29) and did not
// call it again. Of the candidates whose change set holds all the others,
// the widest (usually the base declaration) wins.
func (h *Handler) pickAmbiguousByFamily(ctx context.Context, err error) (*grove.ChangeImpactResult, string, bool) {
	type cand struct {
		qn, file string
		line     int
	}
	var cands []cand
	for _, m := range ambiguousCandidateLine.FindAllStringSubmatch(err.Error(), -1) {
		line, _ := strconv.Atoi(m[3])
		cands = append(cands, cand{m[1], m[2], line})
	}
	if len(cands) < 2 || len(cands) >= wideMemberAmbiguityThreshold {
		return nil, "", false
	}
	var best *grove.ChangeImpactResult
	bestQN, bestSites := "", -1
	for _, c := range cands {
		r, cerr := h.Grove.ChangeImpactScoped(ctx, c.qn, c.file)
		if cerr != nil || r == nil {
			continue
		}
		have := map[string]bool{}
		for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family, r.Supers} {
			for _, s := range group {
				have[normalizePath(s.FilePath)+":"+strconv.Itoa(s.Span.Start)] = true
			}
		}
		all := true
		for _, o := range cands {
			if !have[normalizePath(o.file)+":"+strconv.Itoa(o.line)] {
				all = false
				break
			}
		}
		if n := len(impactSites(r, true)); all && n > bestSites {
			best, bestQN, bestSites = r, c.qn, n
		}
	}
	if best == nil {
		return nil, "", false
	}
	names := make([]string, 0, len(cands))
	for _, o := range cands {
		names = append(names, o.qn)
	}
	return best, fmt.Sprintf("the name matched %d declarations (%s); they are one override family, so this is the change set of %s, which includes all of them",
		len(cands), strings.Join(names, ", "), bestQN), true
}
