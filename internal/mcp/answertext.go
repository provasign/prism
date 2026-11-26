package mcp

// The text renderer for tool answers the specific renderers in
// RenderToolText do not cover. It was the CLI's own (internal/cli printOutput);
// it lives here so every text form of an answer is in one package (ENG-04).

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RenderAnswerText renders any answer as text: the shape-specific forms
// below, else indented key/value lines. Never JSON (CON-02).
func RenderAnswerText(m map[string]any) string {
	var b strings.Builder
	writeTextOutput(&b, m)
	return b.String()
}

// GenericText renders v as indented "key: value" lines.
func GenericText(v any) string {
	var b strings.Builder
	writeGeneric(&b, "", v)
	return b.String()
}

// printOutput prints v in the requested format.
// JSON round-trips through map[string]any so both typed structs (queryResult)
// and plain maps are handled uniformly by the text/lean formatters.
// printTextMatches renders the merged full-text section of a prism_query /
// prism_search response: per-file matched lines, cached files as line
// numbers only.
func writeTextMatches(w io.Writer, m map[string]any) {
	groups := asSliceAny(m["textMatches"])
	if groups == nil {
		groups = asSliceAny(m["textHits"])
	}
	if len(groups) == 0 {
		return
	}
	backend, _ := m["textBackend"].(string)
	fmt.Fprintf(w, "// text matches (%s):\n", backend)
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if note, _ := gm["note"].(string); note != "" && gm["file"] == nil {
			fmt.Fprintf(w, "//   %s\n", note)
			// exhaustive=true inventory: the files past the render cap
			// (same as the MCP renderer in searchtext.go).
			for _, f := range asSliceAny(gm["files"]) {
				fmt.Fprintf(w, "//   %v\n", f)
			}
			continue
		}
		file, _ := gm["file"].(string)
		if cached, _ := gm["cached"].(bool); cached {
			var lines []string
			for _, l := range asSliceAny(gm["lines"]) {
				lines = append(lines, fmt.Sprint(jsonInt(l)))
			}
			fmt.Fprintf(w, "//   %s:%s [cached — content already delivered this session]\n",
				file, strings.Join(lines, ","))
			continue
		}
		for _, h := range asSliceAny(gm["hits"]) {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			line := jsonInt(hm["line"])
			before := asSliceAny(hm["before"])
			for i, l := range before {
				fmt.Fprintf(w, "//   %s:%d-  %v\n", file, line-len(before)+i, l)
			}
			fmt.Fprintf(w, "//   %s:%d: %v\n", file, line, hm["text"])
			for i, l := range asSliceAny(hm["after"]) {
				fmt.Fprintf(w, "//   %s:%d-  %v\n", file, line+1+i, l)
			}
			if len(before) > 0 || hm["after"] != nil {
				fmt.Fprintln(w, "//   --")
			}
		}
		if more := jsonInt(gm["moreHits"]); more > 0 {
			fmt.Fprintf(w, "//   %s: +%d more matches\n", file, more)
		}
	}
}

