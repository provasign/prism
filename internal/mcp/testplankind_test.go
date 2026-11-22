package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// BUG-15: tests are recognized by attribute, not only by file name.
func TestTestCodeClassifier(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/lib.rs", "pub fn add() {}\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn adds() {}\n\n    fn helper() {}\n\n    #[tokio::test]\n    async fn later() {}\n}\n")
	write("src/Calc.java", "class Calc {\n  @Test\n  void adds() {}\n  void plain() {}\n}\n")
	c := &testCodeClassifier{root: root}
	sym := func(file, qn string, line int) grove.SymbolRecord {
		name := qn
		if i := len(qn) - 1; i >= 0 {
			for j := len(qn) - 1; j >= 0; j-- {
				if qn[j] == '.' {
					name = qn[j+1:]
					break
				}
			}
		}
		return grove.SymbolRecord{FilePath: file, Name: name, QualifiedName: qn, Kind: "function", Span: grove.SpanInfo{Start: line, End: line}}
	}
	cases := []struct {
		s    grove.SymbolRecord
		want string
	}{
		{sym("src/lib.rs", "add", 1), ""},
		{sym("src/lib.rs", "tests.adds", 6), "test"},
		{sym("src/lib.rs", "tests.helper", 8), "helper"},
		{sym("src/lib.rs", "tests.later", 11), "test"},
		{sym("src/Calc.java", "Calc.adds", 3), "test"},
		{sym("src/Calc.java", "Calc.plain", 4), ""},
		{sym("app/util.py", "test_parse", 1), "test"},
		{sym("app/util.py", "parse", 1), ""},
		{sym("pkg/a_test.go", "TestA", 1), "test"},
		{sym("pkg/a_test.go", "setup", 1), "helper"},
	}
	for _, tc := range cases {
		if got := c.kind(tc.s); got != tc.want {
			t.Errorf("%s %s: kind = %q, want %q", tc.s.FilePath, tc.s.QualifiedName, got, tc.want)
		}
	}
}

// A test one call from the change is focused; one reached only through a
// long chain is transitive, not dropped.
func TestPlanInventoryTiersByCallHops(t *testing.T) {
	s := func(id, file, name string) grove.SymbolRecord {
		return grove.SymbolRecord{ID: id, FilePath: file, Name: name, QualifiedName: name, Kind: "function", Language: "go"}
	}
	syms := []grove.SymbolRecord{
		s("seed", "a/a.go", "Changed"),
		s("near", "a/a_test.go", "TestNear"),
		s("m1", "b/b.go", "M1"), s("m2", "c/c.go", "M2"), s("m3", "d/d.go", "Dispatch"),
		s("far", "e/e_test.go", "TestFar"),
	}
	calls := func(from, to string) grove.Edge {
		return grove.Edge{From: from, To: to, Type: "calls", Source: "native", Confidence: 1}
	}
	edges := []grove.Edge{calls("near", "seed"), calls("m1", "seed"), calls("m2", "m1"), calls("m3", "m2"), calls("far", "m3")}
	r := testPlanResult{FileTargets: []testPlanTarget{}}
	planInventory(context.Background(), &r, syms, edges, map[string]bool{"seed": true})
	got := map[string]testPlanTarget{}
	for _, ft := range r.FileTargets {
		got[ft.File] = ft
	}
	if got["a/a_test.go"].Classification != "focused file" || got["a/a_test.go"].CallHops != 1 {
		t.Fatalf("near: %+v", got["a/a_test.go"])
	}
	if got["e/e_test.go"].Classification != "transitive file" || got["e/e_test.go"].CallHops != 4 {
		t.Fatalf("far: %+v", got["e/e_test.go"])
	}
}

func TestPlanGapHelpers(t *testing.T) {
	langs := planDiffLanguages(map[string]string{"pkg/a.go": "M"})
	if !planLanguageInDiff(langs, "go") || planLanguageInDiff(langs, "csharp") || !planLanguageInDiff(langs, "index") {
		t.Fatalf("language filter: %v", langs)
	}
	if got := planIDName("internal/view/view.go::docPath@02afe387"); got != "docPath (internal/view/view.go)" {
		t.Fatalf("planIDName = %q", got)
	}
}
