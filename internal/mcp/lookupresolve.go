package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// resolveLookup resolves a lookup name to one symbol (plus same-file
// overloads), an ambiguity list, or an explicit NO EXACT MATCH.
//
// The rule that matters most: a qualifier the caller wrote is a constraint.
// "Context.Recovery" must not resolve to the free function Recovery, and
// "render.Context.JSON" must not resolve to gin's Context.JSON, just because
// the last segment matches. Unqualified candidates used to "never conflict",
// which returned a confident, wrong body (29 of 321 names in the 2026-09-26
// sweep). A leading qualifier is accepted when it is the symbol's owner
// chain, or describes where the symbol lives (package clause, namespace,
// directory, module file, Go import path); otherwise the candidate is a
// conflict and the result is NO EXACT MATCH with candidates.
func (h *Handler) resolveLookup(ctx context.Context, name, fileScope, fileHint string, fields []string) (any, error) {
	q := parseLookupName(name)
	loc := newLookupLocator(h.Root)
	pool, err := h.lookupPool(ctx, q, name, fileScope)
	if err != nil {
		return nil, err
	}
	// File disambiguator: restrict to candidates whose path contains the hint, so
	// a name shared across packages resolves to the one the agent means. Ignored
	// if it would empty the set (a stale/typo'd hint shouldn't lose the symbol).
	if fileScope == "" && fileHint != "" {
		var kept []grove.SymbolRecord
		for _, s := range pool {
			if strings.Contains(strings.ToLower(s.FilePath), fileHint) {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			pool = kept
		}
	}

	if out := h.bestLookup(ctx, loc, q, name, pool, false, fields, fileScope); out != nil {
		return out, nil
	}
	if fileScope != "" {
		return map[string]any{
			"symbol": nil, "name": name, "matched": false,
			"note": fmt.Sprintf("no exact symbol named %q indexed in file %q; scope was not widened. Check the name/path and index freshness", name, fileScope),
		}, nil
	}
	if out := h.lookupInherited(ctx, loc, q, fields); out != nil {
		return out, nil
	}
	if out := h.bestLookup(ctx, loc, q, name, pool, true, fields, fileScope); out != nil {
		out["matchKind"] = "case-insensitive"
		note := fmt.Sprintf("case-insensitive match: no symbol is spelled exactly %q", name)
		if prev, _ := out["note"].(string); prev != "" {
			note += "; " + prev
		}
		out["note"] = note
		return out, nil
	}
	return h.lookupMiss(ctx, loc, q, name, pool), nil
}

// lookupPool gathers the candidate symbols for q: every symbol of the scoped
// file, or a name search plus qualified-form searches (so a precise
// Type.method is in the pool even when the bare name has hundreds of hits).
func (h *Handler) lookupPool(ctx context.Context, q lookupQuery, raw, fileScope string) ([]grove.SymbolRecord, error) {
	if fileScope != "" {
		// Exact file reads must not inherit the global name search's hit cap.
		syms, err := h.Grove.FileSymbols(ctx, fileScope)
		return dedupeSymbolsByID(filterGeneratedPrismContext(syms)), err
	}
	name := q.name()
	const bareLimit = 200
	syms, err := h.Grove.SearchSymbols(ctx, name, bareLimit)
	if err != nil {
		return nil, err
	}
	var extra []grove.SymbolRecord
	queries := []string{}
	if n := len(q.segs); n >= 2 {
		// Owner.name also brings near misses of the named type into the pool
		// (Context.SetAccepted for "Context.Accepted").
		queries = append(queries, q.owner()+"."+name)
		// Exact-name hits rank first, so an unsaturated bare search already
		// holds every symbol with this name; only a saturated one needs the
		// qualified spellings (C++ records "::").
		if len(syms) >= bareLimit {
			queries = append(queries, strings.Join(q.segs, "."), q.owner()+"::"+name)
			if n >= 3 {
				queries = append(queries, strings.Join(q.segs[n-3:], "."), strings.Join(q.segs[n-3:], "::"))
			}
		}
	}
	if raw = strings.TrimSpace(raw); raw != name && strings.ContainsAny(raw, "/:") {
		// A recorded QN spelled verbatim (document paths, C++ names).
		queries = append(queries, raw)
	}
	seen := map[string]bool{}
	for _, qs := range queries {
		if seen[qs] {
			continue
		}
		seen[qs] = true
		if more, qerr := h.Grove.SearchSymbols(ctx, qs, 50); qerr == nil {
			extra = append(extra, more...)
		}
	}
	return dedupeSymbolsByID(filterGeneratedPrismContext(append(extra, syms...))), nil
}

// bestLookup scores the pool and delivers the best match, or returns nil
// when nothing matches exactly.
func (h *Handler) bestLookup(ctx context.Context, loc *lookupLocator, q lookupQuery, raw string, pool []grove.SymbolRecord, fold bool, fields []string, fileScope string) map[string]any {
	raw = strings.TrimSpace(raw)
	scores := make([]int, len(pool))
	best := -1
	for i, s := range pool {
		sc, ok, _ := loc.lookupMatch(q, s, fold)
		if !ok {
			// The caller's exact spelling of a recorded qualified name (a
			// document path, an unusual QN) always counts.
			if !fold && raw != "" && s.QualifiedName == raw {
				sc, ok = 10000, true
			}
		} else if !fold && s.QualifiedName == raw {
			sc = 10000
		}
		if !ok {
			scores[i] = -1
			continue
		}
		if cFamilyDefinition(s) {
			// A C/C++ header prototype and its definition share a name; the
			// definition's body is the one to read (jansson's json_object_get
			// showed the 2-line jansson.h prototype). The prototype stays
			// listed under "declarations".
			sc += cFamilyDefinitionBonus
		}
		scores[i] = sc
		if best == -1 || sc > scores[best] {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	return h.deliverLookup(ctx, pool, scores, best, fields, fileScope)
}

// deliverLookup renders the best-scoring symbol. Same-file ties with the same
// qualified name are overloads (Java/C#/C++/Kotlin): no name or file argument
// can select one of them, so their bodies are delivered. Other ties are
// genuine ambiguity the qualifier couldn't resolve — listed as candidates.
func (h *Handler) deliverLookup(ctx context.Context, syms []grove.SymbolRecord, scores []int, bestIdx int, fields []string, fileScope string) map[string]any {
	best := syms[bestIdx]
	bestScore := scores[bestIdx]
	var out map[string]any
	if len(fields) > 0 {
		// Column projection requested: return just those fields.
		out = projectSymbol(best, fields)
	} else {
		out = map[string]any{"symbol": best, "content": best.RawText}
	}
	if cFamilyDefinition(best) {
		var decls []string
		for i := range syms {
			if i != bestIdx && scores[i] == bestScore-cFamilyDefinitionBonus && cFamilyDeclaration(syms[i]) &&
				syms[i].QualifiedName == best.QualifiedName {
				decls = append(decls, lookupCandidateLabel(syms[i]))
			}
		}
		if len(decls) > 0 {
			out["declarations"] = decls
		}
	}
	tied := 0
	for _, sc := range scores {
		if sc == bestScore {
			tied++
		}
	}
	if tied <= 1 {
		return out
	}
	cands := make([]string, 0, tied)
	var overloads []grove.SymbolRecord
	for i := range syms {
		if scores[i] != bestScore {
			continue
		}
		if i != bestIdx && syms[i].QualifiedName == best.QualifiedName && syms[i].FilePath == best.FilePath {
			overloads = append(overloads, syms[i])
			continue
		}
		cands = append(cands, lookupCandidateLabel(syms[i]))
	}
	if len(overloads) > 0 && fileScope == "" {
		// The name search caps its hits, which can cut a large overload
		// family short (StringUtils.join has 26). Take the complete set from
		// the declaring file.
		if fileSyms, ferr := h.Grove.FileSymbols(ctx, best.FilePath); ferr == nil {
			overloads = overloads[:0]
			for _, s := range dedupeSymbolsByID(fileSyms) {
				// Any kind: a PHP property and its same-named getter
				// (Bound.isInclusive) share the qualified name, and a kind
				// filter hid one of them entirely.
				if s.Span.Start != best.Span.Start && s.QualifiedName == best.QualifiedName {
					overloads = append(overloads, s)
				}
			}
		}
	}
	if len(overloads) > 0 {
		out["overloads"] = lookupOverloads(overloads, fields, len(best.RawText))
	}
	if len(cands) > 1 {
		out["ambiguous"] = true
		out["candidates"] = cands
	}
	return out
}

// lookupInherited resolves Type.member when Type declares no such member but
// a supertype does (Flask.route is Scaffold.route; Engine.GET is the
// embedded RouterGroup.GET). It walks extends/implements edges breadth-first
// from the owner type, nearest ancestor first.
func (h *Handler) lookupInherited(ctx context.Context, loc *lookupLocator, q lookupQuery, fields []string) map[string]any {
	if len(q.segs) < 2 {
		return nil
	}
	name := q.name()
	ownerQ := lookupQuery{raw: q.raw, segs: q.segs[:len(q.segs)-1], pathHint: q.pathHint}
	found, _ := h.Grove.SearchSymbols(ctx, ownerQ.name(), 50)
	if len(ownerQ.segs) >= 2 {
		if more, err := h.Grove.SearchSymbols(ctx, strings.Join(ownerQ.segs, "."), 25); err == nil {
			found = append(more, found...)
		}
	}
	var owners []grove.SymbolRecord
	for _, t := range dedupeSymbolsByID(found) {
		if !isTypeKind(t.Kind) {
			continue
		}
		if _, ok, _ := loc.lookupMatch(ownerQ, t, false); ok {
			owners = append(owners, t)
		}
	}
	type hop struct {
		sym   grove.SymbolRecord
		chain []string
	}
	var queue []hop
	visited := map[string]bool{}
	for _, t := range owners {
		visited[t.ID] = true
		queue = append(queue, hop{t, []string{lookupSymbolName(t)}})
	}
	for depth := 0; depth < 8 && len(queue) > 0 && len(visited) < 64; depth++ {
		var next []hop
		for _, cur := range queue {
			edges, err := h.Grove.Edges(ctx, cur.sym.ID, "out", []string{"extends", "implements"})
			if err != nil {
				continue
			}
			for _, e := range edges {
				super, ok := h.symbolAt(ctx, e.File, e.Name, e.Line)
				if !ok || visited[super.ID] {
					continue
				}
				visited[super.ID] = true
				chain := append(append([]string(nil), cur.chain...), lookupSymbolName(super))
				if members := h.typeMembers(ctx, super, name); len(members) > 0 {
					scores := make([]int, len(members))
					out := h.deliverLookup(ctx, members, scores, 0, fields, "")
					out["matchKind"] = "inherited"
					out["note"] = fmt.Sprintf("inherited: %s declares no %s; delivered %s from its supertype chain %s",
						lookupSymbolName(owners[0]), name, lookupSymbolName(members[0]), strings.Join(chain, " -> "))
					return out
				}
				next = append(next, hop{super, chain})
			}
		}
		queue = next
	}
	return nil
}

// symbolAt finds the indexed symbol with qualified name qn starting at line.
func (h *Handler) symbolAt(ctx context.Context, file, qn string, line int) (grove.SymbolRecord, bool) {
	syms, err := h.Grove.FileSymbols(ctx, file)
	if err != nil {
		return grove.SymbolRecord{}, false
	}
	for _, s := range syms {
		if s.Span.Start == line && lookupSymbolName(s) == qn {
			return s, true
		}
	}
	return grove.SymbolRecord{}, false
}

// typeMembers returns the members of type t named name, declaring-file
// members first (a Go method may live in another file of the package).
func (h *Handler) typeMembers(ctx context.Context, t grove.SymbolRecord, name string) []grove.SymbolRecord {
	want := append(symbolSegs(t), name)
	var pool []grove.SymbolRecord
	if fs, err := h.Grove.FileSymbols(ctx, t.FilePath); err == nil {
		pool = append(pool, fs...)
	}
	sep := "."
	if strings.Contains(t.QualifiedName, "::") {
		sep = "::"
	}
	if more, err := h.Grove.SearchSymbols(ctx, lookupSymbolName(t)+sep+name, 25); err == nil {
		pool = append(pool, more...)
	}
	var out []grove.SymbolRecord
	for _, s := range dedupeSymbolsByID(pool) {
		segs := symbolSegs(s)
		if len(segs) == len(want) && segsHaveSuffix(segs, want, false) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].FilePath == t.FilePath) != (out[j].FilePath == t.FilePath) {
			return out[i].FilePath == t.FilePath
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].Span.Start < out[j].Span.Start
	})
	return out
}