// printTextOutput renders a Prism response as plain text for agent consumption.
// Handles prism_query, prism_read, prism_search, and prism_lookup responses.
func writeTextOutput(w io.Writer, m map[string]any) {
	if root, _ := m["root"].(string); root != "" {
		if _, search := m["results"]; search || m["textHits"] != nil || m["symbols"] != nil || m["files"] != nil {
			fmt.Fprintf(w, "// root: %s\n", root)
		}
	}
	// Multi-term prism_search: one group per term, each rendered by the
	// single-term path below so the two forms read identically.
	if groups, ok := m["results"]; ok {
		if note, _ := m["note"].(string); note != "" {
			fmt.Fprintln(w, "// "+note)
		}
		writeDidYouMean(w, m)
		for _, g := range asSliceAny(groups) {
			gm, ok := g.(map[string]any)
			if !ok {
				continue
			}
			fmt.Fprintf(w, "// ── %v ──\n", gm["query"])
			writeTextOutput(w, gm)
		}
		for _, f := range asSliceAny(m["failedTerms"]) {
			fmt.Fprintf(w, "// failed: %v\n", f)
		}
		return
	}
	// A pure text search (scope="text") has textHits and nothing else. Without
	// this branch it fell past every case below to the JSON fallback, so
	// `prism search X --scope text --format text` — the exact invocation the
	// steering gives Bash-only subagents — printed JSON, several times the
	// tokens of the line-oriented form it asked for.
	// prism_query source delivery: the assembled context IS the answer.
	// Measured 2026-08-26 (jackson worktree): the MCP surface returned the
	// full 21KB context while this CLI path printed FIVE FILE PATHS — the
	// files_only branch below fired because the query payload has "files"
	// and no "symbols" key, and the entire "content" field (5,201 delivered
	// tokens, anchors, callers) was silently discarded. Every Bash-only
	// consumer (subagents, CI — exactly who the CLAUDE.md bash table sends
	// here) got paths where the tool's whole purpose is context.
	// prism_read also carries "content" but never "files"; its own branch
	// below prints the header line — the guard keeps it out of this one.
	if content, ok := m["content"].(string); ok && content != "" {
		if _, isQuery := m["files"]; isQuery {
			fmt.Fprint(w, content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Fprintln(w)
			}
			writeTextMatches(w, m)
			return
		}
	}
	// files_only delivery: paths, no lines.
	if files, ok := m["files"]; ok {
		if _, hasSyms := m["symbols"]; !hasSyms {
			for _, f := range asSliceAny(files) {
				fmt.Fprintf(w, "%v\n", f)
			}
			if len(asSliceAny(files)) == 0 {
				fmt.Fprintln(w, "// no matching files")
			}
			for _, k := range []string{"warning", "note"} {
				if s, _ := m[k].(string); s != "" {
					fmt.Fprintln(w, "// "+s)
				}
			}
			return
		}
	}
	if _, hasHits := m["textHits"]; hasHits {
		_, hasSyms := m["symbols"]
		_, hasContent := m["content"]
		if !hasSyms && !hasContent {
			// Headline first — the graph's reading of the term leads, the
			// grep lines follow (searchtext.go has the measurement).
			if s, _ := m["resolvedNote"].(string); s != "" {
				fmt.Fprintln(w, "// "+s)
			}
			writeTextMatches(w, m)
			if len(asSliceAny(m["textHits"])) == 0 {
				// Same evidence rule as the MCP renderer (searchtext.go):
				// a bare null is indistinguishable from a broken/partial
				// search, so state completion explicitly.
				if timedOut, _ := m["timedOut"].(bool); timedOut {
					fmt.Fprintln(w, "// no matches — search timed out before finishing; results may be incomplete")
				} else {
					fmt.Fprintln(w, "// no matches — search completed (not truncated, not timed out)")
				}
			}
			for _, k := range []string{"warning", "note"} {
				if s, _ := m[k].(string); s != "" {
					fmt.Fprintln(w, "// "+s)
				}
			}
			writeDidYouMean(w, m)
			// Graph rollup of a truncated search's FULL hit set (rollup.go) —
			// same rendering as the MCP text surface.
			if ru, _ := m["hitRollup"].([]any); len(ru) > 0 {
				fmt.Fprintln(w, "// ALL matches by enclosing symbol (graph rollup of the full set):")
				for _, e := range ru {
					em, _ := e.(map[string]any)
					if em == nil {
						continue
					}
					if note, _ := em["note"].(string); note != "" {
						fmt.Fprintln(w, "//   "+note)
						continue
					}
					span, _ := em["span"].(map[string]any)
					line := fmt.Sprintf("//   %v  %v", em["symbol"], em["file"])
					if span != nil {
						line += fmt.Sprintf(":%v-%v", span["start"], span["end"])
					}
					fmt.Fprintf(w, "%s  (%v hits)\n", line, em["hits"])
				}
			}
			// The truncation warning is already carried in m["warning"] and
			// printed above; printing a second line here duplicated it.
			if t, _ := m["truncated"].(bool); t && m["warning"] == nil {
				fmt.Fprintln(w, "// truncated at the hit limit — raise --limit, or --exhaustive")
			}
			return
		}
	}
	// prism_node: source PLUS the orientation payload. Must come first — its
	// shape overlaps prism_lookup's (symbol+content) and prism_read's
	// (file+content), so without this branch both node views fell through and
	// the edges / defines / dependents were silently discarded.
	if view, ok := m["view"].(string); ok && (view == "symbol" || view == "file") {
		writeNodeText(w, m, view)
		return
	}
	// Unmatched lookup/node: a bare {"symbol": null} rendered as JSON told
	// the caller nothing. Print the note and the "did you mean" list.
	if matched, present := m["matched"].(bool); present && !matched {
		if _, hasContent := m["content"]; !hasContent {
			if note, _ := m["note"].(string); note != "" {
				fmt.Fprintln(w, "// "+note)
			} else {
				fmt.Fprintf(w, "// no match for %v\n", m["name"])
			}
			for _, c := range asSliceAny(m["candidates"]) {
				fmt.Fprintf(w, "  %v\n", c)
			}
			return
		}
	}
	// prism_lookup: top-level "content" + "symbol" subkey
	if sym, hasSym := m["symbol"].(map[string]any); hasSym && sym != nil {
		if content, ok := m["content"].(string); ok {
			writeLookupFlagsText(w, m)
			name, _ := sym["name"].(string)
			fp, _ := sym["filePath"].(string)
			fmt.Fprintf(w, "// %s — %s\n", fp, name)
			span, _ := sym["span"].(map[string]any)
			start, end := jsonInt(span["start"]), jsonInt(span["end"])
			lines := strings.SplitAfter(content, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			if start > 0 && end >= start && len(lines) <= end-start+1 {
				for i, line := range lines {
					fmt.Fprintf(w, "%d"+SourceLineSep+"%s", start+i, line)
				}
			} else {
				fmt.Fprint(w, content)
			}
			if !strings.HasSuffix(content, "\n") {
				fmt.Fprintln(w)
			}
			writeLookupOverloadsText(w, m)
			return
		}
	}
	// prism_read: top-level "content" + "file" key
	if content, ok := m["content"].(string); ok {
		file, _ := m["file"].(string)
		strategy, _ := m["strategy"].(string)
		if strategy == "sha-pointer" {
			fmt.Fprintf(w, "// %s [cached — use previous read]\n", file)
		} else {
			if file != "" {
				fmt.Fprintf(w, "// %s\n", file)
			}
			fmt.Fprint(w, content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Fprintln(w)
			}
		}
		return
	}
	// prism_query and prism_search: "symbols" array
	if rawSyms, ok := m["symbols"]; ok {
		syms, _ := rawSyms.([]any)
		for _, s := range syms {
			sym, ok := s.(map[string]any)
			if !ok {
				continue
			}
			fp, _ := sym["filePath"].(string)
			name, _ := sym["name"].(string)
			category, _ := sym["category"].(string)
			content, _ := sym["content"].(string)
			if content == "" {
				content, _ = sym["rawText"].(string)
			}
			if fp != "" && name != "" {
				if category != "" {
					fmt.Fprintf(w, "// %s — %s [%s]\n", fp, name, category)
				} else {
					fmt.Fprintf(w, "// %s — %s\n", fp, name)
				}
			}
			if content != "" {
				fmt.Fprint(w, content)
				if !strings.HasSuffix(content, "\n") {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w)
			}
		}
		// Merged full-text hits (prism_query: "textMatches"; prism_search:
		// "textHits") — matches outside any indexed symbol.
		writeTextMatches(w, m)
		if note, _ := m["note"].(string); note != "" {
			fmt.Fprintln(w, "// "+note)
		}
		return
	}
	// prism_lookup with --fields: projected columns (name/file/line + selected),
	// no "content"/"symbol". Render the requested columns compactly.
	if _, hasContent := m["content"]; !hasContent {
		if _, hasSymbols := m["symbols"]; !hasSymbols {
			if _, hasCands := m["candidates"]; !hasCands {
				if file, ok := m["file"].(string); ok {
					if _, hasName := m["name"]; hasName {
						fmt.Fprintf(w, "// %v — %s:%d\n", m["name"], file, jsonInt(m["line"]))
						for _, col := range []string{"kind", "signature", "doc", "modifiers", "parent", "body"} {
							if v, ok := m[col]; ok {
								fmt.Fprintf(w, "%s: %v\n", col, v)
							}
						}
						return
					}
				}
			}
		}
	}
	// prism_resolve: "candidates" list of {name, kind, file, line, testDouble}
	if rawCands, ok := m["candidates"].([]any); ok {
		name, _ := m["name"].(string)
		fmt.Fprintf(w, "// %s — %d candidate(s)\n", name, len(rawCands))
		for _, c := range rawCands {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			tag := wireTestTag(cm)
			name := cm["name"]
			if p, ok := cm["pasteable"].(string); ok && p != "" {
				name = p // GAP-01: a name that resolves to this candidate alone
			}
			fmt.Fprintf(w, "  %v  %v  %v:%v%s\n", name, cm["kind"], cm["file"], jsonInt(cm["line"]), tag)
		}
		return
	}
	// prism_edges: "edges" map of "<kind> <direction>" -> {shown, total, symbols}
	if rawEdges, ok := m["edges"].(map[string]any); ok {
		name, _ := m["name"].(string)
		fmt.Fprintf(w, "// %s — graph edges\n", name)
		rels := make([]string, 0, len(rawEdges))
		for r := range rawEdges {
			rels = append(rels, r)
		}
		sort.Strings(rels)
		for _, r := range rels {
			g, _ := rawEdges[r].(map[string]any)
			shown, total := jsonInt(g["shown"]), jsonInt(g["total"])
			cap := ""
			if total > shown {
				cap = fmt.Sprintf(" (showing %d of %d)", shown, total)
			}
			fmt.Fprintf(w, "%s%s:\n", r, cap)
			syms, _ := g["symbols"].([]any)
			for _, s := range syms {
				sm, ok := s.(map[string]any)
				if !ok {
					continue
				}
				tag := wireTestTag(sm)
				fmt.Fprintf(w, "  %v  %v:%v%s\n", sm["name"], sm["file"], jsonInt(sm["line"]), tag)
			}
		}
		return
	}
	// The four task-shaped ops. Their responses have no "content"/"symbols"
	// key, so they used to fall straight through to printJSON — meaning
	// `--format text` was a documented no-op on exactly the commands the
	// Bash-only playbook tells agents to run that way.
	if _, ok := m["declarations"]; ok {
		writeChangeImpactText(w, m)
		return
	}
	if _, ok := m["edits"]; ok || (m["status"] == "collision" && m["newName"] != nil) {
		writeRenamePlanText(w, m)
		return
	}
	if _, ok := m["missing"]; ok {
		writeMissingImplText(w, m)
		return
	}
	if _, ok := m["dead"]; ok {
		writeDeadCodeText(w, m)
		return
	}
	// prism_references: "sites" rows path:line:col: text.
	if sites, ok := m["sites"].([]any); ok && m["name"] != nil {
		name, _ := m["name"].(string)
		switch {
		case m["resolved"] == true:
			fmt.Fprintf(w, "// %s — %d reference site(s), resolved to this symbol\n", name, jsonInt(m["count"]))
			writeNotes(w, m, "completeness", "completenessScope", "callerEvidence", "degradedAnalysis")
		case m["ambiguous"] == true:
			fmt.Fprintf(w, "// %s — %d use(s) of the name (ambiguous — %d definitions share it)\n", name, jsonInt(m["count"]), jsonInt(m["definitions"]))
		default:
			fmt.Fprintf(w, "// %s — %d use(s) of the name (one definition)\n", name, jsonInt(m["count"]))
		}
		writeReferenceRows(w, sites)
		for _, l := range anySliceCLI(m["otherMentions"]) {
			fmt.Fprintf(w, "%v\n", l)
		}
		writeNotes(w, m, "note", "warning")
		return
	}
	// prism_references: "byFile" map of file -> [{line, in}]
	if rawByFile, ok := m["byFile"].(map[string]any); ok {
		name, _ := m["name"].(string)
		count := jsonInt(m["count"])
		defs := jsonInt(m["definitions"])
		ambiguous, _ := m["ambiguous"].(bool)
		tier := "unambiguous"
		if ambiguous {
			tier = fmt.Sprintf("ambiguous — %d definitions share this name", defs)
		}
		fmt.Fprintf(w, "// %s — %d references (%s)\n", name, count, tier)
		files := make([]string, 0, len(rawByFile))
		for f := range rawByFile {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			refs, _ := rawByFile[f].([]any)
			fmt.Fprintf(w, "%s\n", f)
			for _, r := range refs {
				ref, ok := r.(map[string]any)
				if !ok {
					continue
				}
				line := jsonInt(ref["line"])
				if in, ok := ref["in"].(string); ok && in != "" {
					fmt.Fprintf(w, "  %d  in %s\n", line, in)
				} else {
					fmt.Fprintf(w, "  %d\n", line)
				}
			}
		}
		return
	}
	// Fallback: plain key/value text, never JSON. --format text used to
	// print raw JSON for the ambiguity choice, a bad regex, a rollup and
	// a symbols delivery (CON-02).
	fmt.Fprint(w, genericText(m))
}

// genericText renders any answer as indented "key: value" lines: scalars
// inline, lists one item per line, nested objects indented. Keys sort,
// except that query/name/status/error/note lead.
func genericText(m map[string]any) string {
	var b strings.Builder
	writeGeneric(&b, "", m)
	return b.String()
}

func writeGeneric(b *strings.Builder, indent string, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			li, iok := genericLead[keys[i]]
			lj, jok := genericLead[keys[j]]
			switch {
			case iok && jok:
				return li < lj
			case iok != jok:
				return iok
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			val := x[k]
			if genericScalar(val) {
				fmt.Fprintf(b, "%s%s: %s\n", indent, k, genericValue(val))
				continue
			}
			if list, ok := val.([]any); ok {
				if len(list) == 0 {
					continue
				}
				fmt.Fprintf(b, "%s%s (%d):\n", indent, k, len(list))
			} else {
				fmt.Fprintf(b, "%s%s:\n", indent, k)
			}
			writeGeneric(b, indent+"  ", val)
		}
	case []any:
		for _, item := range x {
			if genericScalar(item) {
				fmt.Fprintf(b, "%s- %s\n", indent, genericValue(item))
				continue
			}
			if im, ok := item.(map[string]any); ok && genericFlat(im) {
				fmt.Fprintf(b, "%s- %s\n", indent, genericInline(im))
				continue
			}
			fmt.Fprintf(b, "%s-\n", indent)
			writeGeneric(b, indent+"  ", item)
		}
	default:
		fmt.Fprintf(b, "%s%s\n", indent, genericValue(x))
	}
}

func genericScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// genericFlat: an object of at most six scalar fields prints on one line.
func genericFlat(m map[string]any) bool {
	if len(m) > 6 {
		return false
	}
	for _, v := range m {
		if !genericScalar(v) {
			return false
		}
	}
	return true
}

func genericInline(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+genericValue(m[k]))
	}
	return strings.Join(parts, ", ")
}

func genericValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// siteLine renders one change-set entry as "qualifiedName  file:line".
func siteLine(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := m["qualifiedName"].(string)
	if name == "" {
		name, _ = m["name"].(string)
	}
	line := fmt.Sprintf("  %s  %v:%d", name, m["filePath"], jsonInt(m["line"]))
	if via, _ := m["via"].(string); via != "" {
		line += "  (via " + via + ")"
	}
	return line
}

// printSiteGroup prints a labelled group, skipping empty ones.
func writeSiteGroup(w io.Writer, label string, v any) {
	items, _ := v.([]any)
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "%s (%d):\n", label, len(items))
	for _, it := range items {
		if l := siteLine(it); l != "" {
			fmt.Fprintln(w, l)
		}
	}
}

// printNotes emits the advisory keys (completeness, warnings, notes) that
// carry the caveats a caller must not silently drop.
// printDidYouMean renders an empty search's near-miss symbol candidates —
// the retry pointer attachEmptySearchGuidance's note refers to.
func writeDidYouMean(w io.Writer, m map[string]any) {
	dym := asSliceAny(m["didYouMean"])
	if len(dym) == 0 {
		return
	}
	fmt.Fprintln(w, "// closest indexed symbols:")
	for _, d := range dym {
		fmt.Fprintf(w, "//   %v\n", d)
	}
}

