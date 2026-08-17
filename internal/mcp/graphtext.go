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
		"widerAnchor": true,
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
