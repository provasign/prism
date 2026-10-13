package textsearch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// rg runs unsorted (parallel); results must still come back in rg's
// --sort path order, component by component.
func TestPathLessMatchesRgPathOrder(t *testing.T) {
	got := []string{"a-b/x.go", "a/z.go", "a/b/c.go", "b.go", "a.go", "A/q.go"}
	sort.Slice(got, func(i, j int) bool { return PathLess(got[i], got[j]) })
	want := []string{"A/q.go", "a/b/c.go", "a/z.go", "a-b/x.go", "a.go", "b.go"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The same capped search keeps the same hits on every run, in path order.
func TestCappedSearchIsDeterministicAndSorted(t *testing.T) {
	files := map[string]string{}
	for _, d := range []string{"zeta", "alpha", "a-b", "a", "mid"} {
		for _, f := range []string{"one.go", "two.go", "three.go"} {
			files[d+"/"+f] = "needle\nneedle\n"
		}
	}
	root := writeTree(t, files)
	var first []Hit
	for run := 0; run < 5; run++ {
		r := Search(context.Background(), root, "needle", Options{MaxHits: 7})
		if len(r.Hits) != 7 || !r.Truncated {
			t.Fatalf("run %d: %d hits truncated=%v", run, len(r.Hits), r.Truncated)
		}
		for i := 1; i < len(r.Hits); i++ {
			a, b := r.Hits[i-1], r.Hits[i]
			if PathLess(b.File, a.File) || (a.File == b.File && b.Line < a.Line) {
				t.Fatalf("run %d: hits out of path order: %+v", run, r.Hits)
			}
		}
		if run == 0 {
			first = r.Hits
			continue
		}
		for i := range first {
			if r.Hits[i].File != first[i].File || r.Hits[i].Line != first[i].Line {
				t.Fatalf("run %d differs from run 0:\n%+v\n%+v", run, r.Hits, first)
			}
		}
	}
}

// A folder the project lists in .groveignore is skipped by text search, as
// a .gitignore'd one is.
func TestSearchHonorsRootGroveIgnore(t *testing.T) {
	root := writeTree(t, map[string]string{
		".groveignore":        "exports/\n",
		"src/app.py":          "token = 1\n",
		"exports/dump.csv":    "token,token\n",
		"exports/deep/x.json": "{\"token\": 1}\n",
	})
	for _, opts := range []Options{{MaxHits: 50}, {MaxHits: 50, Exhaustive: true}} {
		r := Search(context.Background(), root, "token", opts)
		for _, h := range r.Hits {
			if filepath.Dir(h.File) != "src" {
				t.Fatalf("hit in a .groveignore'd folder: %+v", r.Hits)
			}
		}
		if len(r.Hits) != 1 {
			t.Fatalf("hits = %+v, want src/app.py only", r.Hits)
		}
	}
	c := Count(context.Background(), root, "token", Options{})
	if c.TotalHits != 1 {
		t.Fatalf("count = %d, want 1 (exports/ ignored)", c.TotalHits)
	}
}

func TestHeavyDirsRanksFoldersByBytes(t *testing.T) {
	if Backend() != "rg" {
		t.Skip("heavyDirs needs rg")
	}
	big := make([]byte, 50_000)
	for i := range big {
		big[i] = 'x'
	}
	root := writeTree(t, map[string]string{
		"exports/a.csv": string(big), "exports/b.csv": string(big),
		"src/app.py": "x = 1\n", "README.md": "hi\n",
	})
	got := heavyDirs(root, Options{})
	if len(got) == 0 || got[0].Dir != "exports/" || got[0].Files != 2 || got[0].Bytes != 100_000 {
		t.Fatalf("heavyDirs = %+v, want exports/ first with 2 files, 100000 bytes", got)
	}
}
