// Package view builds component-level projections (quotient graphs) of the
// code graph: a deterministic partition of symbols into components plus
// induced edges aggregated from the primitive edges that cross it.
//
// Two invariants hold for everything this package produces (see
// docs/DESIGN_LAYERED_INTELLIGENCE.md):
//
//  1. Provenance — every induced edge carries the constituent primitive
//     edges (sites) that induced it; Weight carries the full count when the
//     site list is capped. Abstraction is evidence-backed, never narrative.
//  2. Tier honesty — every induced edge reports the capability-tier
//     distribution of its constituent evidence, and a derived result's tier
//     is the minimum over its load-bearing evidence. View results claim
//     "complete-at-tier", never "closed".
//
// The partition is structural and deterministic (directory of each symbol's
// file path, optionally truncated to a depth). Semantic naming or clustering
// may annotate a view; it never defines one.
package view

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// dependencyKinds are the primitive edge kinds that express a directed
// dependency between components. Hierarchy kinds (contains, defines) and
// test evidence (tests) are excluded: the former are the partition itself,
// the latter is coverage, not dependency.
var dependencyKinds = map[string]bool{
	"calls":      true,
	"imports":    true,
	"uses-type":  true,
	"implements": true,
	"extends":    true,
}

// Tier is the capability tier of one piece of edge evidence, mapped from
// Grove's evidence source. Order matters: lower index = stronger claim.
var tierOrder = []string{"precise", "measured", "structural", "heuristic"}

// tierOf maps a Grove evidence source to a capability tier. Unknown or
// empty sources tier down to heuristic: unproven provenance is degraded
// evidence, not silently promoted.
func tierOf(source string) string {
	switch source {
	case "native":
		return "precise"
	case "astkit":
		return "measured"
	case "tree_sitter":
		return "structural"
	default: // heuristic, regex, unknown, ""
		return "heuristic"
	}
}

// MinTier returns the weakest tier present in a tier-count distribution —
// the tier of the derived claim built on that evidence.
func MinTier(tiers map[string]int) string {
	weakest := ""
	for _, t := range tierOrder {
		if tiers[t] > 0 {
			weakest = t
		}
	}
	if weakest == "" {
		return "heuristic"
	}
	return weakest
}

// Options configures a Build.
type Options struct {
	// Depth truncates component keys to the first N path segments
	// (0 = full directory path).
	Depth int
	// MaxSites caps the constituent sites kept per induced edge; Weight
	// carries the full count. 0 = default (5).
	MaxSites int
	// IncludeTests keeps test-file symbols in the view. Default false:
	// an architecture map describes the production shape; test files
	// otherwise pollute it with helper-induced edges (measured on this
	// repository: a heuristic test-only edge manufactured a false
	// httpapi<->mcp cycle). Excluded files are counted, not hidden —
	// View.TestFilesExcluded reports how many were left out.
	IncludeTests bool
}

// Component is one partition cell: a directory (or depth-truncated prefix).
type Component struct {
	Name     string `json:"name"`
	Files    int    `json:"files"`
	Symbols  int    `json:"symbols"`
	Exported int    `json:"exported"`
	FanIn    int    `json:"fanIn"`  // distinct components depending on this one
	FanOut   int    `json:"fanOut"` // distinct components this one depends on
}

// Site is one constituent primitive edge of an induced edge — the expansion
// evidence: a concrete crossing of the component boundary.
type Site struct {
	FromSymbol string `json:"fromSymbol"`
	FromFile   string `json:"fromFile"`
	FromLine   int    `json:"fromLine"`
	ToSymbol   string `json:"toSymbol"`
	ToFile     string `json:"toFile"`
	ToLine     int    `json:"toLine"`
	Kind       string `json:"kind"`
	Tier       string `json:"tier"`
}

// InducedEdge is one component-level dependency: the aggregation of every
// primitive dependency edge crossing from one component to another.
type InducedEdge struct {
	From   string         `json:"from"`
	To     string         `json:"to"`
	Weight int            `json:"weight"` // total constituent primitive edges
	Kinds  map[string]int `json:"kinds"`  // breakdown by primitive edge kind
	Tiers  map[string]int `json:"tiers"`  // breakdown by evidence tier
	Sites  []Site         `json:"sites"`  // capped at MaxSites; Weight carries the truth
}

// Cycle is one strongly connected component of size > 1 in the induced
// graph, with the induced edges among its members as evidence.
type Cycle struct {
	Components []string      `json:"components"`
	Edges      []InducedEdge `json:"edges"`
}