func writeNotes(w io.Writer, m map[string]any, keys ...string) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				fmt.Fprintf(w, "%s: %s\n", k, v)
			}
		case bool:
			if v || k == "safeToClaimComplete" {
				fmt.Fprintf(w, "%s: %t\n", k, v)
			}
		case []any:
			if len(v) > 0 {
				parts := make([]string, 0, len(v))
				for _, e := range v {
					parts = append(parts, fmt.Sprint(e))
				}
				fmt.Fprintf(w, "%s: %s\n", k, strings.Join(parts, ", "))
			}
		}
	}
}

func writeChangeImpactText(w io.Writer, m map[string]any) {
	if text, ok := FormatMemberImpactText(m); ok {
		fmt.Fprint(w, text)
		return
	}
	fmt.Fprintf(w, "// %v — change-impact: %d site(s)\n", m["query"], jsonInt(m["totalSites"]))
	if note, _ := m["degradedAnalysis"].(string); note != "" {
		fmt.Fprintf(w, "// %s\n", note) // qualifies every site below
	}
	fmt.Fprint(w, FormatImpactHeaderNotesText(m))
	writeNotes(w, m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "familyCompleteness", "callerCoverage", "callerEvidence", "coverageNote", "evidenceNote", "hasHeuristicRefs")
	fmt.Fprint(w, FormatImpactRelaySitesText(m))
	fmt.Fprint(w, FormatImpactExtrasText(m))
	writeSiteGroup(w, "declarations", m["declarations"])
	writeSiteGroup(w, "supers", m["supers"])
	writeSiteGroup(w, "family", m["family"])
	writeSiteGroup(w, "declaringTypes", m["declaringTypes"])
	writeSiteGroup(w, "callers", m["callers"])
	writeNotes(w, m, "declaringTypesNote", "externalSupers", "overridesExternal", "warning", "ambiguityNote", "scopeNote")
	if hint, ok := m["widerAnchor"].(map[string]any); ok {
		fmt.Fprintf(w, "widerAnchor: %v\n", hint["note"])
	}
}

