package graphui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// Other repositories: linked checkouts, cache clones and the link
	// server. Their sites are listed, not drawn: the graph holds one
	// repository. WorkspaceScope says what was and was not covered.
	LinkedRepos    []LinkedRepo `json:"linkedRepos,omitempty"`
	LinkedSites    int          `json:"linkedSites"`
	WorkspaceScope string       `json:"workspaceScope,omitempty"`

	Counts map[string]int `json:"counts"` // nodes per role, before caps
	// GroupedByFile: edit sites and reached nodes are drawn one per file
	// (Node.Symbols counts the declarations each stands for).
	GroupedByFile bool `json:"groupedByFile,omitempty"`
	Files         int  `json:"files"` // distinct files holding an edit site
}

// LinkedRepo is one other repository's part of a change-impact answer.
type LinkedRepo struct {
	ID     string       `json:"id"`
	Path   string       `json:"path,omitempty"`
	Source string       `json:"source"` // checkout | clone | server
	Commit string       `json:"commit,omitempty"`
	Ref    string       `json:"ref,omitempty"`
	Pin    string       `json:"pin,omitempty"`
	State  string       `json:"state"`
	Note   string       `json:"note,omitempty"`
	Sites  []LinkedSite `json:"sites"`
}

// LinkedSite is one site in another repository.
type LinkedSite struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Text     string `json:"text,omitempty"`
	From     string `json:"from,omitempty"`
	Evidence string `json:"evidence"`
	Detail   string `json:"detail,omitempty"`
	Test     bool   `json:"test,omitempty"`
}

// linkedFrom reads the answer's linkedRepos (plain JSON values).
func linkedFrom(raw map[string]any) ([]LinkedRepo, int) {
	var repos []LinkedRepo
	total := 0
	for _, r := range asList(raw["linkedRepos"]) {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		lr := LinkedRepo{ID: str(m["id"]), Path: str(m["path"]), Source: str(m["source"]), Commit: str(m["commit"]),
			Ref: str(m["ref"]), Pin: str(m["pin"]), State: str(m["state"]), Note: str(m["note"]), Sites: []LinkedSite{}}
		for _, x := range asList(m["sites"]) {
			sm, _ := x.(map[string]any)
			if sm == nil {
				continue
			}
			t, _ := sm["test"].(bool)
			lr.Sites = append(lr.Sites, LinkedSite{File: str(sm["file"]), Line: num(sm["line"]), Text: str(sm["text"]),
				From: str(sm["from"]), Evidence: str(sm["evidence"]), Detail: str(sm["detail"]), Test: t})
		}
		total += len(lr.Sites)
		repos = append(repos, lr)
	}
	return repos, total
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
	v, err := m.impactNewBuild(id, raw, depth, includeTests)
	if err != nil {
		return nil, err
	}
	v.addChangeSet()
	v.addSites()
	v.addTransitive(v.out.Depth)
	kept := v.keep()
	// symNodes: every indexed symbol in the view and the node standing for it.
	symNodes := map[int32]string{}
	kept = v.groupByFile(kept, symNodes)
	if len(kept) > maxImpactNodes {
		v.out.Hidden = len(kept) - maxImpactNodes
		kept = kept[:maxImpactNodes]
	}
	v.draw(kept, symNodes)
	v.addRelated()
	impactSortSites(v.out.Sites)
	return v.out, nil
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
