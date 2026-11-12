// Package graphui serves the interactive graph explorer behind `prism graph`:
// a local web page that renders the indexed code graph at one level at a
// time (directories -> files -> symbols -> one symbol's neighbourhood) and
// lets the user drill down, search, and inspect the evidence behind an edge.
//
// The model is a read-only projection of one graph snapshot. Every view is
// derived from the same symbols and edges the other Prism commands answer
// from; nothing here invents or re-resolves an edge.
package graphui

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/view"
)

// Limits on one rendered view. A cap is always reported in the response
// (Graph.Hidden / Graph.HiddenOutside), never applied silently.
const (
	maxInsideNodes  = 300
	maxOutsideNodes = 24
	maxSitesPerLink = 12
	maxMembers      = 80
	maxSourceBytes  = 60_000
)

// hierarchyKinds are edge kinds that describe containment or coverage, not
// a dependency between two symbols. They are never drawn as dependencies.
var hierarchyKinds = map[string]bool{"contains": true, "defines": true, "tests": true}

// Node is one drawn vertex: a directory, a file or a symbol.
type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Type      string `json:"type"` // dir | file | symbol
	Kind      string `json:"kind,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Files     int    `json:"files,omitempty"`
	Symbols   int    `json:"symbols,omitempty"`
	Signature string `json:"signature,omitempty"`
	Exported  bool   `json:"exported,omitempty"`
	Test      bool   `json:"test,omitempty"`
	// Outside marks context drawn around the scope: a neighbour of the
	// scope's contents that lives elsewhere in the repository.
	Outside bool `json:"outside,omitempty"`
	// Focus marks the symbol a symbol view is centred on.
	Focus bool `json:"focus,omitempty"`
	// Member marks a symbol declared inside the focus symbol.
	Member bool `json:"member,omitempty"`
	// Role is the node's place in a change-impact view (impact.go).
	Role string `json:"role,omitempty"`
	In   int    `json:"in"`
	Out  int    `json:"out"`
}

// Site is one primitive edge behind a drawn link.
type Site struct {
	FromID     string `json:"fromId"`
	FromSymbol string `json:"fromSymbol"`
	FromFile   string `json:"fromFile"`
	FromLine   int    `json:"fromLine"`
	ToID       string `json:"toId"`
	ToSymbol   string `json:"toSymbol"`
	ToFile     string `json:"toFile"`
	ToLine     int    `json:"toLine"`
	Kind       string `json:"kind"`
	Tier       string `json:"tier"`
}

// Link is one drawn edge: every primitive edge from one node to another.
type Link struct {
	From   string         `json:"from"`
	To     string         `json:"to"`
	Weight int            `json:"weight"`
	Kinds  map[string]int `json:"kinds"`
	Tiers  map[string]int `json:"tiers,omitempty"`
	Sites  []Site         `json:"sites,omitempty"` // capped; Weight is the full count
}

// Crumb is one step of the path from the repository root to the scope.
type Crumb struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Graph is one rendered view.
type Graph struct {
	Scope  Node    `json:"scope"`
	Crumbs []Crumb `json:"crumbs"`
	Nodes  []Node  `json:"nodes"`
	Links  []Link  `json:"links"`
	// Hidden / HiddenOutside count nodes left out by the size caps.
	Hidden        int  `json:"hidden"`
	HiddenOutside int  `json:"hiddenOutside"`
	IncludeTests  bool `json:"includeTests"`
	// TestFilesExcluded counts test files inside the scope that were left
	// out because tests are off.
	TestFilesExcluded int `json:"testFilesExcluded"`
}

// Options selects how a view is built.
type Options struct {
	// IncludeTests keeps test files. A scope that is itself test code
	// always includes them.
	IncludeTests bool
	// Flat lists every directory that holds files instead of one level of
	// the directory tree. It applies to the root scope only.
	Flat bool
}

// Hit is one search result.
type Hit struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Kind  string `json:"kind,omitempty"`
	Path  string `json:"path,omitempty"`
	Line  int    `json:"line,omitempty"`
	Test  bool   `json:"test,omitempty"`
}

// Source is the declaration text of one symbol.
type Source struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Signature string `json:"signature,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Meta summarises the snapshot.
type Meta struct {
	Root      string         `json:"root"`
	Name      string         `json:"name"`
	Files     int            `json:"files"`
	Dirs      int            `json:"dirs"`
	Symbols   int            `json:"symbols"`
	Edges     int            `json:"edges"`
	TestFiles int            `json:"testFiles"`
	Kinds     map[string]int `json:"kinds"`
	EdgeKinds map[string]int `json:"edgeKinds"`
}