// printReferenceRows prints path:line:col: text rows with each path once.
// Past 15 rows only line:col positions are printed per file, so a wide
// answer stays no larger than the old line-number list.
func writeReferenceRows(w io.Writer, sites []any) {
	type group struct {
		path string
		rows []string
	}
	var groups []*group
	byPath := map[string]*group{}
	for _, r := range sites {
		row := fmt.Sprint(r)
		path, rest := row, ""
		if m := referenceRow.FindStringSubmatch(row); m != nil {
			path, rest = m[1], m[2]
		}
		g := byPath[path]
		if g == nil {
			g = &group{path: path}
			byPath[path] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, rest)
	}
	compact := len(sites) > 15
	for _, g := range groups {
		if !compact {
			if len(g.rows) == 1 {
				fmt.Fprintf(w, "  %s:%s\n", g.path, g.rows[0])
				continue
			}
			fmt.Fprintf(w, "  %s:\n", g.path)
			for _, r := range g.rows {
				fmt.Fprintf(w, "    %s\n", r)
			}
			continue
		}
		pos := make([]string, 0, len(g.rows))
		for _, r := range g.rows {
			if i := strings.Index(r, ": "); i >= 0 {
				r = r[:i]
			}
			pos = append(pos, r)
		}
		fmt.Fprintf(w, "  %s (%d): %s\n", g.path, len(pos), strings.Join(pos, " "))
	}
	if compact {
		fmt.Fprintln(w, "  (line:col positions; --format json has each line's text)")
	}
}

