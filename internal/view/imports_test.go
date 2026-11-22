package view

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// prism cli -> version (2026-11-22): the only use was a package variable,
// so the dependency existed only as an import between files, and file
// endpoints were dropped. arch --deny 'internal/cli -> internal/version'
// passed while commands.go:214 read version.Version.
func TestBuild_ImportOnlyDependencyIsInduced(t *testing.T) {
	symbols := []grove.SymbolRecord{
		{ID: "internal/cli/commands.go::Run@1", Name: "Run", Kind: "function", FilePath: "internal/cli/commands.go", Span: grove.SpanInfo{Start: 200, End: 260}},
		{ID: "internal/cli/commands_test.go::TestRun@1", Name: "TestRun", Kind: "function", FilePath: "internal/cli/commands_test.go"},
		{ID: "internal/cli/root.go::root@1", Name: "root", Kind: "function", FilePath: "internal/cli/root.go"},
		{ID: "internal/version/version.go::Version@1", Name: "Version", Kind: "variable", FilePath: "internal/version/version.go"},
		{ID: "internal/version/build.go::Build@1", Name: "Build", Kind: "variable", FilePath: "internal/version/build.go"},
	}
	edges := []grove.Edge{
		// One import names both library files of the package.
		{From: "file:internal/cli/commands.go", To: "file:internal/version/version.go", Type: "imports", Source: "native", Lines: "9:2"},
		{From: "file:internal/cli/commands.go", To: "file:internal/version/build.go", Type: "imports", Source: "native", Lines: "9:2"},
		// A test file's import does not count in the production view.
		{From: "file:internal/cli/commands_test.go", To: "file:internal/version/version.go", Type: "imports", Source: "native", Lines: "5:2"},
		// A variable read records its own position.
		{From: "internal/cli/commands.go::Run@1", To: "internal/version/version.go::Version@1", Type: "reads", Source: "native", Lines: "214:22"},
	}
	v := Build(symbols, edges, Options{Depth: 2})
	e := v.Edge("internal/cli", "internal/version")
	if e == nil {
		t.Fatalf("no internal/cli -> internal/version edge: %+v", v.Edges)
	}
	if e.Kinds["imports"] != 1 || e.Kinds["reads"] != 1 || e.Weight != 2 {
		t.Fatalf("kinds = %v weight = %d, want one import and one read", e.Kinds, e.Weight)
	}
	lines := map[string]int{}
	for _, s := range e.Sites {
		lines[s.Kind] = s.FromLine
		if s.FromFile != "internal/cli/commands.go" {
			t.Fatalf("site from %s, want commands.go (the test file's import is excluded)", s.FromFile)
		}
	}
	if lines["imports"] != 9 || lines["reads"] != 214 {
		t.Fatalf("site lines = %v, want the import line 9 and the read at 214, not Run's declaration (200)", lines)
	}

	// With tests included, the test file's import counts too.
	if e := Build(symbols, edges, Options{Depth: 2, IncludeTests: true}).Edge("internal/cli", "internal/version"); e == nil || e.Kinds["imports"] != 2 {
		t.Fatalf("with tests: %+v, want two imports", e)
	}
}

func TestSiteLine(t *testing.T) {
	for lines, want := range map[string]int{"136:38": 136, "136:38,140:2": 136, "Close@74:18": 74, "": 7, "x": 7} {
		if got := siteLine(lines, 7); got != want {
			t.Errorf("siteLine(%q) = %d, want %d", lines, got, want)
		}
	}
}