// View is the quotient graph: components plus induced edges. All slices are
// deterministically ordered.
type View struct {
	Components []Component   `json:"components"`
	Edges      []InducedEdge `json:"edges"`
	// TierSummary aggregates the tier distribution over every induced
	// edge's constituent evidence.
	TierSummary map[string]int `json:"tierSummary"`
	// Depth echoes Options.Depth (0 = full directory path).
	Depth int `json:"depth"`
	// IncludeTests echoes Options.IncludeTests.
	IncludeTests bool `json:"includeTests"`
	// TestFilesExcluded counts distinct test files left out of the view
	// (0 when IncludeTests is true). Exclusion is reported, never silent.
	TestFilesExcluded int `json:"testFilesExcluded"`
}

// IsTestPath reports whether a repo-relative file path is a test file by
// the naming conventions of the indexed languages. Deterministic and purely
// lexical — a wrong classification here only moves a file between the
// production and test views; it never invents or drops evidence.
func IsTestPath(filePath string) bool {
	p := strings.ToLower(filepath.ToSlash(filePath))
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "test", "tests", "__tests__", "testdata", "spec", "specs":
			return true
		}
	}
	base := path.Base(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case strings.HasSuffix(stem, "_test"), // Go, Python, Ruby, C
		strings.HasSuffix(stem, ".test"), strings.HasSuffix(stem, ".spec"), // JS/TS
		strings.HasSuffix(stem, "_spec"),                                  // Ruby
		strings.HasPrefix(stem, "test_"),                                  // Python
		base == "conftest.py":
		return true
	case (ext == ".java" || ext == ".cs" || ext == ".kt") &&
		(strings.HasSuffix(stem, "test") || strings.HasSuffix(stem, "tests")):
		return true
	}
	return false
}

// componentOf returns the component key for a repo-relative file path.
func componentOf(filePath string, depth int) string {
	dir := path.Dir(filepath.ToSlash(filePath))
	if dir == "." || dir == "/" || dir == "" {
		return "(root)"
	}
	if depth > 0 {
		segs := 1
		for i := 0; i < len(dir); i++ {
			if dir[i] == '/' {
				segs++
				if segs > depth {
					dir = dir[:i]
					break
				}
			}
		}
	}
	return dir
}

// Build constructs the quotient view from a full graph snapshot. It is a
// pure function: identical inputs (in any order) produce identical output.
func Build(symbols []grove.SymbolRecord, edges []grove.Edge, opts Options) *View {
	maxSites := opts.MaxSites
	if maxSites <= 0 {
		maxSites = 5
	}

	// Partition. Document symbols (indexed prose: md/yaml/…) are excluded
	// entirely — they are search food, not architecture.
	type symInfo struct {
		comp string
		sym  *grove.SymbolRecord
	}
	byID := make(map[string]symInfo, len(symbols))
	type compStats struct {
		files            map[string]bool
		symbols, exports int
	}
	stats := map[string]*compStats{}
	excludedTestFiles := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		if s.Kind == "document" {
			continue
		}
		if !opts.IncludeTests && IsTestPath(s.FilePath) {
			excludedTestFiles[s.FilePath] = true
			continue
		}
		comp := componentOf(s.FilePath, opts.Depth)
		byID[s.ID] = symInfo{comp: comp, sym: s}
		cs := stats[comp]
		if cs == nil {
			cs = &compStats{files: map[string]bool{}}
			stats[comp] = cs
		}
		cs.files[s.FilePath] = true
		if s.Kind != "file" {
			cs.symbols++
			if s.Exports {
				cs.exports++
			}
		}
	}

	// Induce. Only dependency kinds; only edges whose endpoints both resolve
	// to indexed, partitioned symbols (external targets are out of scope for
	// a project-local view); only crossings (A != B).
	induced := map[[2]string]*InducedEdge{}
	for _, e := range edges {
		if !dependencyKinds[e.Type] {
			continue
		}
		from, okF := byID[e.From]
		to, okT := byID[e.To]
		if !okF || !okT || from.comp == to.comp {
			continue
		}
		key := [2]string{from.comp, to.comp}
		ie := induced[key]
		if ie == nil {
			ie = &InducedEdge{From: from.comp, To: to.comp,
				Kinds: map[string]int{}, Tiers: map[string]int{}}
			induced[key] = ie
		}
		tier := tierOf(e.Source)
		ie.Weight++
		ie.Kinds[e.Type]++
		ie.Tiers[tier]++
		ie.Sites = append(ie.Sites, Site{
			FromSymbol: displayName(from.sym), FromFile: from.sym.FilePath, FromLine: from.sym.Span.Start,
			ToSymbol: displayName(to.sym), ToFile: to.sym.FilePath, ToLine: to.sym.Span.Start,
			Kind: e.Type, Tier: tier,
		})
	}

	v := &View{TierSummary: map[string]int{}, Depth: opts.Depth,
		IncludeTests: opts.IncludeTests, TestFilesExcluded: len(excludedTestFiles)}

	// Deterministic ordering + site capping.
	fanIn := map[string]map[string]bool{}
	fanOut := map[string]map[string]bool{}
	for _, ie := range induced {
		sort.Slice(ie.Sites, func(i, j int) bool {
			a, b := ie.Sites[i], ie.Sites[j]
			if a.FromFile != b.FromFile {
				return a.FromFile < b.FromFile
			}
			if a.FromLine != b.FromLine {
				return a.FromLine < b.FromLine
			}
			return a.ToSymbol < b.ToSymbol
		})
		if len(ie.Sites) > maxSites {
			ie.Sites = ie.Sites[:maxSites]
		}
		for t, n := range ie.Tiers {
			v.TierSummary[t] += n
		}
		if fanOut[ie.From] == nil {
			fanOut[ie.From] = map[string]bool{}
		}
		if fanIn[ie.To] == nil {
			fanIn[ie.To] = map[string]bool{}
		}
		fanOut[ie.From][ie.To] = true
		fanIn[ie.To][ie.From] = true
		v.Edges = append(v.Edges, *ie)
	}
	sort.Slice(v.Edges, func(i, j int) bool {
		if v.Edges[i].Weight != v.Edges[j].Weight {
			return v.Edges[i].Weight > v.Edges[j].Weight
		}
		if v.Edges[i].From != v.Edges[j].From {
			return v.Edges[i].From < v.Edges[j].From
		}
		return v.Edges[i].To < v.Edges[j].To
	})

	for comp, cs := range stats {
		v.Components = append(v.Components, Component{
			Name: comp, Files: len(cs.files), Symbols: cs.symbols,
			Exported: cs.exports, FanIn: len(fanIn[comp]), FanOut: len(fanOut[comp]),
		})
	}
	sort.Slice(v.Components, func(i, j int) bool {
		return v.Components[i].Name < v.Components[j].Name
	})
	return v
}

