package mcp

import (
	"context"
	"fmt"
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
