package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// inferExternalMethodImpact turns a wide same-name ambiguity into one
// project-local family query. Go external interfaces are satisfied implicitly,
// so there is no implements edge or local interface declaration for Grove to
// anchor. The dominant local method signature is the contract fingerprint;
// callers are still obtained from each file-scoped, graph-resolved impact.
func (h *Handler) inferExternalMethodImpact(ctx context.Context, query, signature string) (*grove.ChangeImpactResult, string, error) {
	leaf := query
	if i := strings.IndexByte(leaf, '('); i >= 0 {
		leaf = leaf[:i]
	}
	leaf = leafOf(strings.TrimSpace(leaf))
	if leaf == "" {
		return nil, "", fmt.Errorf("cannot infer an external method family from %q", query)
	}
	candidates, err := h.Grove.SearchSymbols(ctx, leaf, exhaustiveSymbolCap)
	if err != nil {
		return nil, "", err
	}
	var methods []grove.SymbolRecord
	for _, c := range candidates {
		if c.Name != leaf || (c.Kind != "method" && c.Kind != "function") {
			continue
		}
		if !strings.Contains(c.Signature, leaf+"(") && !strings.Contains(c.Signature, leaf+" (") {
			continue
		}
		methods = append(methods, c)
	}
	if len(methods) == 0 {
		return nil, "", fmt.Errorf("no indexed methods named %s carry a comparable signature", leaf)
	}

	var selected []grove.SymbolRecord
	shape := ""
	if signature != "" {
		base := paramListNamed(signature, leaf)
		if base == "" && !strings.Contains(signature, leaf+"(") && !strings.Contains(signature, leaf+" (") {
			return nil, "", fmt.Errorf("signature must contain %s(...)", leaf)
		}
		for _, c := range methods {
			if paramsMatch(base, paramListNamed(c.Signature, leaf), c.Language, nil) {
				selected = append(selected, c)
			}
		}
		shape = base
	} else {
		clusters := map[string][]grove.SymbolRecord{}
		for _, c := range methods {
			key := contractParamKey(c)
			clusters[key] = append(clusters[key], c)
		}
		for key, cluster := range clusters {
			if len(cluster) > len(selected) || (len(cluster) == len(selected) && key < shape) {
				shape, selected = key, cluster
			}
		}
	}
	if len(selected) < wideMemberAmbiguityThreshold {
		return nil, "", fmt.Errorf("largest compatible %s signature family has only %d members", leaf, len(selected))
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].FilePath != selected[j].FilePath {
			return selected[i].FilePath < selected[j].FilePath
		}
		return selected[i].Span.Start < selected[j].Span.Start
	})
	result := &grove.ChangeImpactResult{
		Query:             query,
		Family:            selected,
		Completeness:      "project-local",
		OverridesExternal: []string{query},
	}
	seen := map[string]bool{}
	for _, m := range selected {
		seen[symbolSiteKey(m)] = true
	}
	appendUnique := func(dst *[]grove.SymbolRecord, syms []grove.SymbolRecord) {
		for _, s := range syms {
			key := symbolSiteKey(s)
			if seen[key] {
				continue
			}
			seen[key] = true
			*dst = append(*dst, s)
		}
	}
	for _, m := range selected {
		impact, impactErr := h.Grove.ChangeImpactScoped(ctx, displayQN(m), m.FilePath)
		if impactErr != nil {
			continue // the declaration remains in Family; only this caller slice is unavailable
		}
		appendUnique(&result.Supers, impact.Supers)
		appendUnique(&result.Family, impact.Family)
		appendUnique(&result.Callers, impact.Callers)
		appendUnique(&result.DeclaringTypes, impact.DeclaringTypes)
		result.HasHeuristicRefs = result.HasHeuristicRefs || impact.HasHeuristicRefs
	}
	note := fmt.Sprintf("inferred one external-interface method family from %d local %s implementations with compatible parameter shape %q; callers are the union of file-scoped resolved impacts", len(selected), leaf, shape)
	return result, note, nil
}

func contractParamKey(s grove.SymbolRecord) string {
	params := splitParams(paramListNamed(s.Signature, s.Name))
	for i := range params {
		params[i] = strings.ReplaceAll(paramType(params[i], s.Language), " ", "")
	}
	return s.Language + ":" + strings.Join(params, ",")
}

func symbolSiteKey(s grove.SymbolRecord) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("%s:%d:%s", s.FilePath, s.Span.Start, displayQN(s))
}
