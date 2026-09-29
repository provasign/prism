package mcp

// change_impact additions derived from the 2026-09-26 review of failed
// benchmark tasks (jackson-databind pr6061/pr5977/pr6052/pr6018, zod
// pr6129). Each addition is either a reference site the engine already
// knows about (re-exports) or a clearly labelled pointer that is NOT part
// of the change set (related sites, test-only overloads), so relaySites
// keeps its completeness meaning.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// ── signature selects an overload ────────────────────────────────────────

// signatureParamTypes extracts the parameter types from a declaration-shaped
// signature ("TreeTraversingParser(JsonNode n, ObjectReadContext c)",
// "func (r *T) Do(ctx context.Context, n int) error", "gte(value: number)")
// in the form grove's Type.method(P1, P2) query accepts. ok=false when no
// parameter list can be found.
func signatureParamTypes(signature, language string) ([]string, bool) {
	sig := strings.TrimSpace(signature)
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return nil, false
	}
	if strings.HasPrefix(sig, "func (") {
		// Go method: skip the receiver list.
		if _, end, ok := parenGroup(sig, open); ok {
			if next := strings.IndexByte(sig[end+1:], '('); next >= 0 {
				open = end + 1 + next
			}
		}
	}
	inner, _, ok := parenGroup(sig, open)
	if !ok {
		return nil, false
	}
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return []string{}, true
	}
	params := splitTopLevelCommas(inner)
	types := make([]string, len(params))
	named := false
	for i, p := range params {
		p = strings.TrimSpace(p)
		if eq := topLevelIndex(p, '='); eq >= 0 {
			p = strings.TrimSpace(p[:eq]) // default value
		}
		if colon := topLevelIndex(p, ':'); colon >= 0 && language != "cpp" && language != "c" {
			types[i] = strings.TrimSpace(p[colon+1:])
			continue
		}
		fields := strings.Fields(p)
		for len(fields) > 1 && (fields[0] == "final" || strings.HasPrefix(fields[0], "@")) {
			fields = fields[1:]
		}
		switch {
		case len(fields) == 0:
			return nil, false
		case language == "go":
			if len(fields) >= 2 {
				named = true
				types[i] = strings.Join(fields[1:], " ")
			} else {
				types[i] = ""
				if !named {
					types[i] = fields[0]
				}
			}
		case len(fields) == 1:
			types[i] = fields[0]
		default:
			types[i] = strings.Join(fields[:len(fields)-1], " ")
		}
	}
	if language == "go" && named {
		carry := ""
		for i := len(types) - 1; i >= 0; i-- {
			if f := strings.Fields(strings.TrimSpace(params[i])); len(f) >= 2 {
				carry = types[i]
			} else {
				types[i] = carry
			}
		}
	}
	for _, t := range types {
		if strings.TrimSpace(t) == "" {
			return nil, false
		}
	}
	return types, true
}

func parenGroup(s string, open int) (string, int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
		}
	}
	return "", 0, false
}

