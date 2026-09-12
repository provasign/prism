package textsearch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func adaptiveFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, start, count int) {
		t.Helper()
		var body strings.Builder
		body.WriteString("package fixture\n\nfunc F() {\n")
		for i := 0; i < count; i++ {
			needle := "Phase4Needle"
			if i%2 == 1 {
				needle = "phase4needle"
			}
			fmt.Fprintf(&body, "\t// %s %02d\n", needle, start+i)
		}
		body.WriteString("}\n")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.go", 1, 22)
	write("beta.go", 23, 20)
	return dir
}

func assertExactAdaptiveCount(t *testing.T, name string, r CountResult, ok bool) {
	t.Helper()
	if !ok {
		t.Fatalf("%s count invocation failed", name)
	}
	if !r.Complete || r.TimedOut {
		t.Fatalf("%s count incomplete: %+v", name, r)
	}
	if r.TotalHits != 42 || r.FilesMatched != 2 {
		t.Fatalf("%s count = %d hits in %d files, want 42 in 2: %+v",
			name, r.TotalHits, r.FilesMatched, r)
	}
	if len(r.Files) != 2 || r.Files[0].Count != 22 || r.Files[1].Count != 20 {
		t.Fatalf("%s per-file counts = %+v, want 22 and 20", name, r.Files)
	}
}

func TestCountBackendsReturnExactMatchingLineCounts(t *testing.T) {
	dir := adaptiveFixture(t)
	opts := Options{Timeout: 5 * time.Second}.withDefaults()
	native := nativeCount(context.Background(), dir, "Phase4Needle", opts)
	assertExactAdaptiveCount(t, "native", native, true)
	if _, err := exec.LookPath("rg"); err == nil {
		r, ok := runRgCount(context.Background(), dir, "Phase4Needle", opts)
		assertExactAdaptiveCount(t, "rg", r, ok)
	}
	if _, err := exec.LookPath("grep"); err == nil {
		r, ok := runGrepCount(context.Background(), dir, "Phase4Needle", opts)
		assertExactAdaptiveCount(t, "grep", r, ok)
	}
}

func TestSearchAdaptiveReturnsCompleteSmallResult(t *testing.T) {
	dir := adaptiveFixture(t)
	r := Search(context.Background(), dir, "Phase4Needle", Options{
		MaxHits: 25, MaxPerFile: 20, Timeout: 5 * time.Second, Adaptive: true,
	})
	if !r.CountComplete || !r.ResultsComplete || r.Truncated {
		t.Fatalf("adaptive result is not complete: %+v", r)
	}
	if r.TotalHits != 42 || r.FilesMatched != 2 || len(r.Hits) != 42 {
		t.Fatalf("adaptive search = %d/%d hits in %d files, want 42/42 in 2",
			len(r.Hits), r.TotalHits, r.FilesMatched)
	}
}

func TestSearchAdaptiveReturnsCompletePayloadAboveFormerCountThreshold(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "many.txt"),
		[]byte(strings.Repeat("ShortNeedle\n", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "ShortNeedle", Options{
		MaxHits: 25, MaxPerFile: 20, Timeout: 5 * time.Second, Adaptive: true,
	})
	if !r.CountComplete || !r.ResultsComplete || r.Truncated {
		t.Fatalf("small-payload adaptive result is not complete: %+v", r)
	}
	if r.TotalHits != 100 || len(r.Hits) != 100 {
		t.Fatalf("small-payload adaptive search = %d/%d hits, want 100/100", len(r.Hits), r.TotalHits)
	}
}

func TestSearchAdaptiveCompletesShortInventoryAboveTwoHundredHits(t *testing.T) {
	for _, count := range []int{210, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a"), []byte(strings.Repeat("X\n", count)), 0o644); err != nil {
				t.Fatal(err)
			}
			r := Search(context.Background(), dir, "X", Options{
				MaxHits: 25, MaxPerFile: 20, Timeout: 5 * time.Second, Context: 2, Adaptive: true,
			})
			if !r.CountComplete || !r.ResultsComplete || r.Truncated || r.TotalHits != count || len(r.Hits) != count {
				t.Fatalf("short %d-hit inventory should fit the token budget: %+v", count, r)
			}
		})
	}
}

func TestSearchAdaptiveKeepsLargeResultBoundedWithExactCount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "many.txt"),
		[]byte(strings.Repeat("LargeNeedle "+strings.Repeat("long line ", 100)+"\n", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Search(context.Background(), dir, "LargeNeedle", Options{
		MaxHits: 25, MaxPerFile: 20, Timeout: 5 * time.Second, Context: 2, Adaptive: true,
	})
	if !r.CountComplete || r.ResultsComplete || !r.Truncated {
		t.Fatalf("large adaptive result has wrong completeness: %+v", r)
	}
	if r.TotalHits != 100 || r.FilesMatched != 1 {
		t.Fatalf("large adaptive count = %d in %d files, want 100 in 1", r.TotalHits, r.FilesMatched)
	}
	if len(r.Hits) != 25 {
		t.Fatalf("large adaptive sample has %d hits, want explicit cap 25", len(r.Hits))
	}
}
