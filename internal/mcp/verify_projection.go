package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// The base graph owns relationships, not after-side coordinates. Resolve each
// surviving site in changed files before comparing it with an after-side diff.
type baseSiteProjector struct {
	h       *Handler
	changed map[string][]lineRange
	deleted map[string]bool
	files   map[string][]grove.SymbolRecord
	errors  map[string]error
}

func newBaseSiteProjector(h *Handler, changed map[string][]lineRange, deleted []string) *baseSiteProjector {
	p := &baseSiteProjector{h: h, changed: changed, deleted: map[string]bool{}, files: map[string][]grove.SymbolRecord{}, errors: map[string]error{}}
	for _, f := range deleted {
		p.deleted[f] = true
	}
	return p
}

func siteIdentity(s grove.SymbolRecord) string {
	return s.FilePath + "\x00" + s.Kind + "\x00" + displayQN(s)
}

func (p *baseSiteProjector) project(ctx context.Context, base *grove.ChangeImpactResult, name string) ([]grove.SymbolRecord, []string) {
	callers := map[string]bool{}
	for _, s := range base.Callers {
		callers[siteIdentity(s)] = true
	}
	for _, group := range [][]grove.SymbolRecord{base.Supers, base.Family, base.DeclaringTypes} {
		for _, s := range group {
			delete(callers, siteIdentity(s))
		}
	}
	var sites []grove.SymbolRecord
	var gaps []string
	for _, old := range impactSites(base, false) {
		if p.deleted[old.FilePath] {
			continue
		}
		if _, changed := p.changed[old.FilePath]; !changed {
			sites = append(sites, old)
			continue
		}
		current, loaded := p.files[old.FilePath]
		if !loaded {
			var err error
			current, err = p.h.Grove.FileSymbols(ctx, old.FilePath)
			p.files[old.FilePath], p.errors[old.FilePath] = current, err
		}
		if err := p.errors[old.FilePath]; err != nil {
			gaps = append(gaps, fmt.Sprintf("%s: base site could not be mapped to current source: %v", displayQN(old), err))
			continue
		}
		var matches []grove.SymbolRecord
		for _, s := range current {
			if siteIdentity(s) == siteIdentity(old) {
				matches = append(matches, s)
			}
		}
		if len(matches) > 1 {
			var exact []grove.SymbolRecord
			for _, s := range matches {
				if s.Signature == old.Signature {
					exact = append(exact, s)
				}
			}
			if len(exact) == 1 {
				matches = exact
			}
		}
		if len(matches) == 0 {
			continue
		} // removed or renamed site
		if len(matches) != 1 {
			gaps = append(gaps, displayQN(old)+": ambiguous surviving base site; review overloads")
			continue
		}
		s := matches[0]
		if callers[siteIdentity(old)] && len(old.CallSites) > 0 {
			// Removing/replacing the call satisfies this caller's old obligation.
			hasCall := false
			for _, cs := range s.CallSites {
				if cs.Callee == name || strings.HasSuffix(cs.Callee, "."+name) {
					hasCall = true
					break
				}
			}
			if !hasCall {
				continue
			}
		}
		sites = append(sites, s)
	}
	return sites, gaps
}
