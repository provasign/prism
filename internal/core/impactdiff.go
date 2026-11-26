package core

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// toolChangeImpactDiff answers "what does my diff reach" (GAP-05), where
// verify answers "is my diff complete". It reuses verify's projection of
// the diff onto symbols and, per changed symbol:
//   - contract changes (signature, rename, removal): change-impact's sites,
//     split into those the diff already touched and those it did not;
//   - body-only changes: the direct callers, as "behaviour may change",
//     with no completeness claim;
//   - the components (directories) those sites are in, beyond its own;
//
// plus test-plan's tiers for the whole diff. No risk score: none is
// validated, and a made-up number undercuts a tool whose claim is
// precision.
func (h *Handler) toolChangeImpactDiff(ctx context.Context, args map[string]any) (any, error) {
	base := stringArg(args, "base", "HEAD")
	vout, err := h.toolVerify(ctx, map[string]any{"base": base})
	if err != nil {
		return nil, err
	}
	v, _ := vout.(map[string]any)
	out := map[string]any{"base": base}
	if note, _ := v["note"].(string); note != "" && v["verdict"] == "clean" {
		out["note"] = note
		out["changes"] = []map[string]any{}
		return out, nil
	}
	changed, err := gitChangedRanges(h.Root, base)
	if err != nil {
		return nil, err
	}
	var changes []map[string]any
	seen := map[string]bool{}
	for _, sc := range anyMaps(v["signatureChanges"]) {
		name, _ := sc["symbol"].(string)
		file, _ := sc["file"].(string)
		key := file + "\x00" + name
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		entry := map[string]any{"symbol": name, "file": file, "line": sc["line"], "kind": "contract", "reason": sc["reason"]}
		ci, cerr := h.toolChangeImpact(ctx, map[string]any{"query": name, "file": file})
		if cerr != nil {
			entry["error"] = cerr.Error()
			changes = append(changes, entry)
			continue
		}
		cm, _ := ci.(map[string]any)
		var touchedRows, untouched []string
		dirs := map[string]bool{}
		for _, r := range anySlice(cm["editSites"]) {
			row := fmt.Sprint(r)
			f, line := splitSiteRow(row)
			if f == "" {
				continue
			}
			dirs[path.Dir(f)] = true
			if lineTouched(changed, f, line) {
				touchedRows = append(touchedRows, row)
			} else {
				untouched = append(untouched, row)
			}
		}
		// verify's missed sites come from the base revision's callers: a
		// call the edit broke no longer resolves in the current code, so
		// change-impact alone missed it (docPath's untouched caller).
		have := map[string]bool{}
		for _, r := range untouched {
			f, l := splitSiteRow(r)
			have[fmt.Sprintf("%s:%d", f, l)] = true
		}
		for _, ms := range anyMaps(v["missedSites"]) {
			if ms["becauseOf"] != sc["reason"] {
				continue
			}
			f := normalizePath(fmt.Sprint(ms["file"]))
			k := fmt.Sprintf("%s:%v", f, ms["line"])
			if have[k] {
				continue
			}
			have[k] = true
			untouched = append(untouched, fmt.Sprintf("%s:%v: %v (%v)", f, ms["line"], ms["symbol"], ms["detail"]))
			dirs[path.Dir(f)] = true
		}
		sort.Strings(untouched)
		entry["touched"] = touchedRows
		entry["untouched"] = untouched
		entry["completeness"] = cm["completeness"]
		entry["components"] = otherComponents(dirs, file)
		changes = append(changes, entry)
	}
	for _, tc := range anyMaps(v["testCoverage"]) {
		name, _ := tc["symbol"].(string)
		file, _ := tc["file"].(string)
		key := file + "\x00" + name
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		entry := map[string]any{"symbol": name, "file": file, "line": tc["line"], "kind": "body",
			"reason": "body changed; callers may see different behaviour (no completeness claim)"}
		recs, eerr := h.Grove.Edges(ctx, name, "in", []string{"calls"})
		if eerr == nil {
			var callers []string
			dirs := map[string]bool{}
			for _, e := range recs {
				callers = append(callers, fmt.Sprintf("%s  %s:%d", e.Name, normalizePath(e.File), e.Line))
				dirs[path.Dir(normalizePath(e.File))] = true
			}
			sort.Strings(callers)
			entry["callers"] = callers
			entry["components"] = otherComponents(dirs, file)
		}
		if cov, ok := tc["coveredBy"]; ok {
			entry["coveredBy"] = cov
		}
		changes = append(changes, entry)
	}
	if changes == nil {
		changes = []map[string]any{}
	}
	out["changes"] = changes
	out["changedFiles"] = v["changedFiles"]
	if tp, terr := h.toolTestPlan(ctx, map[string]any{"base": base}); terr == nil {
		if m, ok := tp.(map[string]any); ok {
			tests := map[string]any{}
			var focused []string
			transitive := 0
			for _, t := range anyMaps(m["fileTargets"]) {
				switch t["classification"] {
				case "transitive file":
					transitive++
				default:
					focused = append(focused, fmt.Sprint(t["file"]))
				}
			}
			tests["runFirst"] = focused
			tests["transitive"] = transitive
			tests["safetyFiles"] = m["safetyFileCount"]
			out["tests"] = tests
		}
	}
	return out, nil
}

