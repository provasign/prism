package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// budgetRouterFixture reproduces the echo pr3006 shape: a config type used
// by a little production code and many test functions.
func budgetRouterFixture() map[string]string {
	var tests strings.Builder
	tests.WriteString("package r\n\nimport \"testing\"\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&tests, "\nfunc TestRouter%02d(t *testing.T) {\n\tcfg := RouterConfig{Unescape: true}\n\tr := NewRouter(cfg)\n\tif r == nil {\n\t\tt.Fatal(\"nil\")\n\t}\n}\n", i)
	}
	return map[string]string{
		"router.go": "package r\n\ntype RouterConfig struct {\n\tUnescape bool\n}\n\n" +
			"func DefaultRouterConfig() RouterConfig {\n\treturn RouterConfig{Unescape: true}\n}\n\n" +
			"type Router struct {\n\tcfg RouterConfig\n}\n\n" +
			"func NewRouter(cfg RouterConfig) *Router {\n\tif cfg.Unescape {\n\t\treturn &Router{cfg: cfg}\n\t}\n\treturn &Router{}\n}\n",
		"router_test.go": tests.String(),
	}
}

var contextLine = regexp.MustCompile(`(?m)^\s+\d+- `)

// echo pr3006: a default search returned +/-2 context lines for 20 matches,
// mostly in test files, plus full bodies of test functions (11-16k chars).
func TestDefaultSearchBudgetBareLinesTestCountsOneBody(t *testing.T) {
	srv := compactFixture(t, budgetRouterFixture())
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"RouterConfig"}, "scope": "text"})
	if contextLine.MatchString(out) {
		t.Fatalf("a term with more than %d hits kept context lines:\n%s", searchBudgetContextHits, out)
	}
	if strings.Contains(out, "router_test.go:") {
		t.Fatalf("test-file hits were listed line by line:\n%s", out)
	}
	if !strings.Contains(out, "// test files: 12 hit(s) in 1 file(s)") || !strings.Contains(out, "router_test.go (12)") {
		t.Fatalf("test-file hits were not collapsed to a per-file count:\n%s", out)
	}
	if !strings.Contains(out, "router.go:") {
		t.Fatalf("production match lines missing:\n%s", out)
	}
	if n := strings.Count(out, "**`"); n > 1 {
		t.Fatalf("default search delivered %d bodies, want at most one:\n%s", n, out)
	}
	if strings.Contains(out, "**`router_test.go`**") {
		t.Fatalf("the one body was a test function:\n%s", out)
	}
}

func TestDefaultSearchBudgetKeepsTestLinesWhenTestsTargeted(t *testing.T) {
	srv := compactFixture(t, budgetRouterFixture())
	for _, args := range []map[string]any{
		{"terms": []any{"RouterConfig"}, "scope": "text", "glob": []any{"*_test.go"}},
		{"terms": []any{"RouterConfig", "TestRouter"}, "scope": "text"},
	} {
		out := callCompact(t, srv, "search", args)
		if !strings.Contains(out, "router_test.go:") {
			t.Fatalf("%v: a test-targeted search lost its test lines:\n%s", args, out)
		}
	}
}

func TestExplicitContextOverridesSearchBudget(t *testing.T) {
	srv := compactFixture(t, budgetRouterFixture())
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"RouterConfig"}, "scope": "text", "context": 1})
	if !contextLine.MatchString(out) || !strings.Contains(out, "router_test.go:") {
		t.Fatalf("explicit context= did not keep the grep -C shape:\n%s", out)
	}
}

// Many matches in one term: at most searchBudgetTermLines lines and an
// explicit count of the rest.
func TestDefaultSearchBudgetCapsLinesPerTerm(t *testing.T) {
	var src strings.Builder
	src.WriteString("package p\n\nfunc Uses() {\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&src, "\tDenseCall(%d)\n", i)
	}
	src.WriteString("}\n\nfunc DenseCall(i int) {}\n")
	srv := compactFixture(t, map[string]string{"p.go": src.String()})
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"DenseCall"}, "scope": "text"})
	if got := len(regexp.MustCompile(`(?m)^p\.go:\d+: `).FindAllString(out, -1)); got > searchBudgetTermLines {
		t.Fatalf("%d match lines, want at most %d:\n%s", got, searchBudgetTermLines, out)
	}
	if !regexp.MustCompile(`// \+\d+ more matching line\(s\) not shown`).MatchString(out) {
		t.Fatalf("no count of the undisplayed lines:\n%s", out)
	}
	if len(out) > 6000 {
		t.Fatalf("single-term default search is %d chars", len(out))
	}
}