func anySliceCLI(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}

// wireTestTag renders a symbol's testLabel ("[test]" or "[test double]");
// a record with only testDouble predates testLabel.
func wireTestTag(m map[string]any) string {
	if l, _ := m["testLabel"].(string); l != "" {
		return "  [" + l + "]"
	}
	if td, _ := m["testDouble"].(bool); td {
		return "  [test double]"
	}
	return ""
}

func writeRenamePlanText(w io.Writer, m map[string]any) {
	if cols, _ := m["collisions"].([]any); len(cols) > 0 {
		fmt.Fprintf(w, "// %v → %v — rename-plan: COLLISION, %v is already declared where the renamed member would land (%d):\n", m["query"], m["newName"], m["newName"], len(cols))
		for _, c := range cols {
			if cm, ok := c.(map[string]any); ok {
				fmt.Fprintf(w, "  %v  %v:%d  (renaming %v)\n", cm["name"], cm["file"], jsonInt(cm["line"]), cm["renamedMember"])
			}
		}
		if _, hasEdits := m["edits"]; !hasEdits {
			writeNotes(w, m, "note")
			return
		}
	}
	edits, _ := m["edits"].([]any)
	amb, _ := m["ambiguous"].([]any)
	unres, _ := m["unresolved"].([]any)
	fmt.Fprintf(w, "// %v → %v — rename-plan: %d site(s): %d edit(s), %d ambiguous, %d unresolved\n",
		m["query"], m["newName"], len(edits)+len(amb)+len(unres), len(edits), len(amb), len(unres))
	writeNotes(w, m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "degradedAnalysis")
	writeEditGroup(w, "edits", m["edits"])
	writeEditGroup(w, "ambiguous", m["ambiguous"])
	writeSiteGroup(w, "unresolved", m["unresolved"])
	writeNotes(w, m, "ambiguousNote", "unresolvedNote", "externalSupers", "overridesExternal", "warning")
}

// printEditGroup renders rename edits as file:line with the before/after
// pair, which is what makes the plan reviewable without re-reading the JSON.
func writeEditGroup(w io.Writer, label string, v any) {
	items, _ := v.([]any)
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "%s (%d):\n", label, len(items))
	for _, it := range items {
		e, ok := it.(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "  %v:%d\n", e["filePath"], jsonInt(e["line"]))
		if before, ok := e["before"].(string); ok {
			fmt.Fprintf(w, "    - %s\n", before)
		}
		if after, ok := e["after"].(string); ok {
			fmt.Fprintf(w, "    + %s\n", after)
		}
		if reason, ok := e["reason"].(string); ok && reason != "" {
			fmt.Fprintf(w, "    // %s\n", reason)
		}
	}
}

