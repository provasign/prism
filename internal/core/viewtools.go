package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/view"
)

// Layer-3 view tools: component-level projections of the code graph.
// prism_map renders the quotient view (components + induced dependency
// edges + cycles); prism_cycles is the dispatchable-only detail surface for
// the CLI. Every induced edge is expandable (from+to params return the full
// constituent site list) and reports the tier distribution of its
// constituent evidence so every abstract edge remains auditable.

// toolMap builds the component view of the whole indexed graph.
//
// Args: depth (0 = full directory granularity), max_sites (cap per edge,
// default 5), component (only edges touching it), from+to (expand one edge:
// full site list).
func (h *Handler) toolMap(ctx context.Context, args map[string]any) (any, error) {
	depth := intArg(args, "depth", 0)
	maxSites := intArg(args, "max_sites", 5)
	expandFrom := stringArg(args, "from", "")
	expandTo := stringArg(args, "to", "")
	component := stringArg(args, "component", "")
	includeTests := boolArg(args, "include_tests")
	excludeAux := !boolArg(args, "include_auxiliary")

	if (expandFrom == "") != (expandTo == "") {
		return nil, fmt.Errorf("expansion needs both from and to")
	}
	if expandFrom != "" {
		// Expansion: the full constituent evidence for one induced edge.
		v, err := h.buildView(ctx, depth, 1<<30, includeTests)
		if err != nil {
			return nil, err
		}
		e := v.Edge(expandFrom, expandTo)
		if e == nil {
			return nil, fmt.Errorf("no induced edge %s -> %s at depth %d", expandFrom, expandTo, depth)
		}
		h.Ledger.RecordCall("prism_map")
		return map[string]any{
			"edge":         e,
			"minTier":      view.MinTier(e.Tiers),
			"completeness": completenessAtTier(e.Tiers),
		}, nil
	}

	v, err := h.buildViewOpts(ctx, view.Options{Depth: depth, MaxSites: maxSites, IncludeTests: includeTests, ExcludeAuxiliary: excludeAux})
	if err != nil {
		return nil, err
	}
	edges := v.Edges
	if component != "" {
		// An unknown component used to print the whole map with no edges
		// and succeed (BUG-21).
		known := false
		names := make([]string, 0, len(v.Components))
		for _, c := range v.Components {
			known = known || c.Name == component
			names = append(names, c.Name)
		}
		if !known {
			for _, a := range v.AuxiliaryExcluded {
				if a == component {
					return nil, fmt.Errorf("component %q is auxiliary and not shown by default; pass include_auxiliary=true (CLI: --all-components)", component)
				}
			}
			return nil, fmt.Errorf("no component %q at depth %d; components: %s", component, v.Depth, strings.Join(names, ", "))
		}
		filtered := edges[:0:0]
		for _, e := range edges {
			if e.From == component || e.To == component {
				filtered = append(filtered, e)
			}
		}
		edges = filtered
	}
	cycles := v.Cycles()
	cycleSummary := make([][]string, 0, len(cycles))
	for _, c := range cycles {
		cycleSummary = append(cycleSummary, c.Components)
	}
	h.Ledger.RecordCall("prism_map")
	return map[string]any{
		"root":              h.Root,
		"depth":             v.Depth,
		"scope":             scopeLine(v),
		"testFilesExcluded": v.TestFilesExcluded,
		"auxiliaryExcluded": v.AuxiliaryExcluded,
		"components":        v.Components,
		"edges":             edges,
		"cycles":            cycleSummary,
		"tierSummary":       v.TierSummary,
		"minTier":           view.MinTier(v.TierSummary),
		"completeness":      completenessAtTier(v.TierSummary),
	}, nil
}

// toolCycles reports the strongly connected components of the induced graph
// with their member edges as evidence. Dispatchable (CLI, direct invoke) but
// not advertised in tools/list — prism_map already carries the summary.
func (h *Handler) toolCycles(ctx context.Context, args map[string]any) (any, error) {
	depth := intArg(args, "depth", 0)
	maxSites := intArg(args, "max_sites", 3)
	v, err := h.buildView(ctx, depth, maxSites, boolArg(args, "include_tests"))
	if err != nil {
		return nil, err
	}
	cycles := v.Cycles()
	h.Ledger.RecordCall("prism_cycles")
	out := make([]map[string]any, 0, len(cycles))
	for _, c := range cycles {
		tiers := map[string]int{}
		for _, e := range c.Edges {
			for t, n := range e.Tiers {
				tiers[t] += n
			}
		}
		out = append(out, map[string]any{
			"components": c.Components,
			"edges":      c.Edges,
			"minTier":    view.MinTier(tiers),
		})
	}
	return map[string]any{
		"root":         h.Root,
		"depth":        depth,
		"scope":        scopeLine(v),
		"cycles":       out,
		"count":        len(out),
		"completeness": completenessAtTier(v.TierSummary),
	}, nil
}

