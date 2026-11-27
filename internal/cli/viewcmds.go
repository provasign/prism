package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/core"
)

// Layer-3 view commands: `prism map` renders the component-level projection
// of the code graph (components + induced dependency edges + cycles);
// `prism cycles` is the dependency-cycle detail surface. Both are thin CLI
// fronts over the prism_map / prism_cycles tools.

func cmdMap(args []string) int {
	dir, dirSet := ".", false
	jsonOut := false
	var mapReq core.MapRequest
	it := newArgIter("map", args)
	for it.next() {
		switch it.Flag {
		case "--depth", "--max-sites":
			n, err := it.positiveInt()
			if err != nil {
				return usageError(err)
			}
			if it.Flag == "--depth" {
				mapReq.Depth = n
			} else {
				mapReq.MaxSites = n
			}
		case "--component":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			mapReq.Component = v
		case "--expand":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			from, to, ok := strings.Cut(v, "->")
			if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
				fmt.Fprintln(os.Stderr, "map: --expand wants 'from->to'")
				return 2
			}
			mapReq.ExpandFrom, mapReq.ExpandTo = strings.TrimSpace(from), strings.TrimSpace(to)
		case "--tests":
			mapReq.IncludeTests = true
		case "--all-components":
			mapReq.IncludeAuxiliary = true
		case "--no-repos":
			mapReq.NoRepos = true
		case "--json":
			jsonOut = true
		case "--format":
			// Accept the flag every other command uses; "text" is the default.
			f, err := it.format()
			if err != nil {
				return usageError(err)
			}
			jsonOut = f != formatText
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("map", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, err := invokeTool(dir, mapReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "map:", err)
		return exitForError(err)
	}
	if jsonOut {
		printJSON(out)
		return 0
	}
	renderMapText(toMap(out))
	return 0
}

func cmdCycles(args []string) int {
	dir, dirSet := ".", false
	jsonOut := false
	var cyclesReq core.CyclesRequest
	it := newArgIter("cycles", args)
	for it.next() {
		switch it.Flag {
		case "--depth":
			n, err := it.positiveInt()
			if err != nil {
				return usageError(err)
			}
			cyclesReq.Depth = n
		case "--tests":
			cyclesReq.IncludeTests = true
		case "--json":
			jsonOut = true
		case "--format":
			// Accept the flag every other command uses; "text" is the default.
			f, err := it.format()
			if err != nil {
				return usageError(err)
			}
			jsonOut = f != formatText
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("cycles", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, err := invokeTool(dir, cyclesReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cycles:", err)
		return 1
	}
	if jsonOut {
		printJSON(out)
		return 0
	}
	renderCyclesText(toMap(out))
	return 0
}

// cmdArch validates declared architecture rules against the component view.
// Exit codes: 0 = pass (or no rules), 1 = violations found (CI-gateable),
// 2 = usage/config error.
func cmdArch(args []string) int {
	dir, dirSet := ".", false
	jsonOut := false
	var archReq core.ArchCheckRequest
	it := newArgIter("arch", args)
	for it.next() {
		switch it.Flag {
		case "--depth":
			n, err := it.positiveInt()
			if err != nil {
				return usageError(err)
			}
			archReq.Depth = n
		case "--deny":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			archReq.Deny = append(archReq.Deny, v)
		case "--tests":
			archReq.IncludeTests = true
		case "--strict":
			archReq.Strict = true
		case "--json":
			jsonOut = true
		case "--format":
			// Accept the flag every other command uses; "text" is the default.
			f, err := it.format()
			if err != nil {
				return usageError(err)
			}
			jsonOut = f != formatText
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("arch", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, err := invokeTool(dir, archReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch:", err)
		return exitInternal
	}
	m := toMap(out)
	if jsonOut {
		printJSON(out)
	} else {
		renderArchText(m)
	}
	if s, _ := m["status"].(string); s == "fail" {
		return 1
	}
	return 0
}

func renderArchText(m map[string]any) {
	if m == nil {
		fmt.Println("arch: empty result")
		return
	}
	status, _ := m["status"].(string)
	if status == "no-rules" {
		fmt.Println(m["note"])
		return
	}
	rules := asSliceAny(m["rules"])
	fmt.Printf("arch: %s — %d rules checked against %v induced edges\n",
		status, len(rules), m["checkedEdges"])
	if s, _ := m["scope"].(string); s != "" {
		fmt.Printf("scope: %s\n", s)
	}
	renderViolations(asSliceAny(m["violations"]), "VIOLATION")
	review := asSliceAny(m["needsReview"])
	if len(review) > 0 {
		fmt.Println("\nheuristic-tier evidence — review, not an automatic failure" +
			" (interface dispatch can attribute a call across the boundary;" +
			" --strict escalates):")
		renderViolations(review, "NEEDS REVIEW")
	}
	if status == "pass" {
		fmt.Println("no violations")
	}
	fmt.Println(m["completeness"])
}

func renderViolations(list []any, label string) {
	for _, viol := range list {
		vm, _ := viol.(map[string]any)
		if vm == nil {
			continue
		}
		em, _ := vm["edge"].(map[string]any)
		if em == nil {
			continue
		}
		fmt.Printf("\n%s of \"%v\"  [tier: %v]\n", label, vm["rule"], vm["minTier"])
		fmt.Printf("  %v -> %v  %v crossing(s) (%s)\n", em["from"], em["to"],
			em["weight"], kindLine(em["kinds"]))
		for _, s := range asSliceAny(em["sites"]) {
			sm, _ := s.(map[string]any)
			if sm == nil {
				continue
			}
			fmt.Printf("    %s:%v  %s -> %s  (%s, %s)\n", sm["fromFile"],
				sm["fromLine"], sm["fromSymbol"], sm["toSymbol"],
				sm["kind"], sm["tier"])
		}
	}
}

// cmdVerify checks a diff (working tree vs --base, default HEAD) for
// completeness: missed change-impact sites, new
// cross-component dependencies, introduced arch violations.
// Exit codes: 0 = complete/clean, 1 = incomplete (CI-gateable), 2 = error.
func cmdVerify(args []string) int {
	dir, dirSet := ".", false
	jsonOut := false
	var verifyReq core.VerifyRequest
	it := newArgIter("verify", args)
	for it.next() {
		switch it.Flag {
		case "--base":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			verifyReq.Base = v
		case "--strict":
			verifyReq.Strict = true
		case "--removed":
			// Mid-loop residual check: --removed a,b,c reports exact
			// identifier mentions instead of running the full diff check.
			syms, err := it.list()
			if err != nil {
				return usageError(err)
			}
			verifyReq.RemovedSymbols = syms
		case "--json":
			jsonOut = true
		case "--format":
			// Every other prism command spells this --format; verify only
			// understood --json, so the documented flag silently produced
			// the text view and a script asking for JSON parsed prose.
			f, err := it.format()
			if err != nil {
				return usageError(err)
			}
			jsonOut = f != formatText
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("verify", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, text, rendered, err := invokeToolText(dir, verifyReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 2
	}
	m := toMap(out)
	if jsonOut {
		printJSON(out)
	} else {
		printToolResult("verify", out, text, rendered, formatText)
	}
	if failed, _ := m["gateFailure"].(bool); failed {
		return 1
	}
	return 0
}

// toMap round-trips a tool result through JSON into a generic map so the
// renderers below work on the same shape the MCP surface serves.
func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func renderMapText(m map[string]any) {
	if m == nil {
		fmt.Println("map: empty result")
		return
	}
	if renderCrossEdgeText(m) {
		return
	}
	// Expansion result: one edge, full sites.
	if edge, ok := m["edge"].(map[string]any); ok {
		fmt.Printf("%s -> %s  weight %v  [%s]\n", edge["from"], edge["to"],
			edge["weight"], tierLine(edge["tiers"]))
		for _, s := range asSliceAny(edge["sites"]) {
			site, _ := s.(map[string]any)
			if site == nil {
				continue
			}
			fmt.Printf("  %s:%v  %s -> %s  (%s, %s)\n", site["fromFile"],
				site["fromLine"], site["fromSymbol"], site["toSymbol"],
				site["kind"], site["tier"])
		}
		fmt.Println(m["completeness"])
		return
	}

	comps := asSliceAny(m["components"])
	edges := asSliceAny(m["edges"])
	fmt.Printf("view: %d components, %d induced edges", len(comps), len(edges))
	if d, ok := m["depth"].(float64); ok && d > 0 {
		fmt.Printf(" (depth %d)", int(d))
	}
	fmt.Println()
	if s, _ := m["scope"].(string); s != "" {
		fmt.Printf("scope: %s\n", s)
	}
	if ts := tierLine(m["tierSummary"]); ts != "" {
		fmt.Printf("evidence: %s · weakest tier: %v\n", ts, m["minTier"])
	}
	fmt.Println()

	fmt.Println("components (symbols · exported · fan-in/fan-out):")
	nameW := 0
	for _, c := range comps {
		if cm, _ := c.(map[string]any); cm != nil {
			if n, _ := cm["name"].(string); len(n) > nameW {
				nameW = len(n)
			}
		}
	}
	for _, c := range comps {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		fmt.Printf("  %-*v  %4v · %v exported · in %v / out %v\n", nameW,
			cm["name"], cm["symbols"], cm["exported"], cm["fanIn"], cm["fanOut"])
	}
	fmt.Println()

	fmt.Println("edges (by weight; expand with --expand 'from->to'):")
	for _, e := range edges {
		em, _ := e.(map[string]any)
		if em == nil {
			continue
		}
		fmt.Printf("  %v -> %v  %v (%s)  [%s]\n", em["from"], em["to"],
			em["weight"], kindLine(em["kinds"]), tierLine(em["tiers"]))
	}

	renderCrossRepoText(m)

	cycles := asSliceAny(m["cycles"])
	if len(cycles) == 0 {
		fmt.Println("\ncycles: none")
	} else {
		fmt.Printf("\ncycles: %d (detail: prism cycles)\n", len(cycles))
		for _, c := range cycles {
			var parts []string
			for _, n := range asSliceAny(c) {
				parts = append(parts, fmt.Sprint(n))
			}
			fmt.Printf("  %s\n", strings.Join(parts, " <-> "))
		}
	}
	fmt.Println(m["completeness"])
}

func renderCyclesText(m map[string]any) {
	if m == nil {
		fmt.Println("cycles: empty result")
		return
	}
	cycles := asSliceAny(m["cycles"])
	if len(cycles) == 0 {
		fmt.Println("cycles: none")
		fmt.Println(m["completeness"])
		return
	}
	fmt.Printf("cycles: %d\n", len(cycles))
	for _, c := range cycles {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		var parts []string
		for _, n := range asSliceAny(cm["components"]) {
			parts = append(parts, fmt.Sprint(n))
		}
		fmt.Printf("\n  %s  [weakest tier: %v]\n", strings.Join(parts, " <-> "), cm["minTier"])
		for _, e := range asSliceAny(cm["edges"]) {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			fmt.Printf("    %v -> %v  %v (%s)\n", em["from"], em["to"],
				em["weight"], kindLine(em["kinds"]))
			for _, s := range asSliceAny(em["sites"]) {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				fmt.Printf("      %s:%v  %s -> %s\n", sm["fromFile"],
					sm["fromLine"], sm["fromSymbol"], sm["toSymbol"])
			}
		}
	}
	fmt.Println(m["completeness"])
}

func asSliceAny(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	}
	return nil
}

// kindLine renders a {kind: count} map as "calls 80, imports 7",
// counts descending then name for determinism.
func kindLine(v any) string { return countLine(v, " ") }

// tierLine renders a {tier: count} map as "measured 80 · heuristic 7".
func tierLine(v any) string { return countLine(v, " ") }

func countLine(v any, sep string) string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return ""
	}
	type kv struct {
		k string
		n float64
	}
	var items []kv
	for k, raw := range m {
		n, _ := raw.(float64)
		items = append(items, kv{k, n})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].k < items[j].k
	})
	var parts []string
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s%s%d", it.k, sep, int(it.n)))
	}
	return strings.Join(parts, ", ")
}

// crossLabel is "repo:component", or the component for this repository.
func crossLabel(v any) string {
	e, _ := v.(map[string]any)
	if r, _ := e["repo"].(string); r != "" {
		return fmt.Sprintf("%s:%v", r, e["component"])
	}
	return fmt.Sprint(e["component"])
}

// renderCrossRepoText prints the edges that cross into other repositories.
func renderCrossRepoText(m map[string]any) {
	if msg, _ := m["crossRepoError"].(string); msg != "" {
		fmt.Printf("\nother repositories: NOT READ (%s)\n", msg)
		return
	}
	cr, _ := m["crossRepo"].(map[string]any)
	if cr == nil {
		return
	}
	edges := asSliceAny(cr["edges"])
	repos := asSliceAny(cr["repos"])
	fmt.Printf("\nother repositories (%d; edges crossing the repository boundary; expand with --expand 'from->to'):\n", len(repos))
	if len(edges) == 0 {
		fmt.Println("  no edges between this repository and the others")
	}
	w := 0
	for _, e := range edges {
		em, _ := e.(map[string]any)
		if n := len(crossLabel(em["from"])) + len(crossLabel(em["to"])) + 4; n > w {
			w = n
		}
	}
	for _, e := range edges {
		em, _ := e.(map[string]any)
		if em == nil {
			continue
		}
		pair := crossLabel(em["from"]) + " -> " + crossLabel(em["to"])
		fmt.Printf("  %-8v %-*s  %4v  [%v]\n", em["direction"], w, pair, em["weight"], em["evidence"])
	}
	fmt.Printf("cross-repository coverage: %v\n", cr["coverage"])
}

// renderCrossEdgeText prints one expanded cross-repository edge.
func renderCrossEdgeText(m map[string]any) bool {
	e, _ := m["crossEdge"].(map[string]any)
	if e == nil {
		return false
	}
	fmt.Printf("%s -> %s  %v reference(s), %v  [%v]\n", crossLabel(e["from"]), crossLabel(e["to"]), e["weight"], e["direction"], e["evidence"])
	repo := ""
	if from, _ := e["from"].(map[string]any); from != nil {
		repo, _ = from["repo"].(string)
	}
	for _, s := range asSliceAny(e["sites"]) {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		at := fmt.Sprintf("%v:%v", sm["file"], sm["line"])
		if repo != "" {
			at = repo + ":" + at
		}
		fmt.Printf("  %s  %v  %v\n", at, sm["symbol"], sm["text"])
	}
	fmt.Printf("cross-repository coverage: %v\n", m["coverage"])
	return true
}
