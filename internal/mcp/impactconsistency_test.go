package mcp

import (
	"regexp"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// chi, 2026-10-17 (Codex review): three answers about the same contract
// disagreed with themselves. Querying the interface method redirected the
// agent to its implementation as a "wider anchor"; querying the
// implementation listed the interface among its supers and also among
// "other declarations named Match (not in this change)", and called the API
// test-only while listing two production callers that reach it through the
// interface.
var chiLikeFixture = map[string]string{
	"go.mod":                 "module example.com/chi\n\ngo 1.21\n",
	"chi.go":                 "package chi\n\ntype Context struct {\n\tRoutes Routes\n}\n\ntype Routes interface {\n\tMatch(rctx *Context, method, path string) bool\n}\n\ntype Router interface {\n\tRoutes\n\tUse(fn func())\n}\n",
	"mux.go":                 "package chi\n\ntype Mux struct{}\n\nfunc (mx *Mux) Match(rctx *Context, method, path string) bool {\n\treturn mx.find(rctx, method, path)\n}\n\nfunc (mx *Mux) find(rctx *Context, method, path string) bool {\n\treturn method != \"\" && path != \"\"\n}\n",
	"middleware/pattern.go":  "package middleware\n\ntype Pattern struct{ prefix string }\n\nfunc (p Pattern) Match(v string) bool {\n\treturn v == p.prefix\n}\n\nfunc useHeader(p Pattern, v string) bool {\n\treturn p.Match(v)\n}\n",
	"middleware/get_head.go": "package middleware\n\nimport \"example.com/chi\"\n\nfunc GetHead(next func(), rctx *chi.Context) bool {\n\treturn rctx.Routes.Match(rctx, \"HEAD\", \"/\")\n}\n\nfunc SupressNotFound(rctx *chi.Context, method, path string) bool {\n\tmatch := rctx.Routes.Match(rctx, method, path)\n\treturn match\n}\n",
	"mux_test.go":            "package chi\n\nimport \"testing\"\n\nfunc TestMuxMatch(t *testing.T) {\n\tr := &Mux{}\n\tif r.Match(&Context{}, \"GET\", \"/users/1\") == false {\n\t\tt.Fatal(\"x\")\n\t}\n}\n\nfunc TestMuxMatch_HasBasePath(t *testing.T) {\n\tr := &Mux{}\n\tif r.Match(&Context{}, \"GET\", \"/\") != true {\n\t\tt.Fatal(\"y\")\n\t}\n}\n",
}

var (
	supersBlock  = regexp.MustCompile(`(?m)^supers \(\d+\):\n((?:  \S.*\n(?:    .*\n)*)+)`)
	otherDeclRe  = regexp.MustCompile(`(?m)^other declarations named \S+ \(not in this change\): (.*)$`)
	callersBlock = regexp.MustCompile(`(?m)^callers \(\d+\):\n((?:  .*\n)+)`)
	familyBlock  = regexp.MustCompile(`(?m)^family \(\d+\):\n((?:  .*\n)+)`)
	widerAnchor  = regexp.MustCompile(`(?m)^// wider anchor: (\S+) `)
)

// impactContradictions returns the self-contradictions in one rendered
// change_impact answer. Invariants that hold for any answer:
//   - no declaration is both a related super contract and an unrelated namesake;
//   - a TEST-ONLY API claim never sits beside a production caller when the
//     answer lists super contracts (that caller reaches the API through one);
//   - a wider-anchor hint never points at a symbol the answer already lists as
//     its own family or supers: an implementation is not a wider anchor.
func impactContradictions(text string) []string {
	var out []string
	supers := map[string]bool{}
	if m := supersBlock.FindStringSubmatch(text); m != nil {
		for _, l := range strings.Split(m[1], "\n") {
			if f := strings.Fields(l); len(f) > 0 && !strings.HasPrefix(l, "    ") {
				supers[f[0]] = true
			}
		}
	}
	if m := otherDeclRe.FindStringSubmatch(text); m != nil {
		for name := range supers {
			if strings.Contains(m[1], name+" (") {
				out = append(out, name+" is a super contract and also listed as an unrelated namesake")
			}
		}
	}
	if strings.Contains(text, "TEST-ONLY API") && len(supers) > 0 {
		if m := callersBlock.FindStringSubmatch(text); m != nil {
			for _, l := range strings.Split(m[1], "\n") {
				if strings.TrimSpace(l) != "" && !strings.Contains(l, "[test]") {
					out = append(out, "TEST-ONLY API claim beside production caller "+strings.TrimSpace(l))
				}
			}
		}
	}
	if m := widerAnchor.FindStringSubmatch(text); m != nil {
		fam := familyBlock.FindStringSubmatch(text)
		if (fam != nil && strings.Contains(fam[1], m[1]+" ")) || supers[m[1]] {
			out = append(out, "wider anchor "+m[1]+" is already this answer's own family or supers")
		}
	}
	return out
}

func assertImpactConsistent(t *testing.T, text string) {
	t.Helper()
	for _, c := range impactContradictions(text) {
		t.Errorf("%s:\n%s", c, text)
	}
}

func TestImpactAnswersAboutAnInterfaceAndItsImplementationAgree(t *testing.T) {
	h := newImpactPlusHandler(t, chiLikeFixture)

	iface := invokeCompactText(t, h, "change_impact", map[string]any{"name": "Routes.Match"})
	assertImpactConsistent(t, iface)
	if strings.Contains(iface, "wider anchor: Mux.Match") {
		t.Errorf("querying the interface must not redirect to its implementation:\n%s", iface)
	}
	if !strings.Contains(iface, "Mux.Match") || !strings.Contains(iface, "GetHead") {
		t.Errorf("interface answer lost its family or callers:\n%s", iface)
	}

	impl := invokeCompactText(t, h, "change_impact", map[string]any{"name": "Mux.Match"})
	assertImpactConsistent(t, impl)
	if strings.Contains(impl, "TEST-ONLY API") {
		t.Errorf("production callers reach Mux.Match through Routes.Match, it is not test-only:\n%s", impl)
	}
	if !strings.Contains(impl, "GetHead") || !strings.Contains(impl, "SupressNotFound") || !strings.Contains(impl, "TestMuxMatch") {
		t.Errorf("implementation answer lost callers:\n%s", impl)
	}
	if !strings.Contains(impl, "related contract declarations naming Match") {
		t.Errorf("the super contract's declaration line should be listed as related:\n%s", impl)
	}
}

// The checker itself: each invariant fires on a contradictory answer, and a
// consistent one passes.
func TestImpactContradictionsCatchesEachContradiction(t *testing.T) {
	bad := map[string]string{
		"super also namesake":         "supers (1):\n  Routes.Match  chi.go:5\n    Match(rctx *Context) bool\nother declarations named Match (not in this change): Pattern.Match (a.go:1), Routes.Match (chi.go:5)\n",
		"test-only beside production": "// TEST-ONLY API (callers are all tests):\nsupers (1):\n  Routes.Match  chi.go:5\ncallers (2):\n  GetHead  get_head.go:3  [at 4:9]\n  TestX  x_test.go:1  [test]  [at 3:3]\n",
		"anchor is own family":        "family (1):\n  Mux.Match  mux.go:5\n// wider anchor: Mux.Match (9 sites, closed) — x\n",
	}
	for name, text := range bad {
		if len(impactContradictions(text)) == 0 {
			t.Errorf("%s: not flagged:\n%s", name, text)
		}
	}
	ok := "supers (1):\n  Routes.Match  chi.go:5\nother declarations named Match (not in this change): Pattern.Match (a.go:1)\ncallers (1):\n  TestX  x_test.go:1  [test]  [at 3:3]\n// TEST-ONLY API (callers are all tests):\n"
	if c := impactContradictions(ok); len(c) != 0 {
		t.Errorf("a consistent answer was flagged: %v", c)
	}
}

// A concrete method still gets the wider-anchor hint toward the base contract
// that lists it as a family member, with the sites it adds.
func TestAnchorContainsOnlyForTheBaseContract(t *testing.T) {
	impl := &grove.ChangeImpactResult{Declarations: []grove.SymbolRecord{{FilePath: "mux.go", QualifiedName: "Mux.Match"}}}
	base := &grove.ChangeImpactResult{
		Declarations: []grove.SymbolRecord{{FilePath: "chi.go", QualifiedName: "Routes.Match"}},
		Family:       []grove.SymbolRecord{{FilePath: "mux.go", QualifiedName: "Mux.Match"}},
		Callers:      []grove.SymbolRecord{{FilePath: "get_head.go", QualifiedName: "GetHead", Kind: "function"}},
	}
	if !anchorContains(base, impl) {
		t.Error("the base contract lists the implementation as family, so it contains it")
	}
	if anchorContains(impl, base) {
		t.Error("an implementation must never count as containing its own base contract")
	}
	if n := additionalSites(base, impl); n < 2 {
		t.Errorf("base adds its declaration and a caller the implementation answer lacks, got %d", n)
	}
	if n := additionalSites(base, base); n != 0 {
		t.Errorf("identical answers add nothing, got %d", n)
	}
}