func displayName(s *grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

// Edge returns the induced edge from one component to another, or nil.
func (v *View) Edge(from, to string) *InducedEdge {
	for i := range v.Edges {
		if v.Edges[i].From == from && v.Edges[i].To == to {
			return &v.Edges[i]
		}
	}
	return nil
}

// Cycles returns the strongly connected components of size > 1 in the
// induced graph (Tarjan), each with its member edges as evidence. The
// algorithm is exact; the result's tier is the weakest constituent edge
// tier — an exact algorithm over measured evidence yields a measured claim.
func (v *View) Cycles() []Cycle {
	// Adjacency over component names, deterministic order.
	adj := map[string][]string{}
	for _, e := range v.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	nodes := make([]string, 0, len(adj))
	seen := map[string]bool{}
	for _, e := range v.Edges {
		for _, n := range []string{e.From, e.To} {
			if !seen[n] {
				seen[n] = true
				nodes = append(nodes, n)
			}
		}
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		sort.Strings(adj[n])
	}

	// Tarjan.
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var counter int
	var sccs [][]string

	var strongconnect func(n string)
	strongconnect = func(n string) {
		index[n] = counter
		low[n] = counter
		counter++
		stack = append(stack, n)
		onStack[n] = true
		for _, m := range adj[n] {
			if _, ok := index[m]; !ok {
				strongconnect(m)
				if low[m] < low[n] {
					low[n] = low[m]
				}
			} else if onStack[m] && index[m] < low[n] {
				low[n] = index[m]
			}
		}
		if low[n] == index[n] {
			var scc []string
			for {
				m := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[m] = false
				scc = append(scc, m)
				if m == n {
					break
				}
			}
			if len(scc) > 1 {
				sort.Strings(scc)
				sccs = append(sccs, scc)
			}
		}
	}
	for _, n := range nodes {
		if _, ok := index[n]; !ok {
			strongconnect(n)
		}
	}
	sort.Slice(sccs, func(i, j int) bool {
		return fmt.Sprint(sccs[i]) < fmt.Sprint(sccs[j])
	})

	cycles := make([]Cycle, 0, len(sccs))
	for _, scc := range sccs {
		member := map[string]bool{}
		for _, c := range scc {
			member[c] = true
		}
		cy := Cycle{Components: scc}
		for _, e := range v.Edges {
			if member[e.From] && member[e.To] {
				cy.Edges = append(cy.Edges, e)
			}
		}
		cycles = append(cycles, cy)
	}
	return cycles
}
