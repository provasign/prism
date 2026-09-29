package textsearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMatchGlobRipgrepSemantics(t *testing.T) {
	cases := []struct {
		glob, rel string
		want      bool
	}{
		{"**/types.py", "src/click/types.py", true},
		{"**/types.py", "types.py", true},
		{"**/types.py", "src/click/core.py", false},
		{"*.py", "src/click/types.py", true},
		{"types.py", "src/click/types.py", true},
		{"src/**/*.java", "src/main/java/A.java", true},
		{"src/**/*.java", "test/A.java", false},
		{"tests/**", "tests/unit/test_a.py", true},
		{"tests/**", "src/tests.py", false},
		{"render/*.go", "render/html.go", true},
		{"render/*.go", "render/sub/html.go", false},
		{"**/deser/**/*.java", "src/main/java/x/deser/bean/B.java", true},
		{"./src/", "src/a.go", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.glob, c.rel); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.glob, c.rel, got, c.want)
		}
	}
}

func TestCaseSensitiveOption(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("import \"errors\"\nc.Errors = nil\nerrors.New(\"x\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for bi, backend := range []func(Options) Result{
		func(o Options) Result { return Search(context.Background(), root, "Errors", o) },
		func(o Options) Result { return nativeSearch(context.Background(), root, "Errors", o.withDefaults()) },
	} {
		if r := backend(Options{CaseSensitive: true}); len(r.Hits) != 1 || r.Hits[0].Line != 2 {
			t.Fatalf("backend %d case-sensitive hits = %+v %+v", bi, r.Hits, r)
		}
		if r := backend(Options{}); len(r.Hits) != 3 {
			t.Fatalf("default must stay case-insensitive: %+v", r.Hits)
		}
	}
	if c := Count(context.Background(), root, "Errors", Options{CaseSensitive: true}); c.TotalHits != 1 {
		t.Fatalf("case-sensitive count = %d", c.TotalHits)
	}
}
