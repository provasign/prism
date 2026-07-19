package view

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func sym(id, file, kind, name string, line int, exported bool) grove.SymbolRecord {
	return grove.SymbolRecord{ID: id, FilePath: file, Kind: kind, Name: name,
		QualifiedName: name, Span: grove.SpanInfo{Start: line}, Exports: exported}
}

func edge(from, to, kind, source string) grove.Edge {
	return grove.Edge{From: from, To: to, Type: kind, Source: source, Confidence: 1}
}

// fixture: pkg a → pkg b (2 calls + 1 uses-type), pkg b → pkg c (1 call),
// intra-component call inside a (must not induce), a tests edge (must not
// induce), and a document symbol (must not partition).
func fixture() ([]grove.SymbolRecord, []grove.Edge) {
	symbols := []grove.SymbolRecord{
		sym("a1", "a/x.go", "function", "a.F1", 10, true),
		sym("a2", "a/x.go", "function", "a.F2", 20, false),
		sym("b1", "b/y.go", "function", "b.G", 5, true),
		sym("b2", "b/y.go", "struct", "b.T", 30, true),
		sym("c1", "c/z.go", "function", "c.H", 7, true),
		sym("d1", "README.md", "document", "README", 1, false),
	}
	edges := []grove.Edge{
		edge("a1", "b1", "calls", "astkit"),
		edge("a2", "b1", "calls", "heuristic"),
		edge("a1", "b2", "uses-type", "astkit"),
		edge("b1", "c1", "calls", "native"),
		edge("a1", "a2", "calls", "astkit"),  // intra-component
		edge("a1", "b1", "tests", "astkit"),  // non-dependency kind
		edge("a1", "zz", "calls", "astkit"),  // unresolved endpoint
	}
	return symbols, edges
}

func TestBuild_PartitionAndInduction(t *testing.T) {
	symbols, edges := fixture()
	v := Build(symbols, edges, Options{})

	var names []string
	for _, c := range v.Components {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b", "c"}) {
		t.Fatalf("components = %v (document must not partition)", names)
	}

	if len(v.Edges) != 2 {
		t.Fatalf("induced edges = %+v, want a->b and b->c", v.Edges)
	}
	ab := v.Edge("a", "b")
	if ab == nil || ab.Weight != 3 {
		t.Fatalf("a->b = %+v, want weight 3", ab)
	}
	if ab.Kinds["calls"] != 2 || ab.Kinds["uses-type"] != 1 {
		t.Fatalf("a->b kinds = %v", ab.Kinds)
	}
	if ab.Tiers["measured"] != 2 || ab.Tiers["heuristic"] != 1 {
		t.Fatalf("a->b tiers = %v (astkit->measured, heuristic->heuristic)", ab.Tiers)
	}
	if MinTier(ab.Tiers) != "heuristic" {
		t.Fatalf("MinTier(a->b) = %s, want the weakest constituent", MinTier(ab.Tiers))
	}
	bc := v.Edge("b", "c")
	if bc == nil || bc.Weight != 1 || bc.Tiers["precise"] != 1 {
		t.Fatalf("b->c = %+v, want weight 1 precise", bc)
	}
	if MinTier(bc.Tiers) != "precise" {
		t.Fatalf("MinTier(b->c) = %s", MinTier(bc.Tiers))
	}
	if v.Edge("a", "c") != nil {
		t.Fatal("no primitive edge crosses a->c; induction invented one")
	}
}

func TestBuild_ProvenanceSites(t *testing.T) {
	symbols, edges := fixture()
	v := Build(symbols, edges, Options{})
	ab := v.Edge("a", "b")
	if len(ab.Sites) != 3 {
		t.Fatalf("a->b sites = %d, want all 3 constituents", len(ab.Sites))
	}
	// Sites are sorted by (fromFile, fromLine, toSymbol); first is a.F1:10.
	s := ab.Sites[0]
	if s.FromSymbol != "a.F1" || s.FromFile != "a/x.go" || s.FromLine != 10 {
		t.Fatalf("first site = %+v", s)
	}
	if s.ToFile != "b/y.go" || s.Kind == "" || s.Tier == "" {
		t.Fatalf("site missing evidence fields: %+v", s)
	}
}

func TestBuild_SiteCapKeepsWeight(t *testing.T) {
	symbols, edges := fixture()
	v := Build(symbols, edges, Options{MaxSites: 1})
	ab := v.Edge("a", "b")
	if len(ab.Sites) != 1 || ab.Weight != 3 {
		t.Fatalf("capped edge = %d sites weight %d, want 1/3 (weight carries the truth)",
			len(ab.Sites), ab.Weight)
	}
}

func TestBuild_Deterministic(t *testing.T) {
	symbols, edges := fixture()
	want := Build(symbols, edges, Options{})
	for i := 0; i < 5; i++ {
		s2 := append([]grove.SymbolRecord(nil), symbols...)
		e2 := append([]grove.Edge(nil), edges...)
		rand.New(rand.NewSource(int64(i))).Shuffle(len(s2), func(a, b int) { s2[a], s2[b] = s2[b], s2[a] })
		rand.New(rand.NewSource(int64(i))).Shuffle(len(e2), func(a, b int) { e2[a], e2[b] = e2[b], e2[a] })
		got := Build(s2, e2, Options{})
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("shuffle %d changed the view:\nwant %+v\ngot  %+v", i, want, got)
		}
	}
}