// jackson pr5959: files_only with the default scope returned 18,246 chars of
// symbols, notes and match lines; only scope=text honored it.
func TestFilesOnlyHonoredInEveryScope(t *testing.T) {
	srv := compactFixture(t, budgetRouterFixture())
	for _, scope := range []string{"both", "symbols", "text"} {
		out := callCompact(t, srv, "search", map[string]any{"terms": []any{"RouterConfig"}, "scope": scope, "files_only": true})
		if strings.Contains(out, "symbols (") || strings.Contains(out, "**`") || regexp.MustCompile(`\.go:\d+`).MatchString(out) {
			t.Fatalf("scope=%s files_only returned more than paths:\n%s", scope, out)
		}
		if !strings.Contains(out, "router.go") {
			t.Fatalf("scope=%s files_only lost the matching file:\n%s", scope, out)
		}
	}
}

// dubbo pr16395: scope=symbols returned 15.6k chars, 8.6k of it bodies.
func TestSymbolScopeSearchDeliversAtMostOneBody(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 4; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "package r\n\ntype Router%d struct{}\n\nfunc (r *Router%d) DoRoute(in []int) []int {\n", i, i)
		for j := 0; j < 20; j++ {
			fmt.Fprintf(&b, "\tin = append(in, %d)\n", j)
		}
		b.WriteString("\treturn in\n}\n")
		files[fmt.Sprintf("r%d.go", i)] = b.String()
	}
	srv := compactFixture(t, files)
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"DoRoute", "Router1"}, "scope": "symbols"})
	if n := strings.Count(out, "**`"); n > 1 {
		t.Fatalf("scope=symbols delivered %d bodies, want at most one:\n%s", n, out)
	}
	explicit := callCompact(t, srv, "search", map[string]any{"terms": []any{"DoRoute", "Router1"}, "scope": "symbols", "include_bodies": true})
	if n := strings.Count(explicit, "**`"); n < 2 {
		t.Fatalf("explicit include_bodies=true lost the wider body shape (%d bodies):\n%s", n, explicit)
	}
}

// jackson pr5977: the fix file was named "(7)" in the inventory with no code.
// A small inventory gives every file with no displayed line its best line.
func TestInventoryFilesCarryTheirBestMatchLine(t *testing.T) {
	h := newTestHandler(t)
	target := writeInventoryFixture(t, h.Root)
	_, text := sampledTextFromSearch(t, h, map[string]any{"query": "invStaticTyping", "scope": "text"})
	if !strings.Contains(text, "  "+target+" (1)  2: protected final boolean _invStaticTyping;") {
		t.Fatalf("inventory file without a displayed line lacks its best match line:\n%s", text)
	}
}

func TestBestMatchLinePrefersABranchOnTheTerm(t *testing.T) {
	hits := []map[string]any{
		{"line": 33, "text": "    protected final boolean _staticTyping;"},
		{"line": 71, "text": "        _staticTyping = src._staticTyping;"},
		{"line": 169, "text": "                if (_staticTyping && !_elementType.isJavaLangObject()) {"},
	}
	if got := intArg(bestMatchLine(hits), "line", 0); got != 169 {
		t.Fatalf("best line = %d, want the branch at 169", got)
	}
}