// lookupMiss builds the NO EXACT MATCH answer: the flag, a note saying why,
// and short candidate lines (name, location, signature). No body is
// delivered unless the closest candidate belongs to the type the caller
// named: an unrelated body read first is worse than no body.
func (h *Handler) lookupMiss(ctx context.Context, loc *lookupLocator, q lookupQuery, name string, pool []grove.SymbolRecord) map[string]any {
	var conflicts, others []grove.SymbolRecord
	for _, s := range pool {
		if _, _, conflict := loc.lookupMatch(q, s, false); conflict {
			conflicts = append(conflicts, s)
		} else if _, _, conflict := loc.lookupMatch(q, s, true); conflict {
			conflicts = append(conflicts, s)
		} else {
			others = append(others, s)
		}
	}
	owner := q.owner()
	ownedBy := func(s grove.SymbolRecord) bool {
		segs := symbolSegs(s)
		return owner != "" && len(segs) >= 2 && strings.EqualFold(segs[len(segs)-2], owner)
	}
	// Hits owned by the named type go first: for "Context.Accepted" the
	// closest useful symbol is Context.SetAccepted, not another type's member.
	sort.SliceStable(others, func(i, j int) bool { return ownedBy(others[i]) && !ownedBy(others[j]) })
	near := h.typoSuggestions(ctx, q)

	var ordered []grove.SymbolRecord
	seen := map[string]bool{}
	for _, group := range [][]grove.SymbolRecord{conflicts, near, others} {
		for _, s := range group {
			if !seen[s.ID] {
				seen[s.ID] = true
				ordered = append(ordered, s)
			}
		}
	}
	if len(ordered) == 0 {
		return map[string]any{
			"symbol": nil, "name": name, "matched": false,
			"note": fmt.Sprintf("no symbol named %q in the index — check the spelling, "+
				"or use prism_search for a name fragment", name),
		}
	}
	cands := make([]string, 0, 6)
	for _, s := range ordered[:minInt(6, len(ordered))] {
		cands = append(cands, lookupMissLabel(s))
	}
	if len(pool) == 0 {
		// Nothing carries the name: only spelling suggestions.
		return map[string]any{
			"symbol": nil, "name": name, "matched": false, "candidates": cands,
			"note": fmt.Sprintf("no symbol named %q in the index — did you mean one of the candidates?", name),
		}
	}
	primary := ordered[0]
	var note string
	switch {
	case len(conflicts) > 0:
		where := strings.Join(q.segs[:len(q.segs)-1], ".")
		if q.pathHint != "" {
			if where != "" {
				where = q.pathHint + " " + where
			} else {
				where = q.pathHint
			}
		}
		note = fmt.Sprintf("NO EXACT MATCH for %q: %s exists, but not in %s; the qualifier does not match its owner, package or path. Candidates are listed; pick one by its qualified name or file.",
			name, q.name(), where)
	case len(near) > 0:
		note = fmt.Sprintf("NO EXACT MATCH for %q — did you mean one of the candidates?", name)
	default:
		note = fmt.Sprintf("NO EXACT MATCH for %q; the candidates are related names, none of them is %q.", name, name)
	}
	stripped := primary
	stripped.RawText = ""
	stripped.CallSites = nil
	out := map[string]any{
		"symbol":     stripped,
		"name":       name,
		"matched":    false,
		"candidates": cands,
		"note":       note,
	}
	if ownedBy(primary) {
		out["symbol"] = primary
		out["content"] = primary.RawText
	}
	return out
}