func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func topLevelIndex(s string, want byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case want:
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// selectOverloadBySignature narrows a merged overload result to the one the
// caller's signature names. Measured on jackson-databind pr6052: passing
// the 3-arg TreeTraversingParser constructor's signature still returned all
// three constructors merged, hiding that only tests call the 3-arg one.
// Returns the narrowed result and a note, or (nil, note) when the
// signature matched no single overload (the merged result stands).
func (h *Handler) selectOverloadBySignature(ctx context.Context, query, file, signature string, r *grove.ChangeImpactResult) (*grove.ChangeImpactResult, string) {
	if r == nil || signature == "" || strings.Contains(query, "(") || len(r.Declarations) < 2 {
		return nil, ""
	}
	params, ok := signatureParamTypes(signature, r.Declarations[0].Language)
	if !ok {
		return nil, fmt.Sprintf("signature %q has no parseable parameter list; all %d overloads are merged below", signature, len(r.Declarations))
	}
	narrowed, err := h.Grove.ChangeImpactScoped(ctx, query+"("+strings.Join(params, ", ")+")", file)
	if err != nil || narrowed == nil || len(narrowed.Declarations) == 0 || len(narrowed.Declarations) >= len(r.Declarations) {
		return nil, fmt.Sprintf("signature %q matched no single overload; all %d overloads are merged below", signature, len(r.Declarations))
	}
	d := narrowed.Declarations[0]
	return narrowed, fmt.Sprintf("signature selected 1 of %d overloads: %s line %d; the other overloads and their callers are excluded",
		len(r.Declarations), displayQN(d), d.Span.Start)
}

// ── change_impact on an override that does not exist yet ─────────────────

// inheritedImpact answers change_impact Type.method when Type declares no
// method but a supertype does: adding an override is the common edit
// (jackson-databind pr6018 added StdConvertingSerializer
// .isUnwrappingSerializer), and the old dead-end error sent the agent away.
// The inherited declaration's change set is what the new override joins.
func (h *Handler) inheritedImpact(ctx context.Context, query, file string) (*grove.ChangeImpactResult, string, bool) {
	base, params := query, ""
	if i := strings.IndexByte(base, '('); i >= 0 {
		base, params = base[:i], base[i:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return nil, "", false
	}
	typeName, method := strings.TrimSpace(base[:dot]), strings.TrimSpace(base[dot+1:])
	q := parseLookupName(typeName)
	if len(q.segs) == 0 {
		return nil, "", false
	}
	loc := newLookupLocator(h.Root)
	found, _ := h.Grove.SearchSymbols(ctx, q.name(), 50)
	var owners []grove.SymbolRecord
	for _, t := range dedupeSymbolsByID(found) {
		if !isTypeKind(t.Kind) {
			continue
		}
		if file != "" && !strings.Contains(t.FilePath, file) {
			continue
		}
		if _, ok, _ := loc.lookupMatch(q, t, false); ok {
			owners = append(owners, t)
		}
	}
	if len(owners) != 1 {
		return nil, "", false // unknown or ambiguous owner: keep the original error
	}
	owner := owners[0]
	type hop struct {
		sym   grove.SymbolRecord
		chain []string
	}
	visited := map[string]bool{owner.ID: true}
	queue := []hop{{owner, []string{lookupSymbolName(owner)}}}
	for depth := 0; depth < 8 && len(queue) > 0 && len(visited) < 64; depth++ {
		var next []hop
		for _, cur := range queue {
			edges, err := h.Grove.Edges(ctx, cur.sym.ID, "out", []string{"extends", "implements"})
			if err != nil {
				continue
			}
			sort.SliceStable(edges, func(i, j int) bool { return edges[i].Name < edges[j].Name })
			for _, e := range edges {
				super, ok := h.symbolAt(ctx, e.File, e.Name, e.Line)
				if !ok || visited[super.ID] {
					continue
				}
				visited[super.ID] = true
				chain := append(append([]string(nil), cur.chain...), lookupSymbolName(super))
				if members := h.typeMembers(ctx, super, method); len(members) > 0 {
					r, err := h.Grove.ChangeImpactScoped(ctx, lookupSymbolName(super)+"."+method+params, super.FilePath)
					if err != nil || r == nil || len(r.Declarations) == 0 {
						return nil, "", false
					}
					d := r.Declarations[0]
					note := fmt.Sprintf("%s.%s is not declared on %s; it is inherited from %s (%s:%d) via %s. "+
						"Adding an override on %s joins this family: the sites below are the inherited declaration's "+
						"change set (callers dispatching through %s can reach the new override; the family lists "+
						"sibling overrides to model it on).",
						lookupSymbolName(owner), method, lookupSymbolName(owner), displayQN(d), d.FilePath, d.Span.Start,
						strings.Join(chain, " -> "), lookupSymbolName(owner), displayQN(d))
					return r, note, true
				}
				next = append(next, hop{super, chain})
			}
		}
		queue = next
	}
	return nil, "", false
}

// ── test-only API signal ─────────────────────────────────────────────────

const maxTestOnlyChecks = 8

type callerCounts struct {
	prod, test int
	delegating []int // lines of same-named overloads that call this one
	prodSites  []grove.SymbolRecord
}

func (h *Handler) countCallers(ctx context.Context, s grove.SymbolRecord) (callerCounts, bool) {
	var c callerCounts
	callers, err := h.Grove.InboundCallers(ctx, s.ID)
	if err != nil {
		return c, false
	}
	seen := map[string]bool{}
	for _, caller := range callers {
		if seen[caller.ID] || caller.ID == s.ID {
			continue
		}
		seen[caller.ID] = true
		switch {
		case caller.FilePath == s.FilePath && displayQN(caller) == displayQN(s):
			c.delegating = append(c.delegating, caller.Span.Start)
		case isVerifiedTestCaller("/" + caller.FilePath):
			c.test++
		default:
			c.prod++
			c.prodSites = append(c.prodSites, caller)
		}
	}
	sort.Ints(c.delegating)
	return c, true
}

// sameNameOverloads returns the other declarations sharing s's qualified
// name in its file (Java/C#/Kotlin/C++ overloads).
func (h *Handler) sameNameOverloads(ctx context.Context, s grove.SymbolRecord) []grove.SymbolRecord {
	syms, err := h.Grove.FileSymbols(ctx, s.FilePath)
	if err != nil {
		return nil
	}
	var out []grove.SymbolRecord
	for _, o := range dedupeSymbolsByID(syms) {
		if o.ID != s.ID && displayQN(o) == displayQN(s) && testOnlyCheckable(o.Kind) {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Span.Start < out[j].Span.Start })
	return out
}

func testOnlyCheckable(kind string) bool {
	return kind == "method" || kind == "function" || kind == "constructor"
}

func siteLabel(s grove.SymbolRecord) string {
	return fmt.Sprintf("%s (%s:%d)", displayQN(s), normalizePath(s.FilePath), s.Span.Start)
}

// testOnlySignal reports "0 production callers (N test callers)" for a
// method/constructor whose every caller is a test, and names the nearest
// production call sites of the same-named API. jackson-databind pr6052:
// a partial commit had added a 3-arg TreeTraversingParser constructor that
// only tests called; both benchmark arms read its existence as "already
// implemented" and never found the production site
// (DeserializationContext._treeAsTokens) that had to start using it.
// Returns nil unless at least one symbol is test-only.
func (h *Handler) testOnlySignal(ctx context.Context, syms []grove.SymbolRecord) []string {
	var out []string
	checked := 0
	for _, s := range syms {
		if s.ID == "" || !testOnlyCheckable(s.Kind) || isVerifiedTestCaller("/"+s.FilePath) {
			continue
		}
		if checked >= maxTestOnlyChecks {
			break
		}
		checked++
		c, ok := h.countCallers(ctx, s)
		if !ok || c.prod > 0 || c.test == 0 {
			continue
		}
		line := fmt.Sprintf("%s line %d: 0 production callers (%d test caller%s)",
			displayQN(s), s.Span.Start, c.test, plural(c.test))
		if len(c.delegating) > 0 {
			lines := make([]string, len(c.delegating))
			for i, l := range c.delegating {
				lines[i] = fmt.Sprint(l)
			}
			line += fmt.Sprintf("; only its own overload(s) at line %s delegate to it", strings.Join(lines, ", "))
		}
		var groups []string
		shown := 0
		seen := map[string]bool{}
		for _, o := range h.sameNameOverloads(ctx, s) {
			oc, ok := h.countCallers(ctx, o)
			if !ok || len(oc.prodSites) == 0 {
				continue
			}
			var names []string
			for _, p := range oc.prodSites {
				if seen[p.ID] {
					continue
				}
				seen[p.ID] = true
				if shown < 3 {
					names = append(names, siteLabel(p))
					shown++
				}
			}
			label := fmt.Sprintf("overload line %d is called by %d production site%s", o.Span.Start, len(oc.prodSites), plural(len(oc.prodSites)))
			if len(names) > 0 {
				label += ": " + strings.Join(names, "; ")
				if len(names) < len(oc.prodSites) {
					label += fmt.Sprintf(" (+%d more)", len(oc.prodSites)-len(names))
				}
			}
			groups = append(groups, label)
		}
		if len(groups) > 0 {
			line += ". Production code calls the other overload(s) instead — " + strings.Join(groups, " | ") +
				". If this API is meant to be used, those are the sites that would switch to it"
		} else {
			line += ". No production code calls it or a same-named overload"
		}
		out = append(out, line)
	}
	return out
}

// ── related sites and re-exports ─────────────────────────────────────────

func impactRelatedOutput(r *grove.ChangeImpactResult) []map[string]any {
	if r == nil || len(r.Related) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(r.Related))
	for _, rel := range r.Related {
		out = append(out, map[string]any{
			"name":     displayQN(rel.Symbol),
			"filePath": normalizePath(rel.Symbol.FilePath),
			"line":     rel.Symbol.Span.Start,
			"relation": rel.Relation,
			"detail":   rel.Detail,
		})
	}
	return out
}

func impactReExportOutput(r *grove.ChangeImpactResult) []map[string]any {
	if r == nil || len(r.ReExports) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(r.ReExports))
	for _, a := range r.ReExports {
		out = append(out, map[string]any{
			"filePath": normalizePath(a.FilePath), "line": a.Line,
			"text": a.Text, "evidence": a.Evidence,
		})
	}
	return out
}

