package mcp

// Plain-text rendering for prism_read and prism_change_impact MCP results.
//
// Measured over the real protocol (Kinto index, 2026-08-18):
//   - prism_change_impact CacheBase.get: 5,830 B as JSON, 1,907 B as text —
//     3.1x. Symbol records are the most repetitive JSON this server emits
//     (name/qualifiedName/filePath/kind/signature keys per entry), and the
//     graph tools return lists of them.
//   - prism_read: +7–18% — the envelope fields plus JSON string escaping of
//     the entire source body (every newline, quote and tab).
// prism_read was 56% of all prism calls in the full38 bench; every byte of
// its result sits in the session cache and is re-paid on each later turn.
//
// Same contract as renderSearchAsText: render only shapes this code fully
// understands, fall back to JSON on ANY unrecognized field — a silent drop
// is worse than a bigger payload.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// renderReadAsText renders a prism_read result: a short header line, then
// the content verbatim (it is already line-numbered / compressed upstream).
func renderReadAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"file": true, "strategy": true, "originalTokens": true,
		"deliveredTokens": true, "savingsPercent": true, "content": true,
		"delivery": true, "startLine": true, "endLine": true,
		"totalLines": true, "warning": true, "note": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}
	content, ok := out["content"].(string)
	if !ok {
		return "", false
	}
	var b strings.Builder
	if sl, haveRange := out["startLine"]; haveRange {
		fmt.Fprintf(&b, "// %v lines %v-%v of %v\n", out["file"], sl, out["endLine"], out["totalLines"])
	} else {
		fmt.Fprintf(&b, "// %v", out["file"])
		if s, _ := out["strategy"].(string); s != "" && s != "verbatim" {
			fmt.Fprintf(&b, " [%s]", s)
		}
		b.WriteString("\n")
	}
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	if w, _ := out["warning"].(string); w != "" {
		fmt.Fprintf(&b, "// %s\n", w)
	}
	if n, _ := out["note"].(string); n != "" {
		fmt.Fprintf(&b, "// %s\n", n)
	}
	return b.String(), true
}

// renderChangeImpactAsText renders a prism_change_impact result in the same
// shape the CLI has always printed: grouped sites, one per line.
func renderChangeImpactAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"query": true, "declarations": true, "supers": true, "family": true,
		"callers": true, "totalSites": true, "declaringTypes": true,
		"declaringTypesNote": true, "completeness": true,
		"externalSupers": true, "overridesExternal": true, "warning": true,
		"widerAnchor": true, "hasHeuristicRefs": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %v — change-impact: %v site(s)\n", out["query"], out["totalSites"])
	if c, _ := out["completeness"].(string); c != "" {
		fmt.Fprintf(&b, "completeness: %s\n", c)
	}
	if hr, _ := out["hasHeuristicRefs"].(bool); hr {
		b.WriteString("// includes name-derived references (framework template/query bindings) — " +
			"probably right, not certain; verify before relying on it for a safety-critical change\n")
	}
	for _, sec := range []struct{ key, label string }{
		{"declarations", "declarations"},
		{"supers", "supers"},
		{"family", "family"},
		{"callers", "callers"},
		{"declaringTypes", "declaringTypes"},
	} {
		entries := anySlice(out[sec.key])
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s (%d):\n", sec.label, len(entries))
		for _, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				return "", false
			}
			name := m["qualifiedName"]
			if name == nil || name == "" {
				name = m["name"]
			}
			fmt.Fprintf(&b, "  %v  %v:%v", name, m["filePath"], m["line"])
			if via, _ := m["via"].(string); via != "" {
				fmt.Fprintf(&b, "  (via %s)", via)
			}
			b.WriteString("\n")
		}
	}
	if n, _ := out["declaringTypesNote"].(string); n != "" {
		fmt.Fprintf(&b, "// %s\n", n)
	}
	if es := anySlice(out["externalSupers"]); len(es) > 0 {
		fmt.Fprintf(&b, "externalSupers: %v\n", es)
	}
	if oe := anySlice(out["overridesExternal"]); len(oe) > 0 {
		fmt.Fprintf(&b, "overridesExternal: %v\n", oe)
	}
	if w, _ := out["warning"].(string); w != "" {
		fmt.Fprintf(&b, "// %s\n", w)
	}
	if wa, ok := out["widerAnchor"].(map[string]any); ok {
		fmt.Fprintf(&b, "// wider anchor: %v (%v sites, %v) — %v\n",
			wa["qualifiedName"], wa["totalSites"], wa["completeness"], wa["note"])
	}
	return b.String(), true
}