func writeMissingImplText(w io.Writer, m map[string]any) {
	fmt.Fprintf(w, "// %v — missing-implementations (%d type(s) already implement)\n",
		m["query"], jsonInt(m["implementedCount"]))
	writeSiteGroup(w, "contract", m["contract"])
	writeSiteGroup(w, "missing", m["missing"])
	writeSiteGroup(w, "abstractMissing", m["abstractMissing"])
	writeSiteGroup(w, "unverifiable", m["unverifiable"])
	writeSiteGroup(w, "alreadyHave", m["alreadyHave"])
	writeSiteGroup(w, "formerlyImplementing", m["formerlyImplementing"])
	writeNotes(w, m, "completeness", "completenessScope", "safeToClaimComplete", "scopeBoundary", "unverifiableNote", "formerlyImplementingNote", "defaultProvided", "note")
}

func writeDeadCodeText(w io.Writer, m map[string]any) {
	fmt.Fprintf(w, "// dead-code — %d considered, %d reachable from %d root(s)\n",
		jsonInt(m["considered"]), jsonInt(m["reachableCount"]), jsonInt(m["rootCount"]))
	writeSiteGroup(w, "dead", m["dead"])
	writeSiteGroup(w, "exportedUnreferenced", m["exportedUnreferenced"])
	writeNotes(w, m, "caveats")
}

