package graphui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ImpactFunc runs Prism's change-impact for one declaration and returns the
// tool's JSON answer — the same answer `prism change-impact` and the MCP
// tool give. file scopes the query to the declaring file.
type ImpactFunc func(ctx context.Context, query, file string) (map[string]any, error)

// Roles of the nodes in an impact view, from the change outward.
const (
	RoleTarget     = "target"     // the declaration being changed
	RoleFamily     = "family"     // overrides/implementations/supers that change with it
	RoleCaller     = "caller"     // direct callers: edit sites
	RoleAccess     = "access"     // reads/writes of a data member: edit sites
	RoleTransitive = "transitive" // callers of callers: reached, not edit sites
)

// Limits on one impact view; caps are reported, never silent.
const (
	maxImpactNodes = 260
	// Above this many edit-site declarations, sites and reached nodes are
	// drawn one node per file: 200 caller nodes on a ring read as noise.
	maxSiteNodes       = 60
	maxTransitiveDepth = 3
)

// ImpactSite is one confirmed edit site from the change-impact answer.
type ImpactSite struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol,omitempty"`
	NodeID string `json:"nodeId,omitempty"`
	Role   string `json:"role"`
	Access string `json:"access,omitempty"` // decl | read | write | init | call
	Text   string `json:"text,omitempty"`
	Test   bool   `json:"test,omitempty"`
}

// Impact is one rendered change-impact view.
type Impact struct {
	Query  string       `json:"query"`
	Target Node         `json:"target"`
	Crumbs []Crumb      `json:"crumbs"`
	Nodes  []Node       `json:"nodes"`
	Links  []Link       `json:"links"`
	Sites  []ImpactSite `json:"sites"`

	Depth        int  `json:"depth"`
	IncludeTests bool `json:"includeTests"`
	Hidden       int  `json:"hidden"`
	TestsHidden  int  `json:"testsHidden"` // test-file nodes left out (sites still listed)

	// Verdict, copied from the change-impact answer.
	Completeness        string   `json:"completeness,omitempty"`
	CompletenessScope   string   `json:"completenessScope,omitempty"`
	SafeToClaimComplete bool     `json:"safeToClaimComplete"`
	ScopeBoundary       string   `json:"scopeBoundary,omitempty"`
	CoverageNote        string   `json:"coverageNote,omitempty"`
	MemberKind          string   `json:"memberKind,omitempty"`
	TotalSites          int      `json:"totalSites"`
	RelaySites          []string `json:"relaySites,omitempty"`
	Related             []Hit    `json:"related,omitempty"`
	Notes               []string `json:"notes,omitempty"`

	Counts map[string]int `json:"counts"` // nodes per role, before caps
	// GroupedByFile: edit sites and reached nodes are drawn one per file
	// (Node.Symbols counts the declarations each stands for).
	GroupedByFile bool `json:"groupedByFile,omitempty"`
	Files         int  `json:"files"` // distinct files holding an edit site
}

// ImpactQuery returns the change-impact query for a symbol: its qualified
// name, scoped to its file.
func (m *Model) ImpactQuery(id string) (query, file string, err error) {
	typ, key, err := splitScope(id)
	if err != nil || typ != "s" {
		return "", "", errors.New("change impact is available for symbols only")
	}
	i, ok := m.lookupSymbol(key)
	if !ok {
		return "", "", fmt.Errorf("symbol %q is not in the index", key)
	}
	s := &m.syms[i]
	return symLabel(s), s.FilePath, nil
}

// entry is one symbol-shaped record from the change-impact JSON.
type entry struct {
	Path, Name, QName, Kind, Signature string
	Line                               int
}