// renderLookupAsText renders a prism_lookup result. The JSON form shipped
// the symbol body TWICE (symbol.rawText and content, both string-escaped)
// plus index internals (id, blobSha, callSites) no agent uses: measured
// 1,072 B for 221 B of content. Text form: one header line, the body once.
func renderLookupAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"symbol": true, "content": true, "ambiguous": true, "candidates": true,
		"matched": true, "name": true, "note": true,
		// projectSymbol fields= shapes
		"file": true, "line": true, "signature": true, "sig": true,
		"doc": true, "docstring": true, "body": true, "source": true,
		"kind": true, "parent": true, "modifiers": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}
	var b strings.Builder
	if sym, ok := out["symbol"].(map[string]any); ok {
		name := sym["qualifiedName"]
		if name == nil || name == "" {
			name = sym["name"]
		}
		span, _ := sym["span"].(map[string]any)
		fmt.Fprintf(&b, "// %v %v  %v", sym["kind"], name, sym["filePath"])
		if span != nil {
			fmt.Fprintf(&b, ":%v-%v", span["start"], span["end"])
		}
		b.WriteString("\n")
	} else if s, isRecord := out["symbol"].(interface{ GetID() string }); isRecord {
		_ = s // never happens today; guard for future concrete types
		return "", false
	} else if _, present := out["symbol"]; present && out["symbol"] != nil {
		// A concrete grove.SymbolRecord (not yet a map): render via its JSON
		// form to avoid mispresenting fields we cannot see here.
		m := symbolToMap(out["symbol"])
		if m == nil {
			return "", false
		}
		name := m["qualifiedName"]
		if name == nil || name == "" {
			name = m["name"]
		}
		span, _ := m["span"].(map[string]any)
		fmt.Fprintf(&b, "// %v %v  %v", m["kind"], name, m["filePath"])
		if span != nil {
			fmt.Fprintf(&b, ":%v-%v", span["start"], span["end"])
		}
		b.WriteString("\n")
	}
	if c, ok := out["content"].(string); ok && c != "" {
		b.WriteString(c)
		if !strings.HasSuffix(c, "\n") {
			b.WriteString("\n")
		}
	}
	// Projection shape: no symbol/content, just the requested columns.
	if _, hasSym := out["symbol"]; !hasSym {
		for _, k := range []string{"name", "kind", "file", "line", "signature", "sig",
			"doc", "docstring", "parent", "modifiers", "body", "source"} {
			if v, ok := out[k]; ok && v != nil && v != "" {
				fmt.Fprintf(&b, "%s: %v\n", k, v)
			}
		}
	}
	if m, ok := out["matched"].(bool); ok && !m {
		b.WriteString("// NO EXACT MATCH — closest shown above; candidates:\n")
	} else if amb, _ := out["ambiguous"].(bool); amb {
		b.WriteString("// AMBIGUOUS — same score for:\n")
	}
	for _, c := range anySlice(out["candidates"]) {
		fmt.Fprintf(&b, "//   %v\n", c)
	}
	if n, _ := out["note"].(string); n != "" {
		fmt.Fprintf(&b, "// %s\n", n)
	}
	return b.String(), true
}