// jsonInt coerces a JSON number (float64 after round-trip) or int to int.
// printLookupOverloadsText prints what lookup delivers beside the primary body:
// same-file overloads (body, or signature when the budget was spent) and
// cross-symbol ambiguity candidates. Text mode used to stop after the primary
// body, so a CLI caller never learned other overloads or candidates existed.
func writeLookupOverloadsText(w io.Writer, m map[string]any) {
	if overloads := asSliceAny(m["overloads"]); len(overloads) > 0 {
		fmt.Fprintf(w, "// %d more overload(s) with the same name in this file:\n", len(overloads))
		for _, raw := range overloads {
			o, _ := raw.(map[string]any)
			if o == nil {
				continue
			}
			start, end := jsonInt(o["line"]), jsonInt(o["end"])
			fmt.Fprintf(w, "// overload %v  %v:%d-%d\n", o["name"], o["file"], start, end)
			body, _ := o["content"].(string)
			if body == "" {
				body, _ = o["body"].(string)
			}
			if body == "" {
				if sig, _ := o["signature"].(string); sig != "" {
					fmt.Fprintf(w, "// signature: %s\n", sig)
				}
				if omitted, _ := o["bodyOmitted"].(bool); omitted {
					fmt.Fprintf(w, "// body not delivered (lookup body budget); read %v lines %d-%d\n", o["file"], start, end)
				}
				continue
			}
			lines := strings.SplitAfter(body, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			if start > 0 && end >= start+len(lines)-1 {
				for i, line := range lines {
					fmt.Fprintf(w, "%d"+SourceLineSep+"%s", start+i, line)
				}
			} else {
				fmt.Fprint(w, body)
			}
			if !strings.HasSuffix(body, "\n") {
				fmt.Fprintln(w)
			}
		}
	}
	for _, d := range asSliceAny(m["declarations"]) {
		fmt.Fprintf(w, "// declared: %v\n", d)
	}
	if amb, _ := m["ambiguous"].(bool); amb {
		fmt.Fprintln(w, "// AMBIGUOUS — same score for:")
		for _, c := range asSliceAny(m["candidates"]) {
			fmt.Fprintf(w, "//   %v\n", c)
		}
	}
}

// printLookupFlagsText prints a lookup's NO EXACT MATCH / AMBIGUOUS /
// inherited / case-insensitive flags BEFORE the body. This path used to
// ignore matched:false entirely, so the Bash fallback route showed a wrong
// body exactly as if it were the answer.
func writeLookupFlagsText(w io.Writer, m map[string]any) {
	note, _ := m["note"].(string)
	if matched, present := m["matched"].(bool); present && !matched {
		switch {
		case strings.HasPrefix(note, "NO EXACT MATCH"):
			fmt.Fprintln(w, "// "+note)
		case note != "":
			fmt.Fprintln(w, "// NO EXACT MATCH — "+note)
		default:
			fmt.Fprintf(w, "// NO EXACT MATCH for %v — the symbol below is only the closest hit\n", m["name"])
		}
		if cands := asSliceAny(m["candidates"]); len(cands) > 0 {
			fmt.Fprintln(w, "// candidates:")
			for _, c := range cands {
				fmt.Fprintf(w, "//   %v\n", c)
			}
		}
		fmt.Fprintln(w, "// closest symbol shown below; it does NOT exactly match the requested name")
		return
	}
	if amb, _ := m["ambiguous"].(bool); amb {
		fmt.Fprintln(w, "// AMBIGUOUS — several symbols fit equally; the first is shown, all are listed at the end")
	}
	if note != "" {
		fmt.Fprintln(w, "// "+note)
	}
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// printNodeText renders `prism node` in text form: the source, then the
// orientation payload the JSON carries. Without this the node views matched
// prism_lookup's and prism_read's branches and their whole point — neighbours
// for a symbol, defines/dependents for a file — was dropped.
func writeNodeText(w io.Writer, m map[string]any, view string) {
	if note, _ := m["note"].(string); note != "" {
		fmt.Fprintf(w, "// %s\n", note)
	}
	if cands := asSliceAny(m["candidates"]); len(cands) > 0 {
		fmt.Fprintln(w, "// ambiguous — candidates:")
		for _, c := range cands {
			fmt.Fprintf(w, "//   %v\n", c)
		}
		return
	}

	if view == "symbol" {
		if sym, ok := m["symbol"].(map[string]any); ok && sym != nil {
			name, _ := sym["name"].(string)
			fp, _ := sym["filePath"].(string)
			fmt.Fprintf(w, "// %s — %s\n", fp, name)
		}
		if content, ok := m["content"].(string); ok && content != "" {
			fmt.Fprint(w, content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Fprintln(w)
			}
		}
		writeNodeEdges(w, m)
		return
	}

	// File view.
	file, _ := m["file"].(string)
	if strategy, _ := m["strategy"].(string); strategy == "sha-pointer" {
		fmt.Fprintf(w, "// %s [cached — use previous read]\n", file)
	} else {
		if file != "" {
			fmt.Fprintf(w, "// %s\n", file)
		}
		if content, ok := m["content"].(string); ok && content != "" {
			fmt.Fprint(w, content)
			if !strings.HasSuffix(content, "\n") {
				fmt.Fprintln(w)
			}
		}
	}
	if defs := asSliceAny(m["defines"]); len(defs) > 0 {
		fmt.Fprintf(w, "\n// defines (%d):\n", len(defs))
		for _, d := range defs {
			dm, _ := d.(map[string]any)
			if dm == nil {
				continue
			}
			fmt.Fprintf(w, "//   %v  (%v:%v)\n", dm["name"], file, dm["line"])
		}
	}
	deps := asSliceAny(m["dependents"])
	fmt.Fprintf(w, "\n// dependents (%d):\n", len(deps))
	if len(deps) == 0 {
		fmt.Fprintln(w, "//   (none — no indexed file references this one)")
	}
	for _, d := range deps {
		fmt.Fprintf(w, "//   %v\n", d)
	}
}

// printNodeEdges renders the neighbour menu of a symbol node view.
func writeNodeEdges(w io.Writer, m map[string]any) {
	edges, _ := m["edges"].(map[string]any)
	if len(edges) == 0 {
		fmt.Fprintln(w, "\n// neighbours: (none — a leaf in the current graph)")
		return
	}
	groups := make([]string, 0, len(edges))
	for k := range edges {
		groups = append(groups, k)
	}
	sort.Strings(groups)
	fmt.Fprintln(w, "\n// neighbours:")
	for _, g := range groups {
		gm, _ := edges[g].(map[string]any)
		if gm == nil {
			continue
		}
		syms := asSliceAny(gm["symbols"])
		names := make([]string, 0, len(syms))
		for _, s := range syms {
			sm, _ := s.(map[string]any)
			if sm == nil {
				continue
			}
			names = append(names, fmt.Sprintf("%v", sm["name"]))
		}
		total := gm["total"]
		shown := len(names)
		const cap = 12
		if shown > cap {
			names = names[:cap]
		}
		line := strings.Join(names, ", ")
		if tn, ok := total.(float64); ok && int(tn) > len(names) {
			line += fmt.Sprintf(", … (+%d more)", int(tn)-len(names))
		}
		fmt.Fprintf(w, "//   %s (%v): %s\n", g, total, line)
	}
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

// referenceRow splits "path:line:col: text" or "path:line: text".
var referenceRow = regexp.MustCompile(`^(.+?):(\d+(?::\d+)?: .*|\d+(?::\d+)?)$`)

var genericLead = map[string]int{"query": 0, "name": 1, "status": 2, "error": 3, "warning": 4, "note": 5}
