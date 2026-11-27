package graphui

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// impactMember is one node of an impact view with its strongest role.
type impactMember struct {
	node  Node
	role  string
	sym   int32 // -1 when the entry maps to no indexed symbol
	order int
}

// impactRoleRank orders roles from the change outward; the lower rank wins
// when a node is reached in several roles.
var impactRoleRank = map[string]int{RoleTarget: 0, RoleFamily: 1, RoleCaller: 2, RoleAccess: 2, RoleTransitive: 3}

// impactBuild carries the state shared while Model.Impact assembles a view.
type impactBuild struct {
	m       *Model
	raw     map[string]any
	out     *Impact
	tests   bool
	ti      int32
	tn      Node
	members map[string]*impactMember
	order   []string
}

// impactNewBuild validates the request and copies the answer's verdict.
func (m *Model) impactNewBuild(id string, raw map[string]any, depth int, includeTests bool) (*impactBuild, error) {
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
	out.LinkedRepos, out.LinkedSites = linkedFrom(raw)
	out.WorkspaceScope = str(raw["workspaceScope"])
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
	return &impactBuild{m: m, raw: raw, out: out, tests: tests, ti: ti, members: map[string]*impactMember{}}, nil
}

func (v *impactBuild) add(n Node, role string, sym int32) *impactMember {
	if cur, ok := v.members[n.ID]; ok {
		if impactRoleRank[role] < impactRoleRank[cur.role] {
			cur.role = role
		}
		return cur
	}
	mb := &impactMember{node: n, role: role, sym: sym, order: len(v.order)}
	v.members[n.ID] = mb
	v.order = append(v.order, n.ID)
	return mb
}

func (v *impactBuild) nodeFor(e entry) (Node, int32) {
	m := v.m
	if i, ok := m.locate(e.Path, firstNonEmpty(e.QName, e.Name), e.Line); ok {
		return m.symNode(i), i
	}
	// Reported but not indexed as a symbol: keep it, unlinked to the
	// model, so nothing in the answer is dropped.
	return Node{ID: fmt.Sprintf("x:%s:%d", e.Path, e.Line), Label: firstNonEmpty(e.QName, e.Name, e.Path),
		Type: "symbol", Kind: e.Kind, Path: e.Path, Line: e.Line, Signature: e.Signature,
		Test: m.fileTest[e.Path]}, -1
}

// addChangeSet adds the target, its declarations and the family.
func (v *impactBuild) addChangeSet() {
	tn := v.m.symNode(v.ti)
	tn.Focus = true
	v.tn = tn
	v.out.Target = tn
	v.add(tn, RoleTarget, v.ti)
	for _, e := range append(entries(v.raw, "declarations"), entries(v.raw, "declaringTypes")...) {
		n, i := v.nodeFor(e)
		v.add(n, RoleTarget, i)
	}
	for _, key := range []string{"family", "supers"} {
		for _, e := range entries(v.raw, key) {
			n, i := v.nodeFor(e)
			v.add(n, RoleFamily, i)
		}
	}
}

// impactCallEvidence returns the line and text of the first evidence item.
func impactCallEvidence(v any) (line int, text string) {
	for _, ev := range asList(v) {
		em, _ := ev.(map[string]any)
		if em != nil {
			return num(em["line"]), str(em["text"])
		}
	}
	return 0, ""
}

// addSites adds the edit sites: callers (with their call lines), member
// accesses, family declarations and the target declaration.
func (v *impactBuild) addSites() {
	m, out, tn := v.m, v.out, v.tn
	callerList := asList(v.raw["callers"])
	for _, cv := range callerList {
		c, _ := cv.(map[string]any)
		if c == nil {
			continue
		}
		e := entry{Path: str(c["filePath"]), Name: str(c["name"]), QName: str(c["qualifiedName"]),
			Kind: str(c["kind"]), Signature: str(c["signature"]), Line: num(c["line"])}
		n, i := v.nodeFor(e)
		mb := v.add(n, RoleCaller, i)
		line, text := impactCallEvidence(c["evidence"])
		if line == 0 {
			line = e.Line
		}
		out.Sites = append(out.Sites, ImpactSite{Path: e.Path, Line: line, Symbol: mb.node.Label,
			NodeID: mb.node.ID, Role: RoleCaller, Access: "call", Text: text, Test: m.fileTest[e.Path]})
	}
	for _, av := range asList(v.raw["accesses"]) {
		a, _ := av.(map[string]any)
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
			mb := v.add(m.symNode(a), RoleAccess, a)
			site.NodeID, site.Symbol = mb.node.ID, mb.node.Label
		} else if i, ok := m.enclosing(path, line); ok {
			a := m.anchor[i]
			mb := v.add(m.symNode(a), RoleAccess, a)
			site.NodeID, site.Symbol = mb.node.ID, mb.node.Label
		}
		out.Sites = append(out.Sites, site)
	}
	// Declarations of family members are edit sites too.
	for _, id := range v.order {
		mb := v.members[id]
		if mb.role == RoleFamily || (mb.role == RoleTarget && id != tn.ID) {
			out.Sites = append(out.Sites, ImpactSite{Path: mb.node.Path, Line: mb.node.Line,
				Symbol: mb.node.Label, NodeID: id, Role: mb.role, Access: "decl", Test: mb.node.Test})
		}
	}
	if len(callerList) == 0 && len(asList(v.raw["accesses"])) == 0 {
		out.Sites = append(out.Sites, ImpactSite{Path: tn.Path, Line: tn.Line, Symbol: tn.Label,
			NodeID: tn.ID, Role: RoleTarget, Access: "decl", Test: tn.Test})
	} else if !hasSite(out.Sites, tn.ID) {
		out.Sites = append([]ImpactSite{{Path: tn.Path, Line: tn.Line, Symbol: tn.Label,
			NodeID: tn.ID, Role: RoleTarget, Access: "decl", Test: tn.Test}}, out.Sites...)
	}
}