// symbolToMap round-trips a concrete symbol value through its JSON encoding
// into a generic map, so one rendering path serves both shapes.
func symbolToMap(v any) map[string]any {
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

// renderVerifyAsText mirrors the CLI's long-standing verify text output
// (internal/cli/viewcmds.go renderVerifyText) on the MCP surface.
func renderVerifyAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"verdict": true, "base": true, "note": true, "changedFiles": true,
		"signatureChanges": true, "missedSites": true, "unverifiedSeeds": true,
		"newDependencies": true, "archStatus": true, "archIntroduced": true,
		"notes": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}
	verdict, _ := out["verdict"].(string)
	var b strings.Builder
	if verdict == "clean" {
		fmt.Fprintf(&b, "verify: clean — %v\n", out["note"])
		return b.String(), true
	}
	changed := anySlice(out["changedFiles"])
	fmt.Fprintf(&b, "verify: %s — %d changed files vs %v\n", verdict, len(changed), out["base"])
	if sigs := anySlice(out["signatureChanges"]); len(sigs) > 0 {
		b.WriteString("\ncontract changes detected:\n")
		for _, s := range sigs {
			sm, ok := s.(map[string]any)
			if !ok {
				return "", false
			}
			fmt.Fprintf(&b, "  %v:%v  %v\n", sm["file"], sm["line"], sm["reason"])
		}
	}
	if missed := anySlice(out["missedSites"]); len(missed) > 0 {
		fmt.Fprintf(&b, "\nMISSED SITES (%d) — required by the change, not touched by the diff:\n", len(missed))
		for _, ms := range missed {
			mm, ok := ms.(map[string]any)
			if !ok {
				return "", false
			}
			fmt.Fprintf(&b, "  %v:%v  %v — %v\n", mm["file"], mm["line"], mm["qualifiedName"], mm["detail"])
		}
	}
	if unv := anySlice(out["unverifiedSeeds"]); len(unv) > 0 {
		fmt.Fprintf(&b, "\nUNVERIFIED contract changes (%d) — fail-closed, review these:\n", len(unv))
		for _, u := range unv {
			fmt.Fprintf(&b, "  %v\n", u)
		}
	}
	if deps := anySlice(out["newDependencies"]); len(deps) > 0 {
		b.WriteString("\ncross-component dependency candidates:\n")
		for _, d := range deps {
			dm, ok := d.(map[string]any)
			if !ok {
				return "", false
			}
			fmt.Fprintf(&b, "  %v -> %v  %v crossing(s)  [tier: %v]\n",
				dm["from"], dm["to"], dm["weight"], dm["minTier"])
		}
	}
	if as, _ := out["archStatus"].(string); as == "fail" || as == "review" {
		fmt.Fprintf(&b, "\narch rules touched by this diff: %s\n", as)
		for _, v := range anySlice(out["archIntroduced"]) {
			fmt.Fprintf(&b, "  %v\n", v)
		}
	}
	for _, n := range anySlice(out["notes"]) {
		fmt.Fprintf(&b, "note: %v\n", n)
	}
	switch verdict {
	case "complete":
		b.WriteString("\nno missed sites — the diff covers its own blast radius\n")
	case "review":
		b.WriteString("\nverdict: review — some contract changes could not be verified (--strict exits 1)\n")
	}
	return b.String(), true
}

// renderQuerySourceAsText renders prism_query's source delivery: the content
// is already formatted line-numbered markdown; the JSON envelope added ~17%
// (1,589 B on a 9,212 B result, measured) purely in string escaping and
// bookkeeping fields.
func renderQuerySourceAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"content": true, "deliveredTokens": true, "delivery": true,
		"files": true, "symbolCount": true, "textMatches": true,
		"textBackend": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}
	content, ok := out["content"].(string)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	if tm := anySlice(out["textMatches"]); len(tm) > 0 {
		b.WriteString("\ntext matches (outside indexed symbols):\n")
		if !renderOneSearchText(&b, map[string]any{"textHits": out["textMatches"]}) {
			return "", false
		}
	}
	return b.String(), true
}
