package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/view"
)

// CrossRepo is the part of a component map that crosses the repository
// boundary: edges between this repository's components and those of its
// linked repositories and of repositories uploaded to the link server.
// docs/design/cross-repo-map.md.
type CrossRepo struct {
	Repos    []CrossRepoInfo `json:"repos"`
	Edges    []CrossEdge     `json:"edges"`
	Coverage string          `json:"coverage"`
	// Unlinked counts this repository's references into packages no linked
	// or uploaded repository declares: not drawn.
	UnlinkedRefs     int `json:"unlinkedRefs"`
	UnlinkedPackages int `json:"unlinkedPackages"`
}

// CrossRepoInfo is one other repository the map read (or could not).
type CrossRepoInfo struct {
	ID     string `json:"id"`
	Source string `json:"source"` // checkout | clone | server
	Commit string `json:"commit,omitempty"`
	Ref    string `json:"ref,omitempty"`
	State  string `json:"state"` // read | stale | unread
	Note   string `json:"note,omitempty"`
}

// CrossEnd is one end of a cross edge: a component of a repository ("" is
// this one).
type CrossEnd struct {
	Repo      string `json:"repo,omitempty"`
	Component string `json:"component"`
}

// CrossEdge is the references from one component to another across the
// boundary. Direction: "uses" (this repository uses the other) or
// "used by".
type CrossEdge struct {
	From      CrossEnd    `json:"from"`
	To        CrossEnd    `json:"to"`
	Direction string      `json:"direction"`
	Weight    int         `json:"weight"`
	Evidence  string      `json:"evidence"` // resolved | import-scoped
	Sites     []CrossSite `json:"sites,omitempty"`
}

// CrossSite is one reference behind a cross edge.
type CrossSite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Text   string `json:"text,omitempty"`
	Symbol string `json:"symbol"` // the referenced symbol's name
}

// Label is how the map prints an end: "grove:pkg/grove", or the bare
// component for this repository.
func (e CrossEnd) Label() string {
	if e.Repo == "" {
		return e.Component
	}
	return e.Repo + ":" + e.Component
}

type crossKey struct {
	from, to CrossEnd
	dir      string
}

type crossAcc struct {
	edges map[crossKey]*CrossEdge
	max   int
}

func (a *crossAcc) add(from, to CrossEnd, dir, key string, site CrossSite) {
	k := crossKey{from, to, dir}
	e := a.edges[k]
	if e == nil {
		e = &CrossEdge{From: from, To: to, Direction: dir, Evidence: "resolved"}
		a.edges[k] = e
	}
	if strings.HasPrefix(key, "py:") {
		e.Evidence = "import-scoped"
	}
	e.Weight++
	if a.max <= 0 || len(e.Sites) < a.max {
		e.Sites = append(e.Sites, site)
	}
}

func keySymbol(key string) string {
	if i := strings.IndexByte(key, '#'); i >= 0 {
		return key[i+1:]
	}
	return key
}

func keyPackage(key string) string {
	k := key
	if i := strings.IndexByte(k, '#'); i >= 0 {
		k = k[:i]
	}
	return k
}

// CrossRepoMap computes the cross-repository edges at a map depth. nil
// when no workspace or server is configured.
func (h *Handler) CrossRepoMap(ctx context.Context, depth, maxSites int, includeTests bool) (*CrossRepo, error) {
	if !h.workspaceConfigured() {
		return nil, nil
	}
	self, err := linkserver.Build(ctx, h.Root, "", "")
	if err != nil {
		return nil, fmt.Errorf("this repository's index: %w", err)
	}
	keep := func(file string) bool { return includeTests || !isTestFilePath(file) }
	selfDefs := map[string]linkserver.Def{}
	for _, d := range self.Defs {
		selfDefs[d.Key] = d
	}
	acc := &crossAcc{edges: map[crossKey]*CrossEdge{}, max: maxSites}
	out := &CrossRepo{Repos: []CrossRepoInfo{}, Edges: []CrossEdge{}}
	resolvedRef := map[int]bool{} // positions in self.Refs some linked repository declares
	var read []string

	if h.Workspace != nil {
		for _, root := range h.Workspace.Roots {
			info := CrossRepoInfo{ID: root.ID, Source: "checkout", State: "read"}
			if root.Cache {
				info.Source = "clone"
			}
			if why := root.Unusable(); why != "" {
				info.State, info.Note = "unread", why
				out.Repos = append(out.Repos, info)
				continue
			}
			stale, indexed := h.freshenLinked(ctx, root)
			if !indexed {
				info.State, info.Note = "unread", "no index there"
				out.Repos = append(out.Repos, info)
				continue
			}
			if stale != "" {
				info.State, info.Note = "stale", stale
			}
			b, err := linkserver.Build(ctx, root.Path, root.ID, "")
			if err != nil {
				info.State, info.Note = "unread", err.Error()
				out.Repos = append(out.Repos, info)
				continue
			}
			info.Commit, info.Ref = b.SHA, b.Ref
			out.Repos = append(out.Repos, info)
			read = append(read, root.ID)
			theirDefs := map[string]linkserver.Def{}
			for _, d := range b.Defs {
				theirDefs[d.Key] = d
			}
			// This repository uses theirs.
			for i, r := range self.Refs {
				d, ok := theirDefs[r.Key]
				if !ok {
					continue
				}
				resolvedRef[i] = true
				if !keep(r.File) {
					continue
				}
				acc.add(CrossEnd{Component: view.ComponentOf(r.File, depth)}, CrossEnd{Repo: root.ID, Component: view.ComponentOf(d.File, depth)},
					"uses", r.Key, CrossSite{File: r.File, Line: r.Line, Text: r.Text, Symbol: keySymbol(r.Key)})
			}
			// Theirs uses this one.
			for _, r := range b.Refs {
				d, ok := selfDefs[r.Key]
				if !ok || !keep(r.File) {
					continue
				}
				acc.add(CrossEnd{Repo: root.ID, Component: view.ComponentOf(r.File, depth)}, CrossEnd{Component: view.ComponentOf(d.File, depth)},
					"used by", r.Key, CrossSite{File: r.File, Line: r.Line, Text: r.Text, Symbol: keySymbol(r.Key)})
			}
		}
	}

	serverPart := ""
	if server := h.serverURL(); server != "" && len(selfDefs) > 0 {
		serverPart = h.crossFromServer(ctx, server, self, selfDefs, depth, keep, acc, out)
	}

	pkgs := map[string]bool{}
	for i, r := range self.Refs {
		if !resolvedRef[i] && keep(r.File) {
			out.UnlinkedRefs++
			pkgs[keyPackage(r.Key)] = true
		}
	}
	out.UnlinkedPackages = len(pkgs)

	for _, e := range acc.edges {
		out.Edges = append(out.Edges, *e)
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		a, b := out.Edges[i], out.Edges[j]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return a.From.Label()+a.To.Label() < b.From.Label()+b.To.Label()
	})

	var parts []string
	if len(read) > 0 {
		parts = append(parts, strings.Join(read, ", ")+" read")
	}
	for _, r := range out.Repos {
		if r.State == "stale" {
			parts = append(parts, r.ID+" answered from a STALE index ("+r.Note+")")
		}
	}
	for _, r := range out.Repos {
		if r.State == "unread" {
			parts = append(parts, fmt.Sprintf("NOT READ: %s (%s)", r.ID, r.Note))
		}
	}
	if serverPart != "" {
		parts = append(parts, serverPart)
	}
	if out.UnlinkedRefs > 0 {
		parts = append(parts, fmt.Sprintf("%d reference(s) to %d package(s) in repositories that are not linked (not drawn)", out.UnlinkedRefs, out.UnlinkedPackages))
	}
	parts = append(parts, "repositories neither linked nor uploaded are not covered")
	out.Coverage = strings.Join(parts, "; ")
	return out, nil
}