// addTransitive walks call edges upstream from the direct sites.
func (v *impactBuild) addTransitive(depth int) {
	m := v.m
	frontier := []int32{}
	inSet := map[int32]bool{}
	for _, id := range v.order {
		if mb := v.members[id]; mb.sym >= 0 {
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
				v.add(m.symNode(a), RoleTransitive, a)
				next = append(next, a)
			}
		}
		frontier = next
	}
}

// keep counts members per role and filters test nodes. Edit sites rank
// before reached-only nodes.
func (v *impactBuild) keep() []*impactMember {
	out := v.out
	files := map[string]bool{}
	for _, s := range out.Sites {
		files[s.Path] = true
	}
	out.Files = len(files)
	var kept []*impactMember
	for _, id := range v.order {
		mb := v.members[id]
		out.Counts[mb.role]++
		if mb.node.Test && !v.tests && id != v.tn.ID {
			out.TestsHidden++
			continue
		}
		kept = append(kept, mb)
	}
	sort.SliceStable(kept, func(i, j int) bool { return impactRoleRank[kept[i].role] < impactRoleRank[kept[j].role] })
	return kept
}

// groupByFile draws edit sites and reached nodes one node per file when
// there are too many site declarations; symNodes records the group node
// standing for each indexed symbol.
func (v *impactBuild) groupByFile(kept []*impactMember, symNodes map[int32]string) []*impactMember {
	siteDecls := 0
	for _, mb := range kept {
		if mb.role == RoleCaller || mb.role == RoleAccess {
			siteDecls++
		}
	}
	if siteDecls <= maxSiteNodes {
		return kept
	}
	v.out.GroupedByFile = true
	remap := map[string]string{}
	groups := map[string]*impactMember{}
	var grouped []*impactMember
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
			g = &impactMember{node: Node{ID: gid, Label: path.Base(mb.node.Path), Type: "file", Kind: v.m.fileLanguage(mb.node.Path),
				Path: mb.node.Path, Test: mb.node.Test, Files: 1}, role: mb.role, sym: -1}
			groups[gid] = g
			grouped = append(grouped, g)
		}
		g.node.Symbols++
		if impactRoleRank[mb.role] < impactRoleRank[g.role] {
			g.role = mb.role
		}
		remap[mb.node.ID] = gid
		if mb.sym >= 0 {
			symNodes[mb.sym] = gid
		}
	}
	for i := range v.out.Sites {
		if gid, ok := remap[v.out.Sites[i].NodeID]; ok {
			v.out.Sites[i].NodeID = gid
		}
	}
	return grouped
}

// draw builds the nodes and links of the kept members.
func (v *impactBuild) draw(kept []*impactMember, symNodes map[int32]string) {
	m, tn := v.m, v.tn
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
	v.out.Nodes, v.out.Links = g.Nodes, g.Links
}

// addRelated copies the answer's related symbols, linked to the index.
func (v *impactBuild) addRelated() {
	for _, rv := range asList(v.raw["related"]) {
		r, _ := rv.(map[string]any)
		if r == nil {
			continue
		}
		h := Hit{Label: str(r["name"]), Type: "symbol", Kind: str(r["relation"]), Path: str(r["filePath"]), Line: num(r["line"])}
		if i, ok := v.m.locate(h.Path, h.Label, h.Line); ok {
			h.ID = "s:" + v.m.syms[i].ID
		}
		v.out.Related = append(v.out.Related, h)
	}
}

// impactSortSites orders sites by role, production before tests, then path
// and line.
func impactSortSites(sites []ImpactSite) {
	sort.SliceStable(sites, func(i, j int) bool {
		a, c := sites[i], sites[j]
		if impactRoleRank[a.Role] != impactRoleRank[c.Role] {
			return impactRoleRank[a.Role] < impactRoleRank[c.Role]
		}
		if a.Test != c.Test {
			return !a.Test
		}
		if a.Path != c.Path {
			return a.Path < c.Path
		}
		return a.Line < c.Line
	})
}
