package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// memberRelayLimit bounds the line-level relay inventory for a data-member
// anchor. Member sites are lines, not methods, so the method-level limit
// (40) would drop the inventory for any widely used field.
const memberRelayLimit = 120

// memberEvidenceLimit: include each access line's source text only while the
// inventory is small enough that the text is cheaper than a follow-up read.
const memberEvidenceLimit = 60

// memberImpactOutput renders change_impact for a data member (field,
// property, constant, variable). Its sites are SOURCE LINES that read,
// write, initialize, or declare the member — confirmed ones in accesses and
// relaySites, name-only matches in a separate, labelled ambiguous bucket.
// Nothing here claims the call graph covers field reads: it does not, and
// the old answer (the declaration alone, "copy this 1-site inventory")
// was the failure this replaces.
func memberImpactOutput(r *grove.ChangeImpactResult, fileScoped bool) map[string]any {
	withText := len(r.Accesses)+len(r.AmbiguousAccesses) <= memberEvidenceLimit
	render := func(in []grove.MemberAccess, reasonKey string) []map[string]any {
		out := make([]map[string]any, 0, len(in))
		for _, a := range in {
			m := map[string]any{"filePath": a.FilePath, "line": a.Line, "access": a.Access}
			if a.Enclosing != "" {
				m["enclosing"] = a.Enclosing
			}
			if a.Evidence != "" {
				m[reasonKey] = a.Evidence
			}
			if withText && a.Text != "" {
				m["text"] = compactImpactLine(a.Text)
			}
			if isVerifiedTestCaller("/" + a.FilePath) {
				m["isTest"] = true
			}
			out = append(out, m)
		}
		return out
	}
	decls := make([]map[string]any, 0, len(r.Declarations))
	for _, s := range r.Declarations {
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		decls = append(decls, map[string]any{
			"name": s.Name, "qualifiedName": qn, "filePath": s.FilePath,
			"line": s.Span.Start, "kind": s.Kind, "signature": s.Signature,
		})
	}
	confirmedLines := map[string]bool{}
	for _, a := range r.Accesses {
		confirmedLines[fmt.Sprintf("%s:%d", a.FilePath, a.Line)] = true
	}
	for _, s := range r.Declarations {
		confirmedLines[fmt.Sprintf("%s:%d", s.FilePath, s.Span.Start)] = true
	}
	out := map[string]any{
		"query":              r.Query,
		"memberKind":         r.MemberKind,
		"declarations":       decls,
		"accesses":           render(r.Accesses, "evidence"),
		"totalSites":         len(confirmedLines),
		"familyCompleteness": r.Completeness,
		"callerCoverage":     r.AccessCoverage,
		"completeness":       r.Completeness,
		"accessCoverage":     r.AccessCoverage,
	}
	addIndexedCompletenessSafety(out)
	if len(r.AmbiguousAccesses) > 0 {
		out["ambiguousAccesses"] = render(r.AmbiguousAccesses, "reason")
		out["ambiguousSites"] = len(r.AmbiguousAccesses)
	}
	if r.ExcludedAccesses > 0 {
		out["excludedAccesses"] = r.ExcludedAccesses
	}
	// Call-graph references into the member (template bindings, ORM
	// attribute references) that have no source-line access of their own.
	var refs []map[string]any
	accessEncl := map[string]bool{}
	for _, a := range r.Accesses {
		accessEncl[a.FilePath+"\x00"+a.Enclosing] = true
	}
	for _, s := range r.Callers {
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		if accessEncl[s.FilePath+"\x00"+qn] {
			continue
		}
		refs = append(refs, map[string]any{"name": s.Name, "qualifiedName": qn, "filePath": s.FilePath, "line": s.Span.Start, "kind": s.Kind})
	}
	if len(refs) > 0 {
		out["callers"] = refs
		out["totalSites"] = len(confirmedLines) + len(refs)
	}
	if relay := memberRelaySites(r, refs); len(relay) > 0 {
		out["relaySites"] = relay
		note := "relaySites lists the CONFIRMED sites (declaration plus reads/writes/initializers with receiver-type or declaring-scope evidence)."
		if n := len(r.AmbiguousAccesses); n > 0 {
			note += fmt.Sprintf(" %d further line(s) in ambiguousAccesses match the name without receiver evidence: check each one before including or excluding it; do not drop them silently.", n)
		}
		out["relayNote"] = note
	}
	var cov strings.Builder
	cov.WriteString("Field/variable impact is computed from source occurrences, not call edges. ")
	cov.WriteString(r.AccessNote)
	if r.HasHeuristicRefs {
		cov.WriteString(" Includes name-derived framework references.")
	}
	out["coverageNote"] = cov.String()
	if !fileScoped {
		files := map[string]bool{}
		for _, d := range r.Declarations {
			files[d.FilePath] = true
		}
		if len(files) > 1 {
			names := make([]string, 0, len(files))
			for f := range files {
				names = append(names, f)
			}
			sort.Strings(names)
			out["ambiguityNote"] = fmt.Sprintf("declarations span %d files (%s); re-run with file=\"<path fragment>\" to scope to one", len(files), strings.Join(names, ", "))
		}
	}
	return out
}