// dubbo pr16395: `lookup AbstractStateRouter` returned the whole 7.4k class.
// A type over typeOutlineLines lines returns its header and member outline;
// a function of the same size keeps its full body.
func TestLookupOutlinesMidSizeTypesKeepsFunctions(t *testing.T) {
	var cls strings.Builder
	cls.WriteString("package r;\n\npublic class Mid {\n    private int count;\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&cls, "    public int m%02d(int x) {\n        int y = x + %d;\n        y = y * 2;\n        y = y - 1;\n        y = y + 3;\n        return y;\n    }\n\n", i, i)
	}
	cls.WriteString("}\n")
	var fn strings.Builder
	fn.WriteString("package p\n\nfunc Long() int {\n\tx := 0\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&fn, "\tx += %d\n", i)
	}
	fn.WriteString("\treturn x\n}\n")
	srv := compactFixture(t, map[string]string{"src/r/Mid.java": cls.String(), "long.go": fn.String()})
	out := callCompact(t, srv, "lookup", map[string]any{"name": "Mid"})
	if !strings.Contains(out, "BODY CAPPED") || !strings.Contains(out, "m11(int x)") || strings.Contains(out, "int y = x + 7;") {
		t.Fatalf("a %d+ line class was not outlined:\n%s", typeOutlineLines, out)
	}
	if !strings.Contains(out, "op=read file=") {
		t.Fatalf("outline note does not name the narrow read:\n%s", out)
	}
	long := callCompact(t, srv, "lookup", map[string]any{"name": "Long"})
	if strings.Contains(long, "BODY CAPPED") || !strings.Contains(long, "x += 99") {
		t.Fatalf("a 100-line function lost its full body:\n%s", long)
	}
}

func TestNarrowReadHint(t *testing.T) {
	for _, c := range []struct {
		from, to, shownFrom, shownTo int
		want                         string
	}{
		{10, 100, 10, 40, `rest: op=read file="a.go" from=41 to=100`},
		{10, 100, 50, 100, `rest: op=read file="a.go" from=10 to=49`},
		{10, 100, 40, 60, `rest: op=read ranges=[{file:"a.go",from:10,to:39},{file:"a.go",from:61,to:100}]`},
		{1, 900, 1, 30, `rest: op=read file="a.go" from=31 to=270`},
	} {
		if got := narrowReadHint("a.go", c.from, c.to, c.shownFrom, c.shownTo); got != c.want {
			t.Errorf("narrowReadHint(%d,%d,%d,%d) = %s, want %s", c.from, c.to, c.shownFrom, c.shownTo, got, c.want)
		}
	}
}

func TestSearchTargetsTests(t *testing.T) {
	for _, c := range []struct {
		sc    searchScope
		terms []string
		want  bool
	}{
		{searchScope{}, []string{"RouterConfig"}, false},
		{searchScope{paths: []string{"src/test/java/x"}}, []string{"JsonView"}, true},
		{searchScope{glob: []string{"tests/**"}}, []string{"zsh"}, true},
		{searchScope{}, []string{"def test_zsh"}, true},
		{searchScope{paths: []string{"src/main/java"}}, []string{"Serializer"}, false},
		{searchScope{paths: []string{"lib/inspect.py"}}, []string{"getLatestVersion"}, false},
		{searchScope{glob: []string{"**/*Test.java"}}, []string{"x"}, true},
		{searchScope{}, []string{"TestRouter"}, true},
		{searchScope{}, []string{"assertEquals"}, true},
	} {
		if got := searchTargetsTests(c.sc, c.terms); got != c.want {
			t.Errorf("searchTargetsTests(%+v, %v) = %v, want %v", c.sc, c.terms, got, c.want)
		}
	}
}

// A term found only in test files keeps its lines: there is nothing else to
// show, and the counts alone would force a second call.
func TestDefaultSearchBudgetKeepsLinesWhenOnlyTestsMatch(t *testing.T) {
	var extra strings.Builder
	extra.WriteString("package r\n\nfunc onlyHelperX() int { return 1 }\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&extra, "var _ = onlyHelperX() // %d\n", i)
	}
	srv := compactFixture(t, map[string]string{"router.go": "package r\n", "helper_test.go": extra.String()})
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"onlyHelperX"}, "scope": "text"})
	if !strings.Contains(out, "helper_test.go:") || strings.Contains(out, "// test files:") {
		t.Fatalf("a term that only matches tests lost its lines:\n%s", out)
	}
}
