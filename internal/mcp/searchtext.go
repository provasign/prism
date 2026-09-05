package mcp

import (
	"fmt"
	"sort"
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
// Symbol-bearing results render as location lines (kind, qualified name,
// file:span, bounded signature) with an explicit prism_lookup pointer —
// see renderOneSearchText. Falls back to JSON (ok=false) only when the
// shape contains a field this function does not recognise: a silent drop
// is worse than a slightly larger response.
func renderSearchAsText(out map[string]any) (string, bool) {
	known := map[string]bool{
		"textHits": true, "textBackend": true, "truncated": true,
		"totalHits": true, "filesMatched": true, "warning": true,
		"files": true, "fileCount": true, "rejectedPaths": true,
		"timedOut": true, "resolvedNote": true, "results": true,
		"failedTerms": true, "note": true, "query": true,
		"symbols": true, "hitRollup": true, "didYouMean": true,
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
		seenNote := map[string]bool{}
		// A batched exhaustive call used to print one overflow inventory per
		// term — the same files, three or four times (measured: a 4-term
		// exhaustive on dubbo, 18.6k chars, 3/4 of it inventories of the
		// same directories). Merge them into one at the end.
		merged := map[string]int{}
		mergedTerms := 0
		// file:line lines already printed under an earlier term of this
		// batch ("http3" and "Http3" hit the same lines): shown once,
		// counted after.
		seenLines := map[string]bool{}
		for _, g := range groups {
			fmt.Fprintf(&b, "── %v ──\n", g["query"])
			// A batched query's terms often resolve to the same symbol
			// ("triple", "getTriple", "TRIPLE" → ProtocolConfig.triple);
			// the headline is worth reading once, not once per term.
			if n, _ := g["resolvedNote"].(string); n != "" {
				if seenNote[n] {
					g = withoutKey(g, "resolvedNote")
				}
				seenNote[n] = true
			}
			if len(groups) > 1 {
				if raw, rest := takeInventory(g); raw != nil {
					g = rest
					mergedTerms++
					for f, n := range raw {
						merged[f] += n
					}
				}
			}
			if !renderOneSearchText(&b, g, seenLines) {
				return "", false
			}
		}
		if len(merged) > 0 {
			files := make([]string, 0, len(merged))
			for f := range merged {
				files = append(files, f)
			}
			sort.Strings(files)
			lines, shape := boundedInventory(files, merged, inventoryLineBudget/2)
			fmt.Fprintf(&b, "// %d more files with matches across %d term(s) — exhaustive=true: %s\n", len(files), mergedTerms, shape)
			for _, l := range lines {
				b.WriteString(l + "\n")
			}
		}
	} else if !renderOneSearchText(&b, out, nil) {
		return "", false
	}

	if note, _ := out["note"].(string); note != "" {
		if strings.HasPrefix(note, "no matches — search completed") {
			// The all-empty guidance already states completion; drop the
			// per-term line that said it first.
			s := strings.ReplaceAll(b.String(), "// no matches — search completed (not truncated, not timed out)\n", "")
			b.Reset()
			b.WriteString(s)
		}
		fmt.Fprintf(&b, "// %s\n", note)
	}
	if dym := anySlice(out["didYouMean"]); len(dym) > 0 {
		b.WriteString("// closest indexed symbols:\n")
		for _, d := range dym {
			fmt.Fprintf(&b, "//   %v\n", d)
		}
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
func renderOneSearchText(b *strings.Builder, m map[string]any, seen map[string]bool) bool {
	// The graph's one-line reading of the term comes FIRST. It used to trail
	// the hit list; measured 2026-09-05 (dubbo retest transcript): the
	// field→type headline sat at byte 3399 of a 5292-byte result, after 170
	// grep lines, and the agent's next move ignored it. A headline is only a
	// headline at the top.
	if n, _ := m["resolvedNote"].(string); n != "" {
		fmt.Fprintf(b, "// %s\n", n)
	}
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
				// A locate line needs enough signature to disambiguate, not
				// a 300-char generic method header; the full form is one
				// prism_lookup away (the pointer below says so).
				if len(sig) > 100 {
					sig = sig[:97] + "..."
				}
				fmt.Fprintf(b, "  %s", sig)
			}
			if td, _ := sm["testDouble"].(bool); td {
				b.WriteString("  [test double]")
			}
			b.WriteString("\n")
		}
		b.WriteString("// locations only — prism_lookup <name> or prism_read for the body\n")
	} else if hasKey(m, "symbols") && !hasKey(m, "textHits") && !hasKey(m, "files") {
		// Same completeness rule as the text-search empty case above. Symbol
		// matching is an in-memory index lookup, not a scan with a timeout
		// risk, so the reassurance is simpler: the whole index was checked,
		// not a partial/truncated pass.
		b.WriteString("// no symbol matches (full index checked, not a partial pass)\n")
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
			// A bare "no matches" is indistinguishable from "the search
			// didn't finish" or "the index missed it" -- an agent that
			// doesn't trust the null rationally re-verifies with grep,
			// which is correct caution, not routing failure. timedOut is
			// already the engine's own completion signal (set only when
			// the deadline fired before the scan finished); surface it so
			// the null carries the same evidence a truncated hit list
			// already does.
			if timedOut, _ := m["timedOut"].(bool); timedOut {
				b.WriteString("// no matches — search timed out before finishing; results may be incomplete\n")
			} else {
				b.WriteString("// no matches — search completed (not truncated, not timed out)\n")
			}
		}
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				return false
			}
			if note, _ := gm["note"].(string); note != "" && gm["file"] == nil {
				fmt.Fprintf(b, "// %s\n", note)
				// exhaustive=true inventory (renderTextMatches): the files
				// past the cap, one per line. Measured 2026-09-05 (dubbo
				// retest): the note promised "every one is listed here" and
				// this renderer dropped the list — the agent got the promise
				// and nothing else.
				for _, f := range anySlice(gm["files"]) {
					fmt.Fprintf(b, "%v\n", f)
				}
				continue
			}
			file, _ := gm["file"].(string)
			if cached, _ := gm["cached"].(bool); cached {
				// New shape: matched lines with text (never elided), context
				// omitted. Legacy shape (lines-only ints) still renders for
				// any old payload in flight.
				if hits := anySlice(gm["hits"]); len(hits) > 0 {
					for _, hh := range hits {
						hm, ok := hh.(map[string]any)
						if !ok {
							return false
						}
						fmt.Fprintf(b, "%s:%v: %v\n", file, hm["line"], hm["text"])
					}
					fmt.Fprintf(b, "%s: [file body cached — content already delivered this session]\n", file)
					continue
				}
				var lines []string
				for _, l := range anySlice(gm["lines"]) {
					lines = append(lines, fmt.Sprint(l))
				}
				fmt.Fprintf(b, "%s: %s [cached — content already delivered this session]\n",
					file, strings.Join(lines, ","))
				continue
			}
			dup := 0
			for _, h := range anySlice(gm["hits"]) {
				hm, ok := h.(map[string]any)
				if !ok {
					return false
				}
				line, _ := hm["line"].(int)
				if seen != nil {
					key := fmt.Sprintf("%s:%d", file, line)
					if seen[key] {
						dup++
						continue
					}
					seen[key] = true
				}
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
			if dup > 0 {
				fmt.Fprintf(b, "%s: %d line(s) already shown under an earlier term\n", file, dup)
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
	if ru := anySlice(m["hitRollup"]); len(ru) > 0 {
		b.WriteString("// ALL matches by enclosing symbol (graph rollup of the full set):\n")
		for _, e := range ru {
			em, ok := e.(map[string]any)
			if !ok {
				return false
			}
			if note, _ := em["note"].(string); note != "" {
				fmt.Fprintf(b, "//   %s\n", note)
				continue
			}
			span, _ := em["span"].(map[string]any)
			fmt.Fprintf(b, "//   %v  %v", em["symbol"], em["file"])
			if span != nil {
				fmt.Fprintf(b, ":%v-%v", span["start"], span["end"])
			}
			fmt.Fprintf(b, "  (%v hits)\n", em["hits"])
		}
	}
	if rp := anySlice(m["rejectedPaths"]); len(rp) > 0 {
		fmt.Fprintf(b, "// rejected paths: %v\n", rp)
	}
	return true
}

// takeInventory splits a term's overflow inventory (the textHits entry
// carrying rawFiles) out of the group: returns the raw file→hits map and a
// copy of the group without that entry, or nil when there is none.
func takeInventory(g map[string]any) (map[string]int, map[string]any) {
	hits := anySlice(g["textHits"])
	for i, h := range hits {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := hm["rawFiles"].(map[string]int)
		if !ok {
			continue
		}
		rest := make([]any, 0, len(hits)-1)
		rest = append(rest, hits[:i]...)
		rest = append(rest, hits[i+1:]...)
		out := make(map[string]any, len(g))
		for k, v := range g {
			out[k] = v
		}
		out["textHits"] = rest
		return raw, out
	}
	return nil, g
}

// withoutKey returns a shallow copy of m with one key removed.
func withoutKey(m map[string]any, k string) map[string]any {
	out := make(map[string]any, len(m))
	for kk, v := range m {
		if kk != k {
			out[kk] = v
		}
	}
	return out
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
