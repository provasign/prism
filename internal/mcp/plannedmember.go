package mcp

import (
	"context"
	"fmt"
	"github.com/provasign/prism/internal/grove"
	"sort"
	"strings"
)

// plannedMemberImplementations answers missing-implementations for a
// member the contract does not declare yet (GAP-04): "what if I add M to
// I" used to stop at 'declares no member'. Every implementer of the type is
// listed: those without a member of that name as missing, those with one
// as already having it (with its signature; signature= marks which match).
func (h *Handler) plannedMemberImplementations(ctx context.Context, query, file, signature string) (map[string]any, bool) {
	i := strings.LastIndexByte(query, '.')
	if i <= 0 || i == len(query)-1 {
		return nil, false
	}
	typeName, member := query[:i], query[i+1:]
	typeID, typeLabel, _, _, err := h.rootSymbol(ctx, typeName, file)
	if err != nil {
		return nil, false
	}
	impls, err := h.Grove.Edges(ctx, typeID, "in", []string{"implements", "extends"})
	if err != nil {
		return nil, false
	}
	var missing, having []map[string]any
	matching := 0
	seen := map[string]bool{}
	for _, impl := range impls {
		if impl.ID == "" || seen[impl.ID] {
			continue
		}
		seen[impl.ID] = true
		row := map[string]any{"name": impl.Name, "qualifiedName": impl.Name, "filePath": normalizePath(impl.File), "line": impl.Line, "kind": impl.Kind}
		has := ""
		if syms, err := h.Grove.FileSymbols(ctx, impl.File); err == nil {
			for _, s := range syms {
				if s.Name == member && (s.QualifiedName == impl.Name+"."+member || strings.HasSuffix(s.QualifiedName, "."+impl.Name+"."+member)) {
					has = s.Signature
					row["line"] = s.Span.Start
					break
				}
			}
		}
		if has == "" {
			missing = append(missing, row)
			continue
		}
		row["signature"] = has
		if signature != "" && compactSignature(has) != "" && strings.Contains(compactSignature(has), compactSignature(signature)) {
			row["signatureMatches"] = true
			matching++
		}
		having = append(having, row)
	}
	if missing == nil {
		missing = []map[string]any{}
	}
	note := fmt.Sprintf("%s does not declare %s yet: every implementer of %s would need it. %d of %d already have a member named %s",
		typeLabel, member, typeLabel, len(having), len(having)+len(missing), member)
	if signature != "" {
		note += fmt.Sprintf("; %d with a signature matching %q", matching, signature)
	}
	out := map[string]any{
		"query":            query,
		"contract":         []map[string]any{},
		"missing":          missing,
		"implementedCount": len(having),
		"planned":          true,
		"note":             note + ".",
	}
	if len(having) > 0 {
		out["alreadyHave"] = having
	}
	addIndexedCompletenessSafety(out)
	return out, true
}

// compactSignature drops spaces, for a containment check.
func compactSignature(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// formerImplementers lists Go types that have every other member of the
// contract's interface but not the queried one, and are not already listed
// as missing (BUG-03). Before the member was added they satisfied the
// interface; Go has no implements clause, so once it is added and nothing
// uses them as the interface they drop out of the family silently, while
// missing-implementations before the edit (GAP-04) listed them. The
// compiler does not require them to change; whether they were meant to
// implement it is the author's call. Matched by method names.
func (h *Handler) formerImplementers(ctx context.Context, r *grove.MissingImplementationsResult) []map[string]any {
	if r == nil || len(r.Contract) == 0 {
		return nil
	}
	c := r.Contract[0]
	if c.Language != "go" {
		return nil
	}
	i := strings.LastIndexByte(c.QualifiedName, '.')
	if i <= 0 {
		return nil
	}
	iface, member := c.QualifiedName[:i], c.Name
	syms, err := h.Grove.FileSymbols(ctx, c.FilePath)
	if err != nil {
		return nil
	}
	var others []string
	for _, s := range syms {
		if strings.HasPrefix(s.QualifiedName, iface+".") && s.Name != member && !strings.Contains(s.QualifiedName[len(iface)+1:], ".") {
			others = append(others, s.Name)
		}
	}
	if len(others) == 0 {
		return nil // every type satisfied an empty interface
	}
	owners := func(name string) map[string]grove.ResolvedSymbol {
		out := map[string]grove.ResolvedSymbol{}
		cands, err := h.Grove.Resolve(ctx, name)
		if err != nil {
			return out
		}
		for _, cand := range cands {
			if j := strings.LastIndexByte(cand.Name, '.'); j > 0 && cand.Name[j+1:] == name {
				out[cand.Name[:j]] = cand
			}
		}
		return out
	}
	common := owners(others[0])
	for _, m := range others[1:] {
		has := owners(m)
		for t := range common {
			if _, ok := has[t]; !ok {
				delete(common, t)
			}
		}
	}
	skip := map[string]bool{iface: true}
	for t := range owners(member) {
		skip[t] = true // already has the member
	}
	for _, m := range r.Missing {
		skip[m.QualifiedName], skip[m.Name] = true, true
	}
	var out []map[string]any
	for t, sym := range common {
		if skip[t] {
			continue
		}
		out = append(out, map[string]any{"name": t, "qualifiedName": t, "filePath": normalizePath(sym.File), "line": sym.Line, "kind": "type"})
	}
	sort.Slice(out, func(a, b int) bool { return fmt.Sprint(out[a]["name"]) < fmt.Sprint(out[b]["name"]) })
	return out
}