func lookupMissLabel(s grove.SymbolRecord) string {
	label := fmt.Sprintf("%s (%s:%d)", lookupSymbolName(s), s.FilePath, s.Span.Start)
	sig := strings.TrimSpace(strings.SplitN(s.Signature, "\n", 2)[0])
	if sig != "" && sig != s.Name && sig != lookupSymbolName(s) {
		if len(sig) > 110 {
			sig = sig[:107] + "..."
		}
		label += " — " + sig
	}
	return label
}

// typoSuggestions returns indexed symbols whose names are a small edit away
// from q's name: members of the named owner type first, then a global scan
// seeded by substring probes that survive one edit.
func (h *Handler) typoSuggestions(ctx context.Context, q lookupQuery) []grove.SymbolRecord {
	name := q.name()
	if len([]rune(name)) < 3 {
		return nil
	}
	thr := typoThreshold(name)
	owner := q.owner()
	var pool []grove.SymbolRecord
	if owner != "" {
		if ts, err := h.Grove.SearchSymbols(ctx, owner, 25); err == nil {
			files := map[string]bool{}
			for _, t := range ts {
				segs := symbolSegs(t)
				if !strings.EqualFold(segs[len(segs)-1], owner) || files[t.FilePath] || len(files) >= 5 {
					continue
				}
				files[t.FilePath] = true
				if fs, err := h.Grove.FileSymbols(ctx, t.FilePath); err == nil {
					pool = append(pool, fs...)
				}
			}
		}
	}
	for _, p := range typoProbes(name) {
		if more, err := h.Grove.SearchSymbols(ctx, p, 300); err == nil {
			pool = append(pool, more...)
		}
	}
	type cand struct {
		s       grove.SymbolRecord
		d       int
		ownerOK bool
	}
	var cands []cand
	seen := map[string]bool{}
	for _, s := range dedupeSymbolsByID(pool) {
		if strings.EqualFold(s.Name, name) {
			continue // same name: a qualifier conflict or case variant, not a typo
		}
		d := osaDistance(s.Name, name)
		if d > thr {
			continue
		}
		key := lookupSymbolName(s) + "\x00" + s.FilePath
		if seen[key] {
			continue
		}
		seen[key] = true
		segs := symbolSegs(s)
		ownerOK := owner == "" || len(segs) >= 2 && strings.EqualFold(segs[len(segs)-2], owner)
		cands = append(cands, cand{s, d, ownerOK})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.ownerOK != b.ownerOK {
			return a.ownerOK
		}
		if a.d != b.d {
			return a.d < b.d
		}
		if ta, tb := isTestDouble(a.s.FilePath), isTestDouble(b.s.FilePath); ta != tb {
			return !ta
		}
		return lookupSymbolName(a.s) < lookupSymbolName(b.s)
	})
	out := make([]grove.SymbolRecord, 0, 5)
	for _, c := range cands[:minInt(5, len(cands))] {
		out = append(out, c.s)
	}
	return out
}