type dep struct {
	from, to int32
	kind     string
	tier     string
}

// Model is an immutable, indexed projection of one graph snapshot.
type Model struct {
	root string
	syms []grove.SymbolRecord
	low  []string // lower-cased display name per symbol, for search

	byID     map[string]int32
	byStable map[string]int32 // ID without its blob suffix; survives a re-index

	deps   []dep
	out    [][]int32 // symbol -> indices into deps
	in     [][]int32
	parent []int32 // containing symbol, -1 if none
	kids   [][]int32
	anchor []int32 // nearest drawn ancestor (fields and locals roll up)

	files    []string // sorted
	fileSyms map[string][]int32
	fileTest map[string]bool

	dirFiles  map[string]int      // files directly in a directory
	dirKids   map[string][]string // child directories, sorted
	edgeKinds map[string]int
}

// BuildModel indexes a snapshot. Document symbols (indexed prose) are left
// out, as in `prism map`: they are search food, not structure.
func BuildModel(root string, symbols []grove.SymbolRecord, edges []grove.Edge) *Model {
	m := &Model{
		root:      root,
		byID:      map[string]int32{},
		byStable:  map[string]int32{},
		fileSyms:  map[string][]int32{},
		fileTest:  map[string]bool{},
		dirFiles:  map[string]int{},
		dirKids:   map[string][]string{},
		edgeKinds: map[string]int{},
	}
	for i := range symbols {
		s := symbols[i]
		if s.Kind == "document" || s.Kind == "file" || s.FilePath == "" {
			continue
		}
		s.FilePath = filepath.ToSlash(s.FilePath)
		m.syms = append(m.syms, s)
	}
	sort.SliceStable(m.syms, func(i, j int) bool {
		a, b := &m.syms[i], &m.syms[j]
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		if a.Span.Start != b.Span.Start {
			return a.Span.Start < b.Span.Start
		}
		return a.ID < b.ID
	})
	n := len(m.syms)
	m.low = make([]string, n)
	m.out = make([][]int32, n)
	m.in = make([][]int32, n)
	m.kids = make([][]int32, n)
	m.parent = make([]int32, n)
	m.anchor = make([]int32, n)
	for i := range m.syms {
		s := &m.syms[i]
		m.byID[s.ID] = int32(i)
		if st := stableID(s.ID); st != s.ID {
			if _, dup := m.byStable[st]; !dup {
				m.byStable[st] = int32(i)
			}
		}
		m.low[i] = strings.ToLower(symLabel(s))
		m.parent[i] = -1
		m.fileSyms[s.FilePath] = append(m.fileSyms[s.FilePath], int32(i))
	}
	for f := range m.fileSyms {
		m.files = append(m.files, f)
		m.fileTest[f] = view.IsTestPath(f)
	}
	sort.Strings(m.files)
	m.buildDirs()

	for _, e := range edges {
		from, okF := m.byID[e.From]
		to, okT := m.byID[e.To]
		if !okF || !okT || from == to {
			continue
		}
		if e.Type == "contains" {
			if m.parent[to] < 0 {
				m.parent[to] = from
				m.kids[from] = append(m.kids[from], to)
			}
			continue
		}
		if hierarchyKinds[e.Type] {
			continue
		}
		m.deps = append(m.deps, dep{from: from, to: to, kind: e.Type, tier: view.TierOf(e.Source)})
	}
	// Symbols are sorted by file and line, so index order is site order.
	sort.Slice(m.deps, func(i, j int) bool {
		a, b := m.deps[i], m.deps[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.kind < b.kind
	})
	for i, d := range m.deps {
		m.out[d.from] = append(m.out[d.from], int32(i))
		m.in[d.to] = append(m.in[d.to], int32(i))
		m.edgeKinds[d.kind]++
	}
	for i := range m.syms {
		m.anchor[i] = m.resolveAnchor(int32(i))
	}
	return m
}

// resolveAnchor returns the symbol a file view draws for sym: itself, or —
// for a field, anything declared inside a function body, or anything whose
// container is itself rolled up — the nearest enclosing symbol that is
// drawn. A struct's fields and a function's local types would otherwise
// bury the call structure of the file.
func (m *Model) resolveAnchor(i int32) int32 {
	return m.anchorAt(i, 0)
}

func (m *Model) anchorAt(i int32, depth int) int32 {
	p := m.parent[i]
	if p < 0 || depth > 32 {
		return i
	}
	if pa := m.anchorAt(p, depth+1); pa != p {
		return pa
	}
	if m.syms[i].Kind == "field" || isCallable(m.syms[p].Kind) {
		return p
	}
	return i
}

func isCallable(kind string) bool {
	switch kind {
	case "function", "method", "constructor":
		return true
	}
	return false
}

func (m *Model) buildDirs() {
	seen := map[string]bool{"": true}
	for _, f := range m.files {
		d := dirOf(f)
		m.dirFiles[d]++
		for d != "" && !seen[d] {
			seen[d] = true
			p := dirOf(d)
			m.dirKids[p] = append(m.dirKids[p], d)
			d = p
		}
	}
	for d := range m.dirKids {
		sort.Strings(m.dirKids[d])
	}
}

// collapse follows a chain of directories that hold nothing but one child
// directory, so `internal` -> `internal/app` is one step, not two.
func (m *Model) collapse(d string) string {
	for m.dirFiles[d] == 0 && len(m.dirKids[d]) == 1 {
		d = m.dirKids[d][0]
	}
	return d
}

// dirOf returns the directory of a slash path; "" is the repository root.
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// under reports whether slash path p lies inside directory d ("" = root).
func under(p, d string) bool {
	return d == "" || strings.HasPrefix(p, d+"/")
}

func stableID(id string) string {
	if i := strings.LastIndexByte(id, '@'); i > 0 {
		return id[:i]
	}
	return id
}

func symLabel(s *grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

func (m *Model) symNode(i int32) Node {
	s := &m.syms[i]
	return Node{
		ID: "s:" + s.ID, Label: symLabel(s), Type: "symbol", Kind: s.Kind,
		Path: s.FilePath, Line: s.Span.Start, EndLine: s.Span.End,
		Signature: s.Signature, Exported: s.Exports, Test: m.fileTest[s.FilePath],
	}
}

func (m *Model) lookupSymbol(id string) (int32, bool) {
	if i, ok := m.byID[id]; ok {
		return i, true
	}
	i, ok := m.byStable[stableID(id)]
	return i, ok
}

// Meta summarises the snapshot.
func (m *Model) Meta() Meta {
	kinds := map[string]int{}
	for i := range m.syms {
		kinds[m.syms[i].Kind]++
	}
	tests := 0
	for _, t := range m.fileTest {
		if t {
			tests++
		}
	}
	dirs := 0
	for d, n := range m.dirFiles {
		if n > 0 && d != "" {
			dirs++
		}
	}
	return Meta{
		Root: m.root, Name: filepath.Base(m.root), Files: len(m.files), Dirs: dirs,
		Symbols: len(m.syms), Edges: len(m.deps), TestFiles: tests,
		Kinds: kinds, EdgeKinds: m.edgeKinds,
	}
}

// splitScope parses a node id into its type prefix and key.
func splitScope(id string) (typ, key string, err error) {
	if id == "" || id == "d:" {
		return "d", "", nil
	}
	if len(id) > 2 && id[1] == ':' {
		switch id[0] {
		case 'd', 'f', 's':
			return id[:1], id[2:], nil
		}
	}
	return "", "", fmt.Errorf("unknown scope %q", id)
}

// Graph renders the view for a scope id: "" (repository root), "d:<dir>",
// "f:<file>" or "s:<symbol id>".
func (m *Model) Graph(scope string, o Options) (*Graph, error) {
	typ, key, err := splitScope(scope)
	if err != nil {
		return nil, err
	}
	switch typ {
	case "f":
		key = path.Clean(filepath.ToSlash(key))
		if _, ok := m.fileSyms[key]; !ok {
			return nil, fmt.Errorf("file %q is not in the index", key)
		}
		return m.fileGraph(key, o), nil
	case "s":
		i, ok := m.lookupSymbol(key)
		if !ok {
			return nil, fmt.Errorf("symbol %q is not in the index", key)
		}
		return m.symbolGraph(i, o), nil
	}
	key = strings.Trim(path.Clean(filepath.ToSlash(key)), "/")
	if key == "." {
		key = ""
	}
	if key != "" && m.dirFiles[key] == 0 && len(m.dirKids[key]) == 0 {
		return nil, fmt.Errorf("directory %q is not in the index", key)
	}
	return m.dirGraph(key, o), nil
}

// crumbs returns the path from the root to a directory, file or symbol.
func (m *Model) crumbs(dir, file string, sym int32) []Crumb {
	out := []Crumb{{ID: "", Label: filepath.Base(m.root)}}
	if dir != "" {
		acc := ""
		for _, seg := range strings.Split(dir, "/") {
			if acc == "" {
				acc = seg
			} else {
				acc += "/" + seg
			}
			out = append(out, Crumb{ID: "d:" + acc, Label: seg})
		}
	}
	if file != "" {
		out = append(out, Crumb{ID: "f:" + file, Label: path.Base(file)})
	}
	if sym >= 0 {
		out = append(out, Crumb{ID: "s:" + m.syms[sym].ID, Label: symLabel(&m.syms[sym])})
	}
	return out
}

// builder accumulates the nodes and links of one view.
type builder struct {
	m     *Model
	nodes map[string]*Node
	links map[[2]string]*Link
	order []string
}

func newBuilder(m *Model) *builder {
	return &builder{m: m, nodes: map[string]*Node{}, links: map[[2]string]*Link{}}
}

func (b *builder) node(n Node) *Node {
	if cur, ok := b.nodes[n.ID]; ok {
		return cur
	}
	nn := n
	b.nodes[n.ID] = &nn
	b.order = append(b.order, n.ID)
	return &nn
}

func (b *builder) link(from, to string, d dep) {
	if from == to {
		return
	}
	k := [2]string{from, to}
	l := b.links[k]
	if l == nil {
		l = &Link{From: from, To: to, Kinds: map[string]int{}, Tiers: map[string]int{}}
		b.links[k] = l
	}
	l.Weight++
	l.Kinds[d.kind]++
	l.Tiers[d.tier]++
	if len(l.Sites) < maxSitesPerLink {
		f, t := &b.m.syms[d.from], &b.m.syms[d.to]
		l.Sites = append(l.Sites, Site{
			FromID: "s:" + f.ID, FromSymbol: symLabel(f), FromFile: f.FilePath, FromLine: f.Span.Start,
			ToID: "s:" + t.ID, ToSymbol: symLabel(t), ToFile: t.FilePath, ToLine: t.Span.Start,
			Kind: d.kind, Tier: d.tier,
		})
	}
}

// finish applies the size caps, drops links to removed nodes, fills the
// per-node in/out weights and returns deterministically ordered slices.
func (b *builder) finish(g *Graph) {
	weight := map[string]int{}
	for k, l := range b.links {
		weight[k[0]] += l.Weight
		weight[k[1]] += l.Weight
	}
	var inside, outside []*Node
	for _, id := range b.order {
		n := b.nodes[id]
		switch {
		case n.Outside && weight[id] == 0:
			// Context with no link into the scope is not context.
		case n.Outside:
			outside = append(outside, n)
		default:
			inside = append(inside, n)
		}
	}
	rank := func(list []*Node) {
		sort.SliceStable(list, func(i, j int) bool {
			a, c := list[i], list[j]
			if a.Focus != c.Focus {
				return a.Focus
			}
			if weight[a.ID] != weight[c.ID] {
				return weight[a.ID] > weight[c.ID]
			}
			if a.Symbols != c.Symbols {
				return a.Symbols > c.Symbols
			}
			return a.ID < c.ID
		})
	}
	rank(inside)
	rank(outside)
	if len(inside) > maxInsideNodes {
		g.Hidden += len(inside) - maxInsideNodes
		inside = inside[:maxInsideNodes]
	}
	if len(outside) > maxOutsideNodes {
		g.HiddenOutside = len(outside) - maxOutsideNodes
		outside = outside[:maxOutsideNodes]
	}
	keep := map[string]*Node{}
	for _, n := range inside {
		keep[n.ID] = n
	}
	for _, n := range outside {
		keep[n.ID] = n
	}
	g.Links = make([]Link, 0, len(b.links))
	for k, l := range b.links {
		from, to := keep[k[0]], keep[k[1]]
		if from == nil || to == nil {
			continue
		}
		from.Out += l.Weight
		to.In += l.Weight
		g.Links = append(g.Links, *l)
	}
	sort.Slice(g.Links, func(i, j int) bool {
		a, c := g.Links[i], g.Links[j]
		if a.Weight != c.Weight {
			return a.Weight > c.Weight
		}
		if a.From != c.From {
			return a.From < c.From
		}
		return a.To < c.To
	})
	g.Nodes = make([]Node, 0, len(keep))
	for _, n := range inside {
		g.Nodes = append(g.Nodes, *n)
	}
	for _, n := range outside {
		// An outside node that lost every link to a capped inside node
		// would float unattached; keep only connected context.
		if n.In+n.Out > 0 {
			g.Nodes = append(g.Nodes, *n)
		} else {
			g.HiddenOutside++
		}
	}
}

// dirGraph renders one directory: its child directories and files as
// nodes, the dependencies between them, and — as context — the rest of the
// repository grouped at the level where it branches off from this scope.
func (m *Model) dirGraph(dir string, o Options) *Graph {
	tests := o.IncludeTests || (dir != "" && view.IsTestPath(dir+"/x"))
	flat := o.Flat && dir == ""
	g := &Graph{IncludeTests: tests, Crumbs: m.crumbs(dir, "", -1)}
	g.Scope = Node{ID: scopeDirID(dir), Label: dirLabel(m, dir), Type: "dir", Path: dir}
	b := newBuilder(m)

	// rep maps a file to the node that stands for it in this view.
	rep := func(f string) Node {
		fd := dirOf(f)
		if under(f, dir) {
			if fd == dir {
				return Node{ID: "f:" + f, Label: path.Base(f), Type: "file", Path: f}
			}
			var child string
			if flat {
				child = fd
			} else {
				rest := strings.TrimPrefix(fd, dir)
				rest = strings.TrimPrefix(rest, "/")
				seg, _, _ := strings.Cut(rest, "/")
				child = seg
				if dir != "" {
					child = dir + "/" + seg
				}
				child = m.collapse(child)
				if !under(f, child) { // collapse never leaves the file's own chain
					child = fd
				}
			}
			label := strings.TrimPrefix(strings.TrimPrefix(child, dir), "/")
			return Node{ID: "d:" + child, Label: label, Type: "dir", Path: child}
		}
		// Outside the scope: group at the first directory below the
		// deepest ancestor this file shares with the scope.
		common := dir
		for !under(f, common) {
			common = dirOf(common)
		}
		if fd == common {
			return Node{ID: "f:" + f, Label: f, Type: "file", Path: f, Outside: true}
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(fd, common), "/")
		seg, _, _ := strings.Cut(rest, "/")
		sib := seg
		if common != "" {
			sib = common + "/" + seg
		}
		return Node{ID: "d:" + sib, Label: sib, Type: "dir", Path: sib, Outside: true}
	}

	fileRep := make(map[string]string, len(m.files))
	for _, f := range m.files {
		if m.fileTest[f] && !tests {
			if under(f, dir) {
				g.TestFilesExcluded++
			}
			continue
		}
		n := b.node(rep(f))
		fileRep[f] = n.ID
		n.Files++
		n.Symbols += len(m.fileSyms[f])
		if n.Type == "file" {
			n.Test = m.fileTest[f]
			n.Kind = m.fileLanguage(f)
		}
		if !n.Outside {
			g.Scope.Files++
			g.Scope.Symbols += len(m.fileSyms[f])
		}
	}
	for _, d := range m.deps {
		from, okF := fileRep[m.syms[d.from].FilePath]
		to, okT := fileRep[m.syms[d.to].FilePath]
		if !okF || !okT || from == to {
			continue
		}
		if b.nodes[from].Outside && b.nodes[to].Outside {
			continue
		}
		b.link(from, to, d)
	}
	b.finish(g)
	return g
}

func scopeDirID(dir string) string {
	if dir == "" {
		return ""
	}
	return "d:" + dir
}

func dirLabel(m *Model, dir string) string {
	if dir == "" {
		return filepath.Base(m.root)
	}
	return dir
}

func (m *Model) fileLanguage(f string) string {
	for _, i := range m.fileSyms[f] {
		if l := m.syms[i].Language; l != "" {
			return l
		}
	}
	return ""
}

// fileGraph renders one file: its symbols as nodes (fields and locals
// rolled into their container), the dependencies among them, and the other
// files they depend on or are used by.
func (m *Model) fileGraph(file string, o Options) *Graph {
	tests := o.IncludeTests || m.fileTest[file]
	g := &Graph{IncludeTests: tests, Crumbs: m.crumbs(dirOf(file), file, -1)}
	g.Scope = Node{ID: "f:" + file, Label: file, Type: "file", Path: file,
		Kind: m.fileLanguage(file), Files: 1, Symbols: len(m.fileSyms[file]), Test: m.fileTest[file]}
	b := newBuilder(m)
	for _, i := range m.fileSyms[file] {
		if m.anchor[i] == i {
			b.node(m.symNode(i))
		}
	}
	rep := func(i int32) (string, bool) {
		f := m.syms[i].FilePath
		if f == file {
			return "s:" + m.syms[m.anchor[i]].ID, true
		}
		if m.fileTest[f] && !tests {
			return "", false
		}
		n := b.node(Node{ID: "f:" + f, Label: f, Type: "file", Path: f, Outside: true,
			Kind: m.fileLanguage(f), Test: m.fileTest[f], Files: 1, Symbols: len(m.fileSyms[f])})
		return n.ID, true
	}
	seen := map[int32]bool{}
	for _, i := range m.fileSyms[file] {
		for _, list := range [][]int32{m.out[i], m.in[i]} {
			for _, di := range list {
				if seen[di] {
					continue
				}
				seen[di] = true
				d := m.deps[di]
				from, okF := rep(d.from)
				to, okT := rep(d.to)
				if okF && okT {
					b.link(from, to, d)
				}
			}
		}
	}
	b.finish(g)
	return g
}

// symbolGraph renders one symbol's neighbourhood: what uses it, what it
// uses, and the symbols declared inside it.
func (m *Model) symbolGraph(sym int32, o Options) *Graph {
	s := &m.syms[sym]
	tests := o.IncludeTests || m.fileTest[s.FilePath]
	g := &Graph{IncludeTests: tests, Crumbs: m.crumbs(dirOf(s.FilePath), s.FilePath, sym)}
	b := newBuilder(m)
	focus := m.symNode(sym)
	focus.Focus = true
	b.node(focus)
	g.Scope = focus

	// The focus stands for itself and for everything rolled up into it.
	group := []int32{sym}
	var collect func(i int32, depth int)
	collect = func(i int32, depth int) {
		for _, k := range m.kids[i] {
			if m.anchor[k] == sym && depth < 32 {
				group = append(group, k)
				collect(k, depth+1)
			}
		}
	}
	collect(sym, 0)
	inGroup := map[int32]bool{}
	for _, i := range group {
		inGroup[i] = true
	}

	members := 0
	for _, k := range m.kids[sym] {
		if members == maxMembers {
			g.Hidden += len(m.kids[sym]) - maxMembers
			break
		}
		members++
		n := m.symNode(k)
		n.Member = true
		b.node(n)
		l := &Link{From: focus.ID, To: n.ID, Weight: 1, Kinds: map[string]int{"contains": 1}, Tiers: map[string]int{}}
		b.links[[2]string{l.From, l.To}] = l
	}

	rep := func(i int32) (string, bool) {
		if inGroup[i] {
			return focus.ID, true
		}
		if m.fileTest[m.syms[i].FilePath] && !tests {
			return "", false
		}
		a := m.anchor[i]
		if a == sym {
			return focus.ID, true
		}
		return b.node(m.symNode(a)).ID, true
	}
	seen := map[int32]bool{}
	for _, i := range group {
		for _, list := range [][]int32{m.out[i], m.in[i]} {
			for _, di := range list {
				if seen[di] {
					continue
				}
				seen[di] = true
				d := m.deps[di]
				from, okF := rep(d.from)
				to, okT := rep(d.to)
				if okF && okT {
					b.link(from, to, d)
				}
			}
		}
	}
	b.finish(g)
	return g
}

// Search finds symbols, files and directories whose name contains q
// (case-insensitive). Results are ranked: exact name, then prefix, then
// substring; production code before tests.
func (m *Model) Search(q string, limit int) []Hit {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 40
	}
	type scored struct {
		hit   Hit
		score int
	}
	var all []scored
	score := func(name, full string) int {
		switch {
		case name == q || full == q:
			return 0
		case strings.HasPrefix(name, q):
			return 1
		case strings.Contains(name, q):
			return 2
		case strings.Contains(full, q):
			return 3
		}
		return -1
	}
	for i := range m.syms {
		s := &m.syms[i]
		sc := score(strings.ToLower(s.Name), m.low[i])
		if sc < 0 {
			continue
		}
		test := m.fileTest[s.FilePath]
		if test {
			sc += 4
		}
		if m.anchor[i] != int32(i) {
			sc++ // fields and locals rank below the declarations that hold them
		}
		all = append(all, scored{Hit{ID: "s:" + s.ID, Label: symLabel(s), Type: "symbol",
			Kind: s.Kind, Path: s.FilePath, Line: s.Span.Start, Test: test}, sc})
	}
	for _, f := range m.files {
		lf := strings.ToLower(f)
		sc := score(path.Base(lf), lf)
		if sc < 0 {
			continue
		}
		if m.fileTest[f] {
			sc += 4
		}
		all = append(all, scored{Hit{ID: "f:" + f, Label: path.Base(f), Type: "file",
			Kind: m.fileLanguage(f), Path: f, Test: m.fileTest[f]}, sc})
	}
	for d := range m.dirFiles {
		if d == "" {
			continue
		}
		ld := strings.ToLower(d)
		sc := score(path.Base(ld), ld)
		if sc < 0 {
			continue
		}
		all = append(all, scored{Hit{ID: "d:" + d, Label: d, Type: "dir", Path: d}, sc})
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if len(a.hit.Label) != len(b.hit.Label) {
			return len(a.hit.Label) < len(b.hit.Label)
		}
		if a.hit.Path != b.hit.Path {
			return a.hit.Path < b.hit.Path
		}
		return a.hit.ID < b.hit.ID
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]Hit, len(all))
	for i, s := range all {
		out[i] = s.hit
	}
	return out
}

// Source returns the indexed declaration text of a symbol.
func (m *Model) Source(id string) (*Source, error) {
	typ, key, err := splitScope(id)
	if err != nil || typ != "s" {
		return nil, fmt.Errorf("source is available for symbols only")
	}
	i, ok := m.lookupSymbol(key)
	if !ok {
		return nil, fmt.Errorf("symbol %q is not in the index", key)
	}
	s := &m.syms[i]
	src := &Source{ID: "s:" + s.ID, Path: s.FilePath, Start: s.Span.Start, End: s.Span.End,
		Signature: s.Signature, Doc: s.Docstring, Text: s.RawText}
	if len(src.Text) > maxSourceBytes {
		cut := strings.LastIndexByte(src.Text[:maxSourceBytes], '\n')
		if cut <= 0 {
			cut = maxSourceBytes
		}
		src.Text = src.Text[:cut]
		src.Truncated = true
	}
	return src, nil
}
