package mcp

import (
	"context"
	"fmt"
	"sort"
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

// nominalFamilyLanguages declare overriding explicitly (extends/implements,
// override): every member of a family is reachable from a local root, so a
// wide bare name is a set of distinct families, not one external contract to
// infer from parameter shapes. Go satisfies interfaces implicitly and keeps
// the inference (grafana QueryData, external interface, no local root).
var nominalFamilyLanguages = map[string]bool{"java": true, "kotlin": true, "csharp": true, "scala": true, "swift": true, "python": true, "php": true}

// localFamily is one override family a wide bare name partitions into.
type localFamily struct {
	root    grove.SymbolRecord
	result  *grove.ChangeImpactResult
	members int
}

// partitionLocalFamilies splits a wide bare member name in a nominal
// language into the override families the graph already knows: query one
// uncovered candidate, mark everything its change set declares, repeat.
// jackson-databind (2026-10-12): serializeWithType names 57 methods in two
// unrelated families, JsonSerializer's (~40) and JsonSerializable's (16).
// The parameter-shape inference split JsonSerializer's overrides by their
// first parameter type (QName, Path, Blob...) and answered with the 16 as
// "one family", silently. ok is false when the name is not nominal-language
// or any candidate stays uncovered (an external root: inference applies).
func (h *Handler) partitionLocalFamilies(ctx context.Context, query string) ([]localFamily, bool) {
	families, ok, _ := h.partitionLocalFamiliesCapped(ctx, query)
	return families, ok
}

// partitionLocalFamiliesCapped is partitionLocalFamilies that also reports
// whether it stopped at the query cap: an unfinished partition is not
// evidence of an external root, so the caller must not infer one.
func (h *Handler) partitionLocalFamiliesCapped(ctx context.Context, query string) ([]localFamily, bool, bool) {
	leaf := query
	if i := strings.IndexByte(leaf, '('); i >= 0 {
		leaf = leaf[:i]
	}
	leaf = strings.TrimSpace(leaf)
	if leaf == "" || strings.Contains(leaf, ".") {
		return nil, false, false
	}
	candidates, err := h.Grove.SearchSymbols(ctx, leaf, exhaustiveSymbolCap)
	if err != nil {
		return nil, false, false
	}
	var methods []grove.SymbolRecord
	for _, c := range candidates {
		if c.Name != leaf || (c.Kind != "method" && c.Kind != "function") {
			continue
		}
		if !nominalFamilyLanguages[c.Language] {
			return nil, false, false
		}
		methods = append(methods, c)
	}
	if len(methods) < 2 {
		return nil, false, false
	}
	sort.Slice(methods, func(i, j int) bool {
		if methods[i].FilePath != methods[j].FilePath {
			return methods[i].FilePath < methods[j].FilePath
		}
		return methods[i].Span.Start < methods[j].Span.Start
	})
	key := func(s grove.SymbolRecord) string { return normalizePath(s.FilePath) + ":" + strconv.Itoa(s.Span.Start) }
	// Query each candidate not yet seen in a change set, then merge change
	// sets that share a declaration: an override's own change set can be
	// just its branch (S0.swt) while its base's (Ser.swt) holds the whole
	// family, and processing order must not split one family into many.
	type part struct {
		keys    map[string]bool
		root    grove.SymbolRecord
		queried grove.SymbolRecord // the declaration result answers for
		result  *grove.ChangeImpactResult
		sites   int
	}
	var parts []*part
	var unresolved []grove.SymbolRecord
	seen := map[string]bool{}
	const maxQueries = 24
	queries := 0
	ask := func(m grove.SymbolRecord) (*grove.ChangeImpactResult, bool) {
		if queries == maxQueries {
			return nil, false
		}
		queries++
		r, cerr := h.Grove.ChangeImpactScoped(ctx, displayQN(m), m.FilePath)
		if (cerr != nil || r == nil) && m.QualifiedName != "" && m.QualifiedName != displayQN(m) {
			// A member of a nested type (EnumSerializationTest.SerializableEnum)
			// may resolve only under its full name.
			r, cerr = h.Grove.ChangeImpactScoped(ctx, m.QualifiedName, m.FilePath)
		}
		return r, cerr == nil && r != nil
	}
	// rootOf: the declaration being overridden when there is one (Able.swt
	// for A0.swt), else the queried declaration itself; keys gains every
	// declaration of the change set.
	rootOf := func(r *grove.ChangeImpactResult, m grove.SymbolRecord, keys map[string]bool) grove.SymbolRecord {
		var root grove.SymbolRecord
		for _, group := range [][]grove.SymbolRecord{r.Supers, r.Declarations, r.Family} {
			for _, s := range group {
				if s.Name != leaf {
					continue
				}
				keys[key(s)] = true
				if root.Name == "" {
					root = s
				}
			}
		}
		if root.Name == "" {
			root = m
		}
		return root
	}
	for _, m := range methods {
		if seen[key(m)] {
			continue
		}
		if queries == maxQueries {
			return nil, false, true
		}
		r, ok := ask(m)
		if !ok {
			// Listed on its own rather than aborting the partition or being
			// guessed into a family.
			unresolved = append(unresolved, m)
			seen[key(m)] = true
			continue
		}
		p := &part{keys: map[string]bool{key(m): true}, queried: m, result: r, sites: len(impactSites(r, true))}
		p.root = rootOf(r, m, p.keys)
		// Ask the root now: an override's change set holds itself and its
		// supers, the root's holds every sibling. Queried in path order,
		// overrides sorting before their interface each cost a query and
		// the cap was hit (databind createContextual: 30 overrides).
		for hops := 0; hops < 4 && key(p.root) != key(p.queried) && !seen[key(p.root)]; hops++ {
			rr, ok := ask(p.root)
			if !ok {
				break
			}
			p.queried, p.result, p.sites = p.root, rr, len(impactSites(rr, true))
			p.root = rootOf(rr, p.root, p.keys)
		}
		for k := range p.keys {
			seen[k] = true
		}
		parts = append(parts, p)
	}
	// Union parts that share any declaration; the widest change set speaks
	// for the merged family (it is the base's, which includes the others).
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(parts) && !merged; i++ {
			for j := i + 1; j < len(parts) && !merged; j++ {
				shared := false
				for k := range parts[j].keys {
					if parts[i].keys[k] {
						shared = true
						break
					}
				}
				if !shared {
					continue
				}
				a, b := parts[i], parts[j]
				if b.sites > a.sites {
					a, b = b, a
				}
				for k := range b.keys {
					a.keys[k] = true
				}
				parts[i] = a
				parts = append(parts[:j], parts[j+1:]...)
				merged = true
			}
		}
	}
	// A family answers from its root. A subclass's change set names the base
	// it overrides, which marks the base seen, so the base itself may never
	// have been queried, and a merged family kept one subclass's result:
	// bare "swt" over a base with eight overrides answered 4 sites, the base
	// 18. Query the root (and its own super, until stable).
	for _, p := range parts {
		for hops := 0; hops < 4 && key(p.root) != key(p.queried); hops++ {
			r, ok := ask(p.root)
			if !ok {
				break
			}
			p.queried, p.result, p.sites = p.root, r, len(impactSites(r, true))
			p.root = rootOf(r, p.root, p.keys)
		}
	}
	covered := map[string]bool{}
	var families []localFamily
	for _, p := range parts {
		for k := range p.keys {
			covered[k] = true
		}
		families = append(families, localFamily{root: p.root, result: p.result, members: len(p.keys)})
	}
	if len(families) == 0 {
		return nil, false, false
	}
	// A candidate no family's change set holds and whose own query failed
	// stays visible as its own entry (result nil: never answered directly).
	for _, m := range unresolved {
		if !covered[key(m)] {
			covered[key(m)] = true
			families = append(families, localFamily{root: m, members: 1})
		}
	}
	for _, m := range methods {
		if !covered[key(m)] {
			return nil, false, false
		}
	}
	// Largest family first: the retry most callers mean is listed first.
	sort.SliceStable(families, func(i, j int) bool { return families[i].members > families[j].members })
	return families, true, false
}

// familiesAmbiguityError names each override family a wide bare name spans,
// with its size, as a retry grove's ambiguity parser and ambiguousImpactCalls
// understand. No change sets were merged and nothing was guessed.
func familiesAmbiguityError(query string, families []localFamily) error {
	var b strings.Builder
	for _, f := range families {
		fmt.Fprintf(&b, "\n  %s  (%s:%d)", displayQN(f.root), f.root.FilePath, f.root.Span.Start)
	}
	var sizes []string
	for _, f := range families {
		sizes = append(sizes, fmt.Sprintf("%s: %d", displayQN(f.root), f.members))
	}
	return fmt.Errorf("change-impact: %q is ambiguous — %d candidates:%s\nThese are %d separate override families (%s declarations each: %s); no change sets were merged. Run change_impact once per family you are changing",
		query, len(families), b.String(), len(families), leafOf(query), strings.Join(sizes, ", "))
}