// reExportRelayLabels renders re-export lines in relaySites form.
func reExportRelayLabels(r *grove.ChangeImpactResult) []string {
	var out []string
	for _, a := range r.ReExports {
		name := strings.TrimSpace(a.Evidence)
		if i := strings.Index(name, " as "); i >= 0 {
			name = strings.Fields(name[i+4:])[0]
		} else if len(r.Declarations) > 0 {
			name = r.Declarations[0].Name
		}
		out = append(out, fmt.Sprintf("%s:%d:%s [re-export]", normalizePath(a.FilePath), a.Line, name))
	}
	return out
}

// FormatImpactHeaderNotesText renders the notes that change how the whole
// result must be read (an inherited anchor, a signature-selected overload);
// they go directly under the header line.
func FormatImpactHeaderNotesText(out map[string]any) string {
	var b strings.Builder
	for _, key := range []string{"inheritedNote", "signatureNote"} {
		if note, _ := out[key].(string); note != "" {
			fmt.Fprintf(&b, "// %s\n", note)
		}
	}
	return b.String()
}

// FormatImpactExtrasText renders the change_impact additions shared by the
// MCP text form and the CLI: the test-only signal, re-export sites, and the
// related-but-not-affected group.
func FormatImpactExtrasText(out map[string]any) string {
	var b strings.Builder
	if lines := anySlice(out["testOnly"]); len(lines) > 0 {
		b.WriteString("// TEST-ONLY API (callers are all tests):\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "//   %v\n", l)
		}
	}
	if rex := anySlice(out["reExports"]); len(rex) > 0 {
		fmt.Fprintf(&b, "reExports (%d; export specifiers that re-export or alias it — included in relaySites):\n", len(rex))
		for _, raw := range rex {
			m, _ := raw.(map[string]any)
			if m == nil {
				continue
			}
			fmt.Fprintf(&b, "  %v:%v  %v  (%v)\n", m["filePath"], m["line"], m["text"], m["evidence"])
		}
	}
	if rel := anySlice(out["related"]); len(rel) > 0 {
		fmt.Fprintf(&b, "related (%d; NOT in the change set or relaySites — sites that may need the same edit, check each):\n", len(rel))
		for _, raw := range rel {
			m, _ := raw.(map[string]any)
			if m == nil {
				continue
			}
			fmt.Fprintf(&b, "  %v:%v %v [%v] %v\n", m["filePath"], m["line"], m["name"], m["relation"], m["detail"])
		}
	}
	return b.String()
}