// splitSiteRow parses "path:line:col: text" or "path:line: text".
func splitSiteRow(row string) (string, int) {
	parts := strings.SplitN(row, ":", 3)
	if len(parts) < 2 {
		return "", 0
	}
	n := 0
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return "", 0
		}
		n = n*10 + int(c-'0')
	}
	return normalizePath(parts[0]), n
}

func otherComponents(dirs map[string]bool, own string) []string {
	ownDir := path.Dir(normalizePath(own))
	var out []string
	for d := range dirs {
		if d != ownDir {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// anyMaps returns the map elements of a []any or []map[string]any.
func anyMaps(v any) []map[string]any {
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// renderImpactDiffText prints the diff impact for people and agents.
func renderImpactDiffText(out map[string]any) string {
	var b strings.Builder
	changes := anyMaps(out["changes"])
	fmt.Fprintf(&b, "// change-impact --diff vs %v: %d changed symbol(s)\n", out["base"], len(changes))
	if note, _ := out["note"].(string); note != "" {
		fmt.Fprintf(&b, "// %s\n", note)
	}
	for _, c := range changes {
		fmt.Fprintf(&b, "\n%v (%v:%v) — %v\n", c["symbol"], c["file"], c["line"], c["reason"])
		if e, _ := c["error"].(string); e != "" {
			fmt.Fprintf(&b, "  impact unavailable: %s\n", e)
			continue
		}
		if c["kind"] == "contract" {
			untouched := anySlice(c["untouched"])
			touched := anySlice(c["touched"])
			fmt.Fprintf(&b, "  completeness: %v; %d site(s) the diff touched, %d it did not\n", c["completeness"], len(touched), len(untouched))
			if len(untouched) > 0 {
				b.WriteString("  not touched by the diff:\n")
				for _, r := range untouched {
					fmt.Fprintf(&b, "    %v\n", r)
				}
			}
		} else {
			callers := anySlice(c["callers"])
			fmt.Fprintf(&b, "  direct callers (%d), behaviour may change:\n", len(callers))
			for _, r := range callers {
				fmt.Fprintf(&b, "    %v\n", r)
			}
		}
		if comps := anySlice(c["components"]); len(comps) > 0 {
			parts := make([]string, 0, len(comps))
			for _, x := range comps {
				parts = append(parts, fmt.Sprint(x))
			}
			fmt.Fprintf(&b, "  other components reached: %s\n", strings.Join(parts, ", "))
		}
	}
	if t, ok := out["tests"].(map[string]any); ok {
		first := anySlice(t["runFirst"])
		fmt.Fprintf(&b, "\ntests: run first %d file(s)", len(first))
		if len(first) > 0 && len(first) <= 10 {
			parts := make([]string, 0, len(first))
			for _, x := range first {
				parts = append(parts, fmt.Sprint(x))
			}
			fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
		}
		fmt.Fprintf(&b, ", %v transitive, %v for safety; prism test-plan lists them\n", t["transitive"], t["safetyFiles"])
	}
	return b.String()
}
