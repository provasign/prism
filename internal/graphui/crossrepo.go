package graphui

import (
	"context"
	"sort"
	"strings"
)

// CrossRepoFunc returns prism map's crossRepo answer (plain JSON values) at
// directory granularity: the edges between this repository's directories
// and other repositories' (linked checkouts, cache clones, the link
// server). nil when no workspace or server is configured.
type CrossRepoFunc func(ctx context.Context, includeTests bool) (map[string]any, error)

// maxRepoNodes caps the other repositories drawn; the rest are listed in
// one "more" node.
const maxRepoNodes = 20

// addCrossRepo draws other repositories on the root view: one node per
// repository, joined to the drawn directory each cross edge lands in.
func addCrossRepo(g *Graph, cr map[string]any) {
	if cr == nil {
		return
	}
	g.CrossRepoCoverage = str(cr["coverage"])
	drawn := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Type == "dir" {
			drawn[n.Path] = true
		}
	}
	// place: the drawn directory a component belongs to (itself, or the
	// nearest drawn ancestor in the tree view).
	place := func(comp string) string {
		for c := comp; c != "" && c != "." && c != "(root)"; {
			if drawn[c] {
				return "d:" + c
			}
			i := strings.LastIndexByte(c, '/')
			if i < 0 {
				break
			}
			c = c[:i]
		}
		return ""
	}
	repos := repoNodes(cr)
	type lk struct{ from, to string }
	links := map[lk]*Link{}
	unplaced := 0
	for _, e := range asList(cr["edges"]) {
		m, _ := e.(map[string]any)
		if m == nil {
			continue
		}
		from, _ := m["from"].(map[string]any)
		to, _ := m["to"].(map[string]any)
		dir := str(m["direction"])
		var repo, here, otherComp string
		if dir == "uses" {
			repo, here, otherComp = str(to["repo"]), str(from["component"]), str(to["component"])
		} else {
			repo, here, otherComp = str(from["repo"]), str(to["component"]), str(from["component"])
		}
		hereID := place(here)
		ri := repos[repo]
		if hereID == "" || ri == nil {
			unplaced += num(m["weight"])
			continue
		}
		key := lk{hereID, ri.node.ID}
		if dir != "uses" {
			key = lk{ri.node.ID, hereID}
		}
		l := links[key]
		if l == nil {
			l = &Link{From: key.from, To: key.to, Kinds: map[string]int{}, Tiers: map[string]int{}}
			links[key] = l
		}
		w := num(m["weight"])
		l.Weight += w
		l.Kinds[dir] += w
		l.Tiers[str(m["evidence"])] += w
		ri.weight += w
		for _, s := range asList(m["sites"]) {
			sm, _ := s.(map[string]any)
			if sm == nil || len(l.Sites) >= 8 {
				continue
			}
			site := Site{Kind: dir, Tier: str(m["evidence"]), ToSymbol: str(sm["symbol"])}
			if dir == "uses" {
				site.FromFile, site.FromLine, site.FromSymbol = str(sm["file"]), num(sm["line"]), here
				site.ToFile = repo + ":" + otherComp
			} else {
				site.FromFile, site.FromLine, site.FromSymbol = repo+":"+str(sm["file"]), num(sm["line"]), otherComp
				site.ToFile = here
			}
			l.Sites = append(l.Sites, site)
		}
	}
	list := make([]*repoInfo, 0, len(repos))
	for _, r := range repos {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].weight != list[j].weight {
			return list[i].weight > list[j].weight
		}
		return list[i].node.Label < list[j].node.Label
	})
	shown := map[string]bool{}
	var more []string
	for i, r := range list {
		if i < maxRepoNodes {
			shown[r.node.ID] = true
			g.Nodes = append(g.Nodes, r.node)
		} else {
			more = append(more, r.node.Label)
		}
	}
	if len(more) > 0 {
		g.Nodes = append(g.Nodes, Node{ID: "r:+more", Label: "+" + itoa(len(more)) + " more repositories", Type: "repo", Kind: "more",
			Detail: strings.Join(more, ", ")})
	}
	for _, l := range links {
		if shown[l.From] || shown[l.To] {
			g.Links = append(g.Links, *l)
		}
	}
	sort.SliceStable(g.Links, func(i, j int) bool { return g.Links[i].Weight > g.Links[j].Weight })
	countRepoLinks(g)
	g.CrossRepoUnplaced = unplaced
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

type repoInfo struct {
	node   Node
	weight int
}

// repoNodes makes one node per other repository the answer names.
func repoNodes(cr map[string]any) map[string]*repoInfo {
	repos := map[string]*repoInfo{}
	for _, r := range asList(cr["repos"]) {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		id := str(m["id"])
		detail := []string{}
		if c := str(m["commit"]); c != "" {
			short := c
			if len(short) > 8 {
				short = short[:8]
			}
			if ref := str(m["ref"]); ref != "" {
				short += " (" + ref + ")"
			}
			detail = append(detail, short)
		}
		if st := str(m["state"]); st != "" && st != "read" {
			detail = append(detail, strings.ToUpper(st))
		}
		if note := str(m["note"]); note != "" {
			detail = append(detail, note)
		}
		repos[id] = &repoInfo{node: Node{ID: "r:" + id, Label: id, Type: "repo", Kind: str(m["source"]), Detail: strings.Join(detail, " · ")}}
	}
	return repos
}

// countRepoLinks adds the cross links to the in/out counts of the nodes
// they join, as for every other node.
func countRepoLinks(g *Graph) {
	idx := map[string]int{}
	for i, n := range g.Nodes {
		idx[n.ID] = i
	}
	for _, l := range g.Links {
		if !strings.HasPrefix(l.From, "r:") && !strings.HasPrefix(l.To, "r:") {
			continue
		}
		if i, ok := idx[l.From]; ok {
			g.Nodes[i].Out += l.Weight
		}
		if i, ok := idx[l.To]; ok {
			g.Nodes[i].In += l.Weight
		}
	}
}
