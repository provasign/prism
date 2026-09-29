package mcp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
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

	// Resolved call edges cannot see calls through an external interface whose
	// declaration is absent from the index. Complete the family with production
	// enclosing symbols from one exhaustive call-shaped text pass. This is the
	// five-site gap in grafana QueryData (QueryMetricsV2, plugin.QueryData,
	// handlePreparedQuery, executeConcurrentQueries, handleQuerySingleDatasource).
	textResult := textsearch.Search(ctx, h.Root, "."+leaf+"(", textsearch.Options{
		MaxHits: 100000, Timeout: textSearchTimeout, Exhaustive: true,
	})
	fileSymbols := map[string][]grove.SymbolRecord{}
	textAdded := 0
	for _, hit := range textResult.Hits {
		if isVerifiedTestCaller("/" + hit.File) {
			continue
		}
		syms, ok := fileSymbols[hit.File]
		if !ok {
			syms, _ = h.Grove.FileSymbols(ctx, hit.File)
			fileSymbols[hit.File] = syms
		}
		enclosing := tightestEnclosingSymbol(syms, hit.Line)
		if enclosing == nil || seen[symbolSiteKey(*enclosing)] {
			continue
		}
		seen[symbolSiteKey(*enclosing)] = true
		result.Callers = append(result.Callers, *enclosing)
		textAdded++
	}
	if textAdded > 0 {
		result.HasHeuristicRefs = true
	}
	note := fmt.Sprintf("inferred one external-interface method family from %d local %s implementations with compatible parameter shape %q; callers union file-scoped resolved impacts plus %d production enclosing symbols from one exhaustive call-shaped text pass", len(selected), leaf, shape, textAdded)
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

// localContractOwners lists the project's own interface declarations of a
// bare member name (members indexed with the "declaration" annotation: Go
// and TS interface methods, bodiless contract members). Non-empty means the
// name is a local contract, not an external one.
func (h *Handler) localContractOwners(ctx context.Context, query string) []grove.SymbolRecord {
	head := query
	if i := strings.IndexByte(head, '('); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	if strings.Contains(head, ".") {
		return nil // already owner-qualified
	}
	candidates, err := h.Grove.SearchSymbols(ctx, head, exhaustiveSymbolCap)
	if err != nil {
		return nil
	}
	var owners []grove.SymbolRecord
	seen := map[string]bool{}
	for _, c := range candidates {
		if c.Name != head || c.ParentSymbol == "" || !hasAnnotation(c, "declaration") {
			continue
		}
		if key := c.QualifiedName + "@" + c.FilePath; !seen[key] {
			seen[key] = true
			owners = append(owners, c)
		}
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].QualifiedName != owners[j].QualifiedName {
			return owners[i].QualifiedName < owners[j].QualifiedName
		}
		return owners[i].FilePath < owners[j].FilePath
	})
	return owners
}

func hasAnnotation(s grove.SymbolRecord, want string) bool {
	for _, a := range s.Annotations {
		if a == want {
			return true
		}
	}
	return false
}

// localContractError keeps grove's ambiguity error and names the local
// interfaces that declare the member, so the retry is one qualified call.
func localContractError(query string, owners []grove.SymbolRecord, orig error) error {
	var b strings.Builder
	for i, o := range owners {
		if i == 12 {
			fmt.Fprintf(&b, "\n  … %d more", len(owners)-i)
			break
		}
		fmt.Fprintf(&b, "\n  %s  (%s:%d)", o.QualifiedName, o.FilePath, o.Span.Start)
	}
	return fmt.Errorf("%w\n\nLOCAL CONTRACT: this project declares %s in its own interface(s):%s\n"+
		"Re-run change_impact with the owner-qualified name (e.g. %s): its family is the "+
		"types that implement that interface, and look-alike methods of unrelated types "+
		"stay out. For an interface declared outside this repository, qualify with that "+
		"interface's name instead (pkg.Interface.Method).",
		orig, leafOf(query), b.String(), owners[0].QualifiedName)
}

// ambiguousCandidateLine is one candidate in grove's ambiguity error:
// "  Binding.Name  (binding/binding.go:33)".
var ambiguousCandidateLine = regexp.MustCompile(`(?m)^\s+(\S+)\s+\(([^():]+):(\d+)\)\s*$`)

// pickAmbiguousBySignature narrows grove's own short candidate list by the
// caller's signature. ok only when exactly one candidate matches.
func (h *Handler) pickAmbiguousBySignature(ctx context.Context, err error, query, signature string) (grove.SymbolRecord, bool) {
	leaf := query
	if i := strings.IndexByte(leaf, '('); i >= 0 {
		leaf = leaf[:i]
	}
	leaf = leafOf(strings.TrimSpace(leaf))
	want := paramListNamed(signature, leaf)
	var match []grove.SymbolRecord
	for _, m := range ambiguousCandidateLine.FindAllStringSubmatch(err.Error(), -1) {
		qn, file := m[1], m[2]
		line, _ := strconv.Atoi(m[3])
		syms, ferr := h.Grove.FileSymbols(ctx, file)
		if ferr != nil {
			continue
		}
		for _, s := range syms {
			if s.QualifiedName == qn && s.Span.Start == line &&
				paramsMatch(want, paramListNamed(s.Signature, leaf), s.Language, nil) {
				match = append(match, s)
			}
		}
	}
	if len(match) != 1 {
		return grove.SymbolRecord{}, false
	}
	return match[0], true
}
