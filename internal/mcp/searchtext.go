package mcp

import (
	"fmt"
	"strings"
)

// renderSearchAsText renders a prism_search TEXT result as plain grep-style
// "path:line: text" lines instead of JSON.
//
// Measured 2026-08-16 (BACKLOG.md item 1): the same hits cost 1.19-1.32x the
// bytes as JSON versus plain text (+219 to +704 bytes for 8 hits across three
// real queries). Source lines are the worst possible JSON payload — every
// tab becomes \t, every quote \". prism_search is the highest-call-count
// tool measured (637 calls / 190 cells), and every result sits in the
// session cache for the rest of the run, so the envelope is paid again on
// every later turn, not once.
//
// Falls back to JSON (ok=false) whenever the result carries structured
// symbol data (scope="symbols"/"both") — flattening signature/doc/body/kind
// would lose information, not just re-encode it — or whenever the shape
// contains a field this function does not recognise. A silent drop is worse
// than a slightly larger response: better to fall back to JSON than to
// truncate a field the agent needs and never say so.
func renderSearchAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"textHits": true, "textBackend": true, "truncated": true,
		"totalHits": true, "filesMatched": true, "warning": true,
		"files": true, "fileCount": true, "rejectedPaths": true,
		"timedOut": true, "resolvedNote": true, "results": true,
		"failedTerms": true, "note": true, "query": true,
		"symbols": true,
	}
	for k := range out {
		if !known[k] {
			return "", false
		}
	}

	var b strings.Builder
	if raw, ok := out["results"]; ok {
		groups, ok := raw.([]map[string]any)
		if !ok {
			return "", false
		}
		for _, g := range groups {
			fmt.Fprintf(&b, "── %v ──\n", g["query"])
			if !renderOneSearchText(&b, g) {
				return "", false
			}
		}
	} else if !renderOneSearchText(&b, out) {
		return "", false
	}

	if note, _ := out["note"].(string); note != "" {
		fmt.Fprintf(&b, "// %s\n", note)
	}
	for _, f := range anySlice(out["failedTerms"]) {
		fmt.Fprintf(&b, "// failed: %v\n", f)
	}
	return b.String(), true
}

// renderOneSearchText renders a single search result (one term's worth) —
// either the files_only shape or the textHits shape — plus its warnings.
// Returns false on any field it does not know how to render, so the caller
// falls back to JSON rather than silently dropping content.
func renderOneSearchText(b *strings.Builder, m map[string]any) bool {
	// Symbol matches first: one location line per symbol instead of the full
	// JSON record. Measured (Kinto, v0.55.5): a default-scope search returned
	// 26-27 KB — full SymbolRecords with rawText bodies, blobSha, ids and
	// callSites — for a tool whose contract is "locate". The text form is the
	// location and the contract to go get the body, stated in-band.
	if syms := anySlice(m["symbols"]); len(syms) > 0 {
		fmt.Fprintf(b, "symbols (%d):\n", len(syms))
		for _, s := range syms {
			sm, ok := s.(map[string]any)
			if !ok {
				return false
			}
			name := sm["qualifiedName"]
			if name == nil || name == "" {
				name = sm["name"]
			}
			span, _ := sm["span"].(map[string]any)
			fmt.Fprintf(b, "  %v %v  %v", sm["kind"], name, sm["filePath"])
			if span != nil {
				fmt.Fprintf(b, ":%v-%v", span["start"], span["end"])
			}
			if sig, _ := sm["signature"].(string); sig != "" {
				fmt.Fprintf(b, "  %s", sig)
			}
			if td, _ := sm["testDouble"].(bool); td {
				b.WriteString("  [test double]")
			}
			b.WriteString("\n")
		}
		b.WriteString("// locations only — prism_lookup <name> or prism_read for the body\n")
	} else if hasKey(m, "symbols") && !hasKey(m, "textHits") && !hasKey(m, "files") {
		b.WriteString("// no symbol matches\n")
	}
	switch {
	case hasKey(m, "symbols") && !hasKey(m, "files") && !hasKey(m, "textHits"):
		// symbols-only result: already rendered above.
	case hasKey(m, "files"):
		files := anySlice(m["files"])
		for _, f := range files {
			fmt.Fprintf(b, "%v\n", f)
		}
		if len(files) == 0 {
			b.WriteString("// no matching files\n")
		}
	case hasKey(m, "textHits"):
		groups := anySlice(m["textHits"])
		if len(groups) == 0 {
			b.WriteString("// no matches\n")
		}
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				return false
			}
			if note, _ := gm["note"].(string); note != "" && gm["file"] == nil {
				fmt.Fprintf(b, "// %s\n", note)
				continue
			}
			file, _ := gm["file"].(string)
			if cached, _ := gm["cached"].(bool); cached {
				var lines []string
				for _, l := range anySlice(gm["lines"]) {
					lines = append(lines, fmt.Sprint(l))
				}
				fmt.Fprintf(b, "%s: %s [cached — content already delivered this session]\n",
					file, strings.Join(lines, ","))
				continue
			}
			for _, h := range anySlice(gm["hits"]) {
				hm, ok := h.(map[string]any)
				if !ok {
					return false
				}
				line, _ := hm["line"].(int)
				before := anySlice(hm["before"])
				for i, l := range before {
					fmt.Fprintf(b, "%s:%d-  %v\n", file, line-len(before)+i, l)
				}
				fmt.Fprintf(b, "%s:%v: %v\n", file, hm["line"], hm["text"])
				for i, l := range anySlice(hm["after"]) {
					fmt.Fprintf(b, "%s:%d-  %v\n", file, line+1+i, l)
				}
				if len(before) > 0 || hm["after"] != nil {
					b.WriteString("--\n")
				}
			}
			if more, ok := gm["moreHits"]; ok {
				fmt.Fprintf(b, "%s: +%v more matches\n", file, more)
			}
		}
	default:
		return false
	}
	if w, _ := m["warning"].(string); w != "" {
		fmt.Fprintf(b, "// %s\n", w)
	}
	if n, _ := m["resolvedNote"].(string); n != "" {
		fmt.Fprintf(b, "// %s\n", n)
	}
	if rp := anySlice(m["rejectedPaths"]); len(rp) > 0 {
		fmt.Fprintf(b, "// rejected paths: %v\n", rp)
	}
	return true
}

// hasKey reports whether m has the key at all, distinguishing "absent" from
// "present but nil" — renderTextMatches returns a nil slice for zero hits,
// and that key must still be recognised as "the textHits shape, zero hits"
// rather than falling through to files/unknown.
func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// anySlice normalises the handful of concrete slice types this package's
// tool results actually use (json.Marshal never runs before this point, so
// these are native Go slices, not []any from a JSON decode) into a uniform
// []any for iteration.
func anySlice(v any) []any {
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
	case []int:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	default:
		return nil
	}
}