func TestBuild_DepthTruncation(t *testing.T) {
	symbols := []grove.SymbolRecord{
		sym("m1", "internal/mcp/tools.go", "function", "Invoke", 1, true),
		sym("v1", "internal/view/view.go", "function", "Build", 1, true),
		sym("r1", "main.go", "function", "main", 1, false),
	}
	edges := []grove.Edge{edge("m1", "v1", "calls", "astkit")}
	v := Build(symbols, edges, Options{Depth: 1})
	var names []string
	for _, c := range v.Components {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"(root)", "internal"}) {
		t.Fatalf("depth-1 components = %v", names)
	}
	// mcp and view collapse into one component at depth 1: the crossing
	// disappears — no self-edges.
	if len(v.Edges) != 0 {
		t.Fatalf("depth-1 edges = %+v, want none (crossing became internal)", v.Edges)
	}
}

func TestCycles(t *testing.T) {
	symbols := []grove.SymbolRecord{
		sym("x1", "x/a.go", "function", "x.A", 1, true),
		sym("y1", "y/b.go", "function", "y.B", 1, true),
		sym("z1", "z/c.go", "function", "z.C", 1, true),
	}
	acyclic := []grove.Edge{
		edge("x1", "y1", "calls", "astkit"),
		edge("y1", "z1", "calls", "astkit"),
	}
	if got := Build(symbols, acyclic, Options{}).Cycles(); len(got) != 0 {
		t.Fatalf("acyclic graph reported cycles: %+v", got)
	}

	cyclic := append(acyclic, edge("z1", "x1", "calls", "heuristic"))
	cycles := Build(symbols, cyclic, Options{}).Cycles()
	if len(cycles) != 1 {
		t.Fatalf("cycles = %+v, want exactly one SCC", cycles)
	}
	if !reflect.DeepEqual(cycles[0].Components, []string{"x", "y", "z"}) {
		t.Fatalf("SCC members = %v", cycles[0].Components)
	}
	if len(cycles[0].Edges) != 3 {
		t.Fatalf("SCC evidence edges = %d, want 3", len(cycles[0].Edges))
	}
}

func TestCycles_TwoIndependentSCCs(t *testing.T) {
	symbols := []grove.SymbolRecord{
		sym("p1", "p/a.go", "function", "p.A", 1, true),
		sym("q1", "q/b.go", "function", "q.B", 1, true),
		sym("r1", "r/c.go", "function", "r.C", 1, true),
		sym("s1", "s/d.go", "function", "s.D", 1, true),
	}
	edges := []grove.Edge{
		edge("p1", "q1", "calls", "astkit"), edge("q1", "p1", "calls", "astkit"),
		edge("r1", "s1", "calls", "astkit"), edge("s1", "r1", "calls", "astkit"),
	}
	cycles := Build(symbols, edges, Options{}).Cycles()
	if len(cycles) != 2 {
		t.Fatalf("cycles = %+v, want two independent SCCs", cycles)
	}
	if !reflect.DeepEqual(cycles[0].Components, []string{"p", "q"}) ||
		!reflect.DeepEqual(cycles[1].Components, []string{"r", "s"}) {
		t.Fatalf("SCCs = %+v", cycles)
	}
}

func TestBuild_TestFilesExcludedByDefault(t *testing.T) {
	symbols := []grove.SymbolRecord{
		sym("p1", "prod/a.go", "function", "prod.A", 1, true),
		sym("q1", "other/b.go", "function", "other.B", 1, true),
		sym("t1", "prod/a_test.go", "function", "prod.TestA", 1, false),
	}
	edges := []grove.Edge{
		edge("p1", "q1", "calls", "astkit"),
		// Test-helper edge: would manufacture a reverse dependency
		// other<-prod... via the test file if tests were included.
		edge("t1", "q1", "calls", "heuristic"),
	}

	v := Build(symbols, edges, Options{})
	if v.TestFilesExcluded != 1 {
		t.Fatalf("TestFilesExcluded = %d, want 1", v.TestFilesExcluded)
	}
	if e := v.Edge("prod", "other"); e == nil || e.Weight != 1 {
		t.Fatalf("prod->other = %+v, want only the production edge", e)
	}
	for _, c := range v.Components {
		if c.Name == "prod" && c.Symbols != 1 {
			t.Fatalf("prod symbols = %d, test symbol leaked in", c.Symbols)
		}
	}

	vt := Build(symbols, edges, Options{IncludeTests: true})
	if vt.TestFilesExcluded != 0 {
		t.Fatalf("IncludeTests view reports %d excluded", vt.TestFilesExcluded)
	}
	if e := vt.Edge("prod", "other"); e == nil || e.Weight != 2 {
		t.Fatalf("include-tests prod->other = %+v, want weight 2", e)
	}
}

func TestIsTestPath(t *testing.T) {
	yes := []string{
		"pkg/a_test.go", "tests/util.py", "src/__tests__/x.ts",
		"app/foo.spec.ts", "app/foo.test.js", "lib/bar_spec.rb",
		"pkg/test_helpers.py", "conftest.py", "src/FooTest.java",
		"src/FooTests.cs", "internal/testdata/fix.go",
	}
	no := []string{
		"pkg/a.go", "src/contest.py", "app/latest.ts", "src/Testament.java",
		"attestation/sign.go", "pkg/protest.go",
	}
	for _, p := range yes {
		if !IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

func TestComponentOf(t *testing.T) {
	cases := []struct {
		file  string
		depth int
		want  string
	}{
		{"main.go", 0, "(root)"},
		{"a/b/c.go", 0, "a/b"},
		{"a/b/c.go", 1, "a"},
		{"a/b/c/d.go", 2, "a/b"},
		{"a/b/c/d.go", 9, "a/b/c"},
	}
	for _, c := range cases {
		if got := componentOf(c.file, c.depth); got != c.want {
			t.Errorf("componentOf(%q, %d) = %q, want %q", c.file, c.depth, got, c.want)
		}
	}
}
