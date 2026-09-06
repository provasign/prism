package textsearch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScopedDotPathsMatchUnscopedPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "calls.go"), []byte("package p\nfunc Call() { Needle() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"rg", "grep", "native"} {
		t.Run(backend, func(t *testing.T) {
			if backend != "native" && bin(backend) == "" {
				t.Skip(backend + " unavailable")
			}
			search := func(paths []string) Result {
				opts := (Options{Paths: paths}).withDefaults()
				var result Result
				ok := true
				switch backend {
				case "rg":
					result, ok = runRg(t.Context(), dir, "Needle", opts)
				case "grep":
					result, ok = runGrep(t.Context(), dir, "Needle", opts)
				default:
					result = nativeSearch(t.Context(), dir, "Needle", opts)
				}
				if !ok || len(result.Hits) != 1 || result.Hits[0].File != "sub/calls.go" {
					t.Fatalf("non-canonical scoped path %v: %+v", paths, result)
				}
				return result
			}
			want := search(nil)
			for _, paths := range [][]string{{"."}, {"./."}, {"sub/.."}, {"./sub"}} {
				if got := search(paths); !reflect.DeepEqual(got, want) {
					t.Fatalf("scoped paths changed hits: got %+v, want %+v", got, want)
				}
			}
		})
	}
}