func (h *Handler) serverURL() string {
	if h.Cfg == nil {
		return ""
	}
	return h.Cfg.WorkspaceServer
}

// crossFromServer adds the uploaded repositories that use this one. Keys
// go in batches; the server answers each from the repositories the token
// may read.
func (h *Handler) crossFromServer(ctx context.Context, server string, self linkserver.Bundle, selfDefs map[string]linkserver.Def,
	depth int, keep func(string) bool, acc *crossAcc, out *CrossRepo) string {
	keys := make([]string, 0, len(selfDefs))
	for k := range selfDefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	exclude := []string{}
	for _, p := range self.Packages {
		exclude = append(exclude, p.Ecosystem+":"+p.Name)
	}
	if h.Workspace != nil {
		for _, r := range h.Workspace.Linked() {
			for _, p := range grove.ManifestPackages(r.Path) {
				exclude = append(exclude, p.Ecosystem+":"+p.Name)
			}
		}
	}
	seen := map[string]bool{}
	searched, unreadable, sites := 0, 0, 0
	const batch = 2000
	for i := 0; i < len(keys); i += batch {
		j := i + batch
		if j > len(keys) {
			j = len(keys)
		}
		resp, err := linkserver.AskConsumers(ctx, server, linkserver.ConsumersRequest{Keys: keys[i:j], ExcludePackages: exclude})
		if err != nil {
			return fmt.Sprintf("link server %s NOT REACHED (%v): uploaded repositories not covered", server, err)
		}
		if resp.Searched > searched {
			searched = resp.Searched
		}
		if resp.Unreadable > unreadable {
			unreadable = resp.Unreadable
		}
		for _, r := range resp.Repos {
			r.Repo, r.SHA, r.Ref = linkserver.CleanName(r.Repo, 256), linkserver.CleanName(r.SHA, 128), linkserver.CleanName(r.Ref, 256)
			id := r.Repo + "@" + shortCommit(r.SHA)
			if !seen[id] {
				seen[id] = true
				out.Repos = append(out.Repos, CrossRepoInfo{ID: id, Source: "server", Commit: r.SHA, Ref: r.Ref, State: "read", Note: uploadedOn(r.Created)})
			}
			for _, s := range r.Sites {
				d, ok := selfDefs[s.Key]
				if !ok || !keep(s.File) {
					continue
				}
				sites++
				file := linkserver.CleanName(s.File, 1024)
				acc.add(CrossEnd{Repo: id, Component: view.ComponentOf(file, depth)}, CrossEnd{Component: view.ComponentOf(d.File, depth)},
					"used by", s.Key, CrossSite{File: file, Line: s.Line, Text: linkserver.CleanLine(s.Text, linkserver.KeyLeaf(s.Key)), Symbol: keySymbol(s.Key)})
			}
		}
	}
	part := fmt.Sprintf("link server: %d site(s) from %d of %d uploaded repositories", sites, len(seen), searched)
	if unreadable > 0 {
		part += fmt.Sprintf(", %d NOT READABLE with this token", unreadable)
	}
	return part
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func uploadedOn(created string) string {
	if day, _, ok := strings.Cut(created, "T"); ok {
		return "uploaded " + day
	}
	return ""
}
