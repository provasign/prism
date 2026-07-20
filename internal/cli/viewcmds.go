package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Layer-3 view commands: `prism map` renders the component-level projection
// of the code graph (components + induced dependency edges + cycles);
// `prism cycles` is the dependency-cycle detail surface. Both are thin CLI
// fronts over the prism_map / prism_cycles tools.

func cmdMap(args []string) int {
	dir := "."
	jsonOut := false
	callArgs := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--depth", "--max-sites":
			if i+1 < len(args) {
				var n int
				fmt.Sscanf(args[i+1], "%d", &n)
				callArgs[strings.TrimPrefix(strings.ReplaceAll(a, "-sites", "_sites"), "--")] = n
				i++
			}
		case "--component":
			if i+1 < len(args) {
				callArgs["component"] = args[i+1]
				i++
			}
		case "--expand":
			if i+1 < len(args) {
				from, to, ok := strings.Cut(args[i+1], "->")
				if !ok {
					fmt.Fprintln(os.Stderr, "map: --expand wants 'from->to'")
					return 2
				}
				callArgs["from"] = strings.TrimSpace(from)
				callArgs["to"] = strings.TrimSpace(to)
				i++
			}
		case "--tests":
			callArgs["include_tests"] = true
		case "--json":
			jsonOut = true
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_map", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "map:", err)
		return 1
	}
	if jsonOut {
		printJSON(out)
		return 0
	}
	renderMapText(toMap(out))
	return 0
}

func cmdCycles(args []string) int {
	dir := "."
	jsonOut := false
	callArgs := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--depth":
			if i+1 < len(args) {
				var n int
				fmt.Sscanf(args[i+1], "%d", &n)
				callArgs["depth"] = n
				i++
			}
		case "--tests":
			callArgs["include_tests"] = true
		case "--json":
			jsonOut = true
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_cycles", callArgs)
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
	dir := "."
	jsonOut := false
	callArgs := map[string]any{}
	var extraDeny []any
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--depth":
			if i+1 < len(args) {
				var n int
				fmt.Sscanf(args[i+1], "%d", &n)
				callArgs["depth"] = n
				i++
			}
		case "--deny":
			if i+1 < len(args) {
				extraDeny = append(extraDeny, args[i+1])
				i++
			}
		case "--tests":
			callArgs["include_tests"] = true
		case "--strict":
			callArgs["strict"] = true
		case "--json":
			jsonOut = true
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	if len(extraDeny) > 0 {
		callArgs["deny"] = extraDeny
	}
	out, err := invokeWithPersistentLedger(dir, "prism_arch_check", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch:", err)
		return 2
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
// completeness: missed change-impact sites, affected tests, new
// cross-component dependencies, introduced arch violations.
// Exit codes: 0 = complete/clean, 1 = incomplete (CI-gateable), 2 = error.
func cmdVerify(args []string) int {
	dir := "."
	jsonOut := false
	callArgs := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--base":
			if i+1 < len(args) {
				callArgs["base"] = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}
	out, err := invokeWithPersistentLedger(dir, "prism_verify", callArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 2
	}
	m := toMap(out)
	if jsonOut {
		printJSON(out)
	} else {
		renderVerifyText(m)
	}
	switch v, _ := m["verdict"].(string); v {
	case "incomplete":
		return 1
	case "review":
		if hasFlag(args, "--strict") {
			return 1
		}
	}
	return 0
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func renderVerifyText(m map[string]any) {
	if m == nil {
		fmt.Println("verify: empty result")
		return
	}
	verdict, _ := m["verdict"].(string)
	if verdict == "clean" {
		fmt.Printf("verify: clean — %v\n", m["note"])
		return
	}
	changed := asSliceAny(m["changedFiles"])
	fmt.Printf("verify: %s — %d changed files vs %v\n", verdict, len(changed), m["base"])

	if sigs := asSliceAny(m["signatureChanges"]); len(sigs) > 0 {
		fmt.Println("\ncontract changes detected:")
		for _, s := range sigs {
			sm, _ := s.(map[string]any)
			if sm == nil {
				continue
			}
			fmt.Printf("  %s:%v  %v\n", sm["file"], sm["line"], sm["reason"])
		}
	}

	missedList := asSliceAny(m["missedSites"])
	if len(missedList) > 0 {
		fmt.Printf("\nMISSED SITES (%d) — required by the change, not touched by the diff:\n", len(missedList))
		for _, ms := range missedList {
			mm, _ := ms.(map[string]any)
			if mm == nil {
				continue
			}
			fmt.Printf("  %s:%v  %v — %v\n", mm["file"], mm["line"], mm["qualifiedName"], mm["detail"])
		}
	}

	if unv := asSliceAny(m["unverifiedSeeds"]); len(unv) > 0 {
		fmt.Printf("\nUNVERIFIED contract changes (%d) — fail-closed, review these:\n", len(unv))
		for _, u := range unv {
			fmt.Printf("  %v\n", u)
		}
	}

	if deps := asSliceAny(m["newDependencies"]); len(deps) > 0 {
		fmt.Println("\ncross-component dependency candidates (all evidence in changed code; no base-graph comparison):")
		for _, d := range deps {
			dm, _ := d.(map[string]any)
			if dm == nil {
				continue
			}
			fmt.Printf("  %v -> %v  %v crossing(s)  [tier: %v]\n",
				dm["from"], dm["to"], dm["weight"], dm["minTier"])
		}
	}
	if as, _ := m["archStatus"].(string); as == "fail" || as == "review" {
		fmt.Printf("\narch rules touched by this diff: %s\n", as)
		renderViolations(asSliceAny(m["archIntroduced"]), "ARCH")
	}

	if tests := asSliceAny(m["affectedTests"]); len(tests) > 0 {
		fmt.Printf("\naffected tests to run (%d):\n", len(tests))
		max := len(tests)
		if max > 15 {
			max = 15
		}
		for _, tv := range tests[:max] {
			tm, _ := tv.(map[string]any)
			if tm == nil {
				continue
			}
			fmt.Printf("  %v  (%s)\n", tm["name"], tm["file"])
		}
		if len(tests) > 15 {
			fmt.Printf("  … and %d more\n", len(tests)-15)
		}
	}
	for _, n := range asSliceAny(m["notes"]) {
		fmt.Printf("note: %v\n", n)
	}
	switch verdict {
	case "complete":
		fmt.Println("\nno missed sites — the diff covers its own blast radius")
	case "review":
		fmt.Println("\nverdict: review — some contract changes could not be verified (--strict exits 1)")
	}
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
	s, _ := v.([]any)
	return s
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