// memberRelaySites is the confirmed line inventory: path:line:enclosing
// [access]. Omitted past memberRelayLimit (accesses still lists every line).
func memberRelaySites(r *grove.ChangeImpactResult, refs []map[string]any) []string {
	type site struct {
		path  string
		line  int
		label string
	}
	seen := map[string]bool{}
	var sites []site
	add := func(path string, line int, encl, access string) {
		path = normalizePath(path)
		key := fmt.Sprintf("%s:%d", path, line)
		if seen[key] {
			return
		}
		seen[key] = true
		label := key
		if encl != "" {
			label += ":" + encl
		}
		sites = append(sites, site{path, line, label + " [" + access + "]"})
	}
	for _, d := range r.Declarations {
		qn := d.QualifiedName
		if qn == "" {
			qn = d.Name
		}
		declared := false
		for _, a := range r.Accesses {
			if a.Access == "decl" && a.FilePath == d.FilePath && a.Line >= d.Span.Start && a.Line <= d.Span.End {
				declared = true
			}
		}
		if !declared {
			add(d.FilePath, d.Span.Start, qn, "decl")
		}
	}
	for _, a := range r.Accesses {
		add(a.FilePath, a.Line, a.Enclosing, a.Access)
	}
	for _, m := range refs {
		add(fmt.Sprint(m["filePath"]), m["line"].(int), fmt.Sprint(m["qualifiedName"]), "reference")
	}
	if len(sites) > memberRelayLimit {
		return nil
	}
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].path != sites[j].path {
			return sites[i].path < sites[j].path
		}
		return sites[i].line < sites[j].line
	})
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.label
	}
	return out
}

// FormatMemberImpactText renders a data-member change_impact result (see
// memberImpactOutput) for MCP text delivery and the CLI. ok=false when out
// is not a member result.
func FormatMemberImpactText(out map[string]any) (string, bool) {
	kind, _ := out["memberKind"].(string)
	if kind == "" {
		return "", false
	}
	amb := anySlice(out["ambiguousAccesses"])
	var b strings.Builder
	fmt.Fprintf(&b, "// %v — change-impact (%s): %v confirmed site(s), %d ambiguous\n", out["query"], kind, out["totalSites"], len(amb))
	for _, key := range []string{"completeness", "accessCoverage", "completenessScope"} {
		if v, _ := out[key].(string); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", key, v)
		}
	}
	if safe, ok := out["safeToClaimComplete"].(bool); ok {
		fmt.Fprintf(&b, "safeToClaimComplete: %t\n", safe)
	}
	for _, key := range []string{"coverageNote", "scopeBoundary"} {
		if v, _ := out[key].(string); v != "" {
			fmt.Fprintf(&b, "// %s\n", v)
		}
	}
	relay := anySlice(out["relaySites"])
	if len(relay) > 0 {
		fmt.Fprintf(&b, "relaySites (%d confirmed; copy this inventory):\n", len(relay))
		for _, s := range relay {
			fmt.Fprintf(&b, "  %v\n", s)
		}
		if note, _ := out["relayNote"].(string); note != "" {
			fmt.Fprintf(&b, "// %s\n", note)
		}
	}
	if decls := anySlice(out["declarations"]); len(decls) > 0 {
		fmt.Fprintf(&b, "declarations (%d):\n", len(decls))
		for _, d := range decls {
			if m, ok := d.(map[string]any); ok {
				fmt.Fprintf(&b, "  %v  %v:%v\n", m["qualifiedName"], m["filePath"], m["line"])
				if sig, _ := m["signature"].(string); sig != "" {
					fmt.Fprintf(&b, "    %s\n", compactImpactLine(sig))
				}
			}
		}
	}
	writeAccesses := func(label string, items []any, reasonKey string) {
		fmt.Fprintf(&b, "%s (%d):\n", label, len(items))
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "  %v:%v", m["filePath"], m["line"])
			if e, _ := m["enclosing"].(string); e != "" {
				fmt.Fprintf(&b, "  %s", e)
			}
			fmt.Fprintf(&b, "  [%v]", m["access"])
			if t, _ := m["isTest"].(bool); t {
				b.WriteString(" [test]")
			}
			if r, _ := m[reasonKey].(string); r != "" && reasonKey == "reason" {
				fmt.Fprintf(&b, " — %s", r)
			}
			b.WriteString("\n")
			if t, _ := m["text"].(string); t != "" {
				fmt.Fprintf(&b, "    %s\n", t)
			}
		}
	}
	if len(relay) == 0 {
		if acc := anySlice(out["accesses"]); len(acc) > 0 {
			writeAccesses("accesses (confirmed)", acc, "evidence")
		}
	}
	if len(amb) > 0 {
		writeAccesses("ambiguousAccesses (name match, receiver untyped — check each)", amb, "reason")
	}
	if n, ok := out["excludedAccesses"]; ok {
		fmt.Fprintf(&b, "excludedAccesses: %v (same name, attributed elsewhere)\n", n)
	}
	if refs := anySlice(out["callers"]); len(refs) > 0 {
		fmt.Fprintf(&b, "references (%d, call-graph):\n", len(refs))
		for _, r := range refs {
			if m, ok := r.(map[string]any); ok {
				fmt.Fprintf(&b, "  %v  %v:%v\n", m["qualifiedName"], m["filePath"], m["line"])
			}
		}
	}
	for _, key := range []string{"ambiguityNote", "staleWarning", "scopeNote"} {
		if v, _ := out[key].(string); v != "" {
			fmt.Fprintf(&b, "// %s\n", v)
		}
	}
	return b.String(), true
}