// toolArchCheck validates declared architecture rules (arch_deny lines in
// prism.yaml, optionally extended with a deny=[...] arg) against the induced
// component view. Deterministic; every violation cites concrete file:line
// sites and the tier of its weakest evidence. Dispatchable (CLI/CI/direct
// invoke), not advertised in tools/list.
func (h *Handler) toolArchCheck(ctx context.Context, args map[string]any) (any, error) {
	raw := append([]string(nil), h.Cfg.ArchDeny...)
	if extra, ok := args["deny"].([]any); ok {
		for _, r := range extra {
			if s, ok := r.(string); ok && s != "" {
				raw = append(raw, s)
			}
		}
	}
	if len(raw) == 0 {
		return map[string]any{
			"status": "no-rules",
			"note":   "no architecture rules declared — add arch_deny: \"<from> -> <to>\" lines to prism.yaml",
		}, nil
	}
	rules, err := view.ParseRules(raw)
	if err != nil {
		return nil, fmt.Errorf("arch rules: %w", err)
	}
	v, err := h.buildView(ctx, intArg(args, "depth", 0), intArg(args, "max_sites", 5),
		boolArg(args, "include_tests"))
	if err != nil {
		return nil, err
	}
	all := v.CheckRules(rules)
	h.Ledger.RecordCall("prism_arch_check")

	// Tier-aware gating: a violation whose ONLY evidence is heuristic-tier
	// (e.g. an interface-dispatch call attributed to a concrete type across
	// the boundary — the dependency-inversion pattern read backwards) is a
	// review item, not an automatic build break. Structural or stronger
	// evidence fails the gate. strict=true escalates review items to fail.
	strict := boolArg(args, "strict")
	var violations, needsReview []view.Violation
	for _, viol := range all {
		if viol.MinTier == "heuristic" && !strict {
			needsReview = append(needsReview, viol)
		} else {
			violations = append(violations, viol)
		}
	}
	status := "pass"
	switch {
	case len(violations) > 0:
		status = "fail"
	case len(needsReview) > 0:
		status = "review"
	}
	ruleStrs := make([]string, 0, len(rules))
	for _, r := range rules {
		ruleStrs = append(ruleStrs, r.Raw)
	}
	return map[string]any{
		"status":       status,
		"rules":        ruleStrs,
		"violations":   violations,
		"needsReview":  needsReview,
		"checkedEdges": len(v.Edges),
		"scope":        scopeLine(v),
		"completeness": completenessAtTier(v.TierSummary),
	}, nil
}

func (h *Handler) buildView(ctx context.Context, depth, maxSites int, includeTests bool) (*view.View, error) {
	return h.buildViewOpts(ctx, view.Options{Depth: depth, MaxSites: maxSites, IncludeTests: includeTests})
}

func (h *Handler) buildViewOpts(ctx context.Context, opts view.Options) (*view.View, error) {
	symbols, edges, err := h.Grove.SnapshotGraph(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph snapshot: %w", err)
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("repository is not indexed; run prism_index first")
	}
	return view.Build(symbols, edges, opts), nil
}

// boolArg reads an optional boolean tool argument.
func boolArg(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// scopeLine states what the view covers — exclusion is reported, never
// silent (a map that quietly dropped files would read as "covered
// everything" when it didn't).
func scopeLine(v *view.View) string {
	line := ""
	switch {
	case v.IncludeTests:
		line = "all indexed code including test files"
	case v.TestFilesExcluded == 0:
		line = "production code (no test files present)"
	default:
		line = fmt.Sprintf("production code (%d test files excluded; include_tests=true to include)", v.TestFilesExcluded)
	}
	if n := len(v.AuxiliaryExcluded); n > 0 {
		shown := v.AuxiliaryExcluded
		if n > 5 {
			shown = shown[:5]
		}
		line += fmt.Sprintf("; not shown: %d auxiliary component(s) (docs/examples, or isolated code in a language the rest does not use): %s", n, strings.Join(shown, ", "))
		if n > 5 {
			line += fmt.Sprintf(", +%d more", n-5)
		}
		line += "; include_auxiliary=true to include"
	}
	return line
}

// completenessAtTier states the honest claim a view result can make: it is
// complete over the indexed graph at the tier of its weakest constituent
// evidence — never "closed" (external dependencies are out of scope, and
// heuristic edges cap the claim). L2 task ops own "closed"; views do not.
func completenessAtTier(tiers map[string]int) string {
	return fmt.Sprintf("complete-at-tier:%s over indexed project edges; external dependencies excluded", view.MinTier(tiers))
}