func entries(raw map[string]any, key string) []entry {
	list, _ := raw[key].([]any)
	out := make([]entry, 0, len(list))
	for _, v := range list {
		e, _ := v.(map[string]any)
		if e == nil {
			continue
		}
		out = append(out, entry{
			Path: str(e["filePath"]), Name: str(e["name"]), QName: str(e["qualifiedName"]),
			Kind: str(e["kind"]), Signature: str(e["signature"]), Line: num(e["line"]),
		})
	}
	return out
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// locate maps a reported declaration to an indexed symbol: same file and
// qualified name first, then same file and start line, then the innermost
// drawn symbol whose span holds the line.
func (m *Model) locate(path, qname string, line int) (int32, bool) {
	syms := m.fileSyms[path]
	if qname != "" {
		for _, i := range syms {
			if symLabel(&m.syms[i]) == qname && (line == 0 || m.syms[i].Span.Start == line) {
				return i, true
			}
		}
		for _, i := range syms {
			if symLabel(&m.syms[i]) == qname {
				return i, true
			}
		}
	}
	return m.enclosing(path, line)
}

// enclosing returns the innermost symbol of a file whose span holds line.
func (m *Model) enclosing(path string, line int) (int32, bool) {
	if line <= 0 {
		return 0, false
	}
	best, bestSpan := int32(-1), 1<<30
	for _, i := range m.fileSyms[path] {
		s := &m.syms[i]
		if s.Span.Start <= line && line <= s.Span.End && s.Span.End-s.Span.Start < bestSpan {
			best, bestSpan = i, s.Span.End-s.Span.Start
		}
	}
	return best, best >= 0
}

// Impact renders a change-impact answer for symbol id onto the graph: the
// declaration, the family that changes with it, every edit site, and —
// when depth > 1 — the callers of those callers, which are reached by the
// change but are not edit sites themselves.
func (m *Model) Impact(id string, raw map[string]any, depth int, includeTests bool) (*Impact, error) {
	typ, key, err := splitScope(id)
	if err != nil || typ != "s" {
		return nil, errors.New("change impact is available for symbols only")
	}
	ti, ok := m.lookupSymbol(key)
	if !ok {
		return nil, fmt.Errorf("symbol %q is not in the index", key)
	}
	if depth < 1 {
		depth = 1
	}
	if depth > maxTransitiveDepth {
		depth = maxTransitiveDepth
	}
	ts := &m.syms[ti]
	tests := includeTests || m.fileTest[ts.FilePath]
	out := &Impact{
		Query: str(raw["query"]), Depth: depth, IncludeTests: tests,
		Crumbs:            m.crumbs(dirOf(ts.FilePath), ts.FilePath, ti),
		Completeness:      str(raw["completeness"]),
		CompletenessScope: str(raw["completenessScope"]),
		ScopeBoundary:     str(raw["scopeBoundary"]),
		CoverageNote:      str(raw["coverageNote"]),
		MemberKind:        str(raw["memberKind"]),
		TotalSites:        num(raw["totalSites"]),
		Counts:            map[string]int{},
	}
	out.SafeToClaimComplete, _ = raw["safeToClaimComplete"].(bool)
	for _, v := range asList(raw["relaySites"]) {
		if s := str(v); s != "" {
			out.RelaySites = append(out.RelaySites, s)
		}
	}
	for _, k := range []string{"inferenceNote", "inheritedNote", "familyPickNote", "modulePathNote", "ambiguityNote"} {
		if s := str(raw[k]); s != "" {
			out.Notes = append(out.Notes, s)
		}
	}

	type member struct {
		node  Node
		role  string
		sym   int32 // -1 when the entry maps to no indexed symbol
		order int
	}
	members := map[string]*member{}
	var order []string
	rank := map[string]int{RoleTarget: 0, RoleFamily: 1, RoleCaller: 2, RoleAccess: 2, RoleTransitive: 3}
	add := func(n Node, role string, sym int32) *member {
		if cur, ok := members[n.ID]; ok {
			if rank[role] < rank[cur.role] {
				cur.role = role
			}
			return cur
		}
		mb := &member{node: n, role: role, sym: sym, order: len(order)}
		members[n.ID] = mb
		order = append(order, n.ID)
		return mb
	}
	nodeFor := func(e entry) (Node, int32) {
		if i, ok := m.locate(e.Path, firstNonEmpty(e.QName, e.Name), e.Line); ok {
			return m.symNode(i), i
		}
		// Reported but not indexed as a symbol: keep it, unlinked to the
		// model, so nothing in the answer is dropped.
		return Node{ID: fmt.Sprintf("x:%s:%d", e.Path, e.Line), Label: firstNonEmpty(e.QName, e.Name, e.Path),
			Type: "symbol", Kind: e.Kind, Path: e.Path, Line: e.Line, Signature: e.Signature,
			Test: m.fileTest[e.Path]}, -1
	}

	tn := m.symNode(ti)
	tn.Focus = true
	out.Target = tn
	add(tn, RoleTarget, ti)
	for _, e := range append(entries(raw, "declarations"), entries(raw, "declaringTypes")...) {
		n, i := nodeFor(e)
		add(n, RoleTarget, i)
	}
	for _, key := range []string{"family", "supers"} {
		for _, e := range entries(raw, key) {
			n, i := nodeFor(e)
			add(n, RoleFamily, i)
		}
	}
	// Sites: callers (with their call lines) and member accesses.
	evidence := func(v any) (line int, text string) {
		for _, ev := range asList(v) {
			em, _ := ev.(map[string]any)
			if em != nil {
				return num(em["line"]), str(em["text"])
			}
		}
		return 0, ""
	}
	callerList := asList(raw["callers"])
	for _, v := range callerList {
		c, _ := v.(map[string]any)
		if c == nil {
			continue
		}
		e := entry{Path: str(c["filePath"]), Name: str(c["name"]), QName: str(c["qualifiedName"]),
			Kind: str(c["kind"]), Signature: str(c["signature"]), Line: num(c["line"])}
		n, i := nodeFor(e)
		mb := add(n, RoleCaller, i)
		line, text := evidence(c["evidence"])
		if line == 0 {
			line = e.Line
		}
		out.Sites = append(out.Sites, ImpactSite{Path: e.Path, Line: line, Symbol: mb.node.Label,
			NodeID: mb.node.ID, Role: RoleCaller, Access: "call", Text: text, Test: m.fileTest[e.Path]})
	}
	for _, v := range asList(raw["accesses"]) {
		a, _ := v.(map[string]any)
		if a == nil {
			continue
		}
		path, line := str(a["filePath"]), num(a["line"])
		site := ImpactSite{Path: path, Line: line, Symbol: str(a["enclosing"]), Role: RoleAccess,
			Access: str(a["access"]), Text: strings.TrimSpace(str(a["text"])), Test: m.fileTest[path]}
		if site.Access == "decl" {
			site.NodeID, site.Role = tn.ID, RoleTarget
		} else if i, ok := m.locate(path, site.Symbol, 0); ok {
			a := m.anchor[i]
			mb := add(m.symNode(a), RoleAccess, a)
			site.NodeID, site.Symbol = mb.node.ID, mb.node.Label
		} else if i, ok := m.enclosing(path, line); ok {
			a := m.anchor[i]
			mb := add(m.symNode(a), RoleAccess, a)
			site.NodeID, site.Symbol = mb.node.ID, mb.node.Label
		}
		out.Sites = append(out.Sites, site)
	}
	// Declarations of family members are edit sites too.
	for _, id := range order {
		mb := members[id]
		if mb.role == RoleFamily || (mb.role == RoleTarget && id != tn.ID) {
			out.Sites = append(out.Sites, ImpactSite{Path: mb.node.Path, Line: mb.node.Line,
				Symbol: mb.node.Label, NodeID: id, Role: mb.role, Access: "decl", Test: mb.node.Test})
		}
	}
	if len(callerList) == 0 && len(asList(raw["accesses"])) == 0 {
		out.Sites = append(out.Sites, ImpactSite{Path: tn.Path, Line: tn.Line, Symbol: tn.Label,
			NodeID: tn.ID, Role: RoleTarget, Access: "decl", Test: tn.Test})
	} else if !hasSite(out.Sites, tn.ID) {
		out.Sites = append([]ImpactSite{{Path: tn.Path, Line: tn.Line, Symbol: tn.Label,
			NodeID: tn.ID, Role: RoleTarget, Access: "decl", Test: tn.Test}}, out.Sites...)
	}

	// Transitive callers: walk call edges upstream from the direct sites.
	frontier := []int32{}
	inSet := map[int32]bool{}
	for _, id := range order {
		if mb := members[id]; mb.sym >= 0 {
			inSet[mb.sym] = true
			if mb.role == RoleCaller || mb.role == RoleAccess || mb.role == RoleFamily || mb.role == RoleTarget {
				frontier = append(frontier, mb.sym)
			}
		}
	}
	for d := 2; d <= depth && len(frontier) > 0; d++ {
		var next []int32
		for _, s := range frontier {
			for _, di := range m.in[s] {
				dp := m.deps[di]
				if dp.kind != "calls" {
					continue
				}
				a := m.anchor[dp.from]
				if inSet[a] {
					continue
				}
				inSet[a] = true
				add(m.symNode(a), RoleTransitive, a)
				next = append(next, a)
			}
		}
		frontier = next
	}

	// Count, filter tests, cap. Edit sites rank before reached-only nodes.
	files := map[string]bool{}
	for _, s := range out.Sites {
		files[s.Path] = true
	}
	out.Files = len(files)
	var kept []*member
	for _, id := range order {
		mb := members[id]
		out.Counts[mb.role]++
		if mb.node.Test && !tests && id != tn.ID {
			out.TestsHidden++
			continue
		}
		kept = append(kept, mb)
	}
	sort.SliceStable(kept, func(i, j int) bool { return rank[kept[i].role] < rank[kept[j].role] })
	// symNodes: every indexed symbol in the view and the node standing for it.
	symNodes := map[int32]string{}
	siteDecls := 0
	for _, mb := range kept {
		if mb.role == RoleCaller || mb.role == RoleAccess {
			siteDecls++
		}
	}
	if siteDecls > maxSiteNodes {
		out.GroupedByFile = true
		remap := map[string]string{}
		groups := map[string]*member{}
		var grouped []*member
		for _, mb := range kept {
			if mb.role == RoleTarget || mb.role == RoleFamily {
				grouped = append(grouped, mb)
				continue
			}
			class := "site"
			if mb.role == RoleTransitive {
				class = "reach"
			}
			gid := "g:" + class + ":" + mb.node.Path
			g := groups[gid]
			if g == nil {
				g = &member{node: Node{ID: gid, Label: path.Base(mb.node.Path), Type: "file", Kind: m.fileLanguage(mb.node.Path),
					Path: mb.node.Path, Test: mb.node.Test, Files: 1}, role: mb.role, sym: -1}
				groups[gid] = g
				grouped = append(grouped, g)
			}
			g.node.Symbols++
			if rank[mb.role] < rank[g.role] {
				g.role = mb.role
			}
			remap[mb.node.ID] = gid
			if mb.sym >= 0 {
				symNodes[mb.sym] = gid
			}
		}
		for i := range out.Sites {
			if gid, ok := remap[out.Sites[i].NodeID]; ok {
				out.Sites[i].NodeID = gid
			}
		}
		kept = grouped
	}
	if len(kept) > maxImpactNodes {
		out.Hidden = len(kept) - maxImpactNodes
		kept = kept[:maxImpactNodes]
	}
	b := newBuilder(m)
	symToNode := map[int32]string{}
	drawn := map[string]bool{}
	for _, mb := range kept {
		drawn[mb.node.ID] = true
	}
	for sym, id := range symNodes {
		if drawn[id] {
			symToNode[sym] = id
		}
	}
	for _, mb := range kept {
		n := mb.node
		n.Kind = firstNonEmpty(n.Kind, "symbol")
		n.Role = mb.role
		b.node(n)
		if mb.sym >= 0 {
			symToNode[mb.sym] = n.ID
		}
	}
	// Links: every indexed dependency among the drawn nodes, with the
	// rolled-up members of each node standing for it.
	rep := func(i int32) (string, bool) {
		if id, ok := symToNode[i]; ok {
			return id, true
		}
		id, ok := symToNode[m.anchor[i]]
		return id, ok
	}
	for i := range m.deps {
		d := m.deps[i]
		from, okF := rep(d.from)
		to, okT := rep(d.to)
		if okF && okT {
			b.link(from, to, d)
		}
	}
	// The answer's own relations that the dependency graph does not carry:
	// a family member changes with the target; a caller the compiler
	// resolved but the graph links elsewhere still calls the change.
	change := map[string]bool{}
	for _, mb := range kept {
		if mb.role == RoleTarget || mb.role == RoleFamily {
			change[mb.node.ID] = true
		}
	}
	linked := func(id string) bool {
		for k := range b.links {
			if k[0] == id && change[k[1]] {
				return true
			}
		}
		return false
	}
	for _, mb := range kept {
		id := mb.node.ID
		switch mb.role {
		case RoleFamily:
			if b.links[[2]string{id, tn.ID}] == nil && b.links[[2]string{tn.ID, id}] == nil {
				b.links[[2]string{id, tn.ID}] = &Link{From: id, To: tn.ID, Weight: 1,
					Kinds: map[string]int{"same-change": 1}, Tiers: map[string]int{}}
			}
		case RoleCaller, RoleAccess:
			if !linked(id) {
				kind := "calls"
				if mb.role == RoleAccess {
					kind = "accesses"
				}
				b.links[[2]string{id, tn.ID}] = &Link{From: id, To: tn.ID, Weight: 1,
					Kinds: map[string]int{kind: 1}, Tiers: map[string]int{}}
			}
		}
	}
	g := &Graph{}
	b.finish(g)
	out.Nodes, out.Links = g.Nodes, g.Links
	for _, v := range asList(raw["related"]) {
		r, _ := v.(map[string]any)
		if r == nil {
			continue
		}
		h := Hit{Label: str(r["name"]), Type: "symbol", Kind: str(r["relation"]), Path: str(r["filePath"]), Line: num(r["line"])}
		if i, ok := m.locate(h.Path, h.Label, h.Line); ok {
			h.ID = "s:" + m.syms[i].ID
		}
		out.Related = append(out.Related, h)
	}
	sort.SliceStable(out.Sites, func(i, j int) bool {
		a, c := out.Sites[i], out.Sites[j]
		if rank[a.Role] != rank[c.Role] {
			return rank[a.Role] < rank[c.Role]
		}
		if a.Test != c.Test {
			return !a.Test
		}
		if a.Path != c.Path {
			return a.Path < c.Path
		}
		return a.Line < c.Line
	})
	return out, nil
}

func hasSite(sites []ImpactSite, nodeID string) bool {
	for _, s := range sites {
		if s.NodeID == nodeID {
			return true
		}
	}
	return false
}

// linkedToChange reports whether a node already has a drawn link into the
// target or a family member.
func asList(v any) []any { l, _ := v.([]any); return l }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
