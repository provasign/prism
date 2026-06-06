// Package grove — fixture-based call-graph correctness tests.
// These verify that Impact() achieves high recall (finds actual callers) and
// high precision (no false positives) using a deterministic synthetic
// multi-package Go codebase instead of an external directory.
//
// Caller relationships in the fixture (fixed and known):
//
//	internal/calc/add.go        — func Add(a, b int) int
//	internal/calc/mul.go        — func Mul(a, b int) int  (never calls Add)
//	internal/calc/calc_test.go  — TestAdd calls Add; TestMul calls Mul
//	internal/render/render.go   — Format calls calc.Add
//	cmd/main.go                 — Run calls render.Format
//	cmd/direct.go               — Direct calls calc.Add
//	noise/noise.go              — Noise() { }  (calls nothing)
package grove

import (
	"context"
	"path/filepath"
	"testing"
)

// buildGraphFixture writes the synthetic fixture to t.TempDir() and returns
// the root directory. Files are real Go source so Grove's parser can index them.
func buildGraphFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module prismfixture\n\ngo 1.21\n",
		"internal/calc/add.go": `package calc

func Add(a, b int) int { return a + b }
`,
		"internal/calc/mul.go": `package calc

func Mul(a, b int) int { return a * b }
`,
		"internal/calc/calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("wrong")
	}
}

func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatal("wrong")
	}
}
`,
		"internal/render/render.go": `package render

import "prismfixture/internal/calc"

func Format(n int) int { return calc.Add(n, 0) }
`,
		"cmd/main.go": `package main

import "prismfixture/internal/render"

func Run() int { return render.Format(42) }
`,
		"cmd/direct.go": `package main

import "prismfixture/internal/calc"

func Direct() int { return calc.Add(1, 1) }
`,
		"noise/noise.go": `package noise

func Noise() {}
`,
	}
	for rel, content := range files {
		writePrismFile(t, dir, rel, content)
	}
	return dir
}

func setupGroveClient(t *testing.T, dir string) (*Client, context.Context) {
	t.Helper()
	ctx := context.Background()
	c := NewClient("", "").WithTokenFromDir(dir)
	if err := c.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	t.Cleanup(c.Shutdown)
	if _, err := c.Index(ctx, dir); err != nil {
		t.Fatalf("Index: %v", err)
	}
	return c, ctx
}

// TestGraphRecall verifies that Impact() achieves 100% recall on the fixture:
// every file known to call (or transitively call) a seed must appear in results.
// Covers both same-package edges (no import) and cross-package edges (with import).
func TestGraphRecall(t *testing.T) {
	dir := buildGraphFixture(t)
	c, ctx := setupGroveClient(t, dir)

	checks := []struct {
		seed            string
		wantCallerFiles []string // must all appear in Impact() results
		noiseFiles      []string // must not appear in Impact() results
	}{
		{
			seed: "Add",
			wantCallerFiles: []string{
				"internal/calc/calc_test.go", // same-package test (TestAdd calls Add without import)
				"internal/render/render.go",  // cross-package direct call (Format → calc.Add)
				"cmd/direct.go",             // cross-package direct call (Direct → calc.Add)
				"cmd/main.go",              // transitive: Run → render.Format → calc.Add (depth 2)
			},
			noiseFiles: []string{
				"noise/noise.go",
				"internal/calc/mul.go", // same package as Add but never calls it
			},
		},
		{
			seed: "Mul",
			wantCallerFiles: []string{
				"internal/calc/calc_test.go", // same-package test (TestMul calls Mul without import)
			},
			noiseFiles: []string{
				"noise/noise.go",
				"cmd/main.go",               // calls Format, not Mul
				"internal/render/render.go", // calls Add, not Mul
				"cmd/direct.go",            // calls Add, not Mul
			},
		},
	}

	for _, tc := range checks {
		t.Run(tc.seed, func(t *testing.T) {
			impacted, err := c.Impact(ctx, tc.seed, 4)
			if err != nil {
				t.Fatalf("Impact(%q): %v", tc.seed, err)
			}
			found := make(map[string]bool, len(impacted))
			for _, sym := range impacted {
				found[filepath.ToSlash(sym.FilePath)] = true
			}

			hit := 0
			for _, f := range tc.wantCallerFiles {
				if found[f] {
					hit++
				} else {
					t.Errorf("recall miss: %q not in Impact(%q)", f, tc.seed)
				}
			}
			t.Logf("recall = %d/%d (%.0f%%)", hit, len(tc.wantCallerFiles),
				100*float64(hit)/float64(len(tc.wantCallerFiles)))

			for _, f := range tc.noiseFiles {
				if found[f] {
					t.Errorf("precision error: noise file %q appeared in Impact(%q)", f, tc.seed)
				}
			}
		})
	}
}

// TestGraphPrecision verifies that Impact() produces no false positives:
// every file in the result must be a known caller, transitive caller, or the
// definition file itself.
func TestGraphPrecision(t *testing.T) {
	dir := buildGraphFixture(t)
	c, ctx := setupGroveClient(t, dir)

	// All files legitimately reachable from Add via the call graph.
	addAllowed := map[string]bool{
		"internal/calc/add.go":      true, // definition (BFS start) — allowed if present
		"internal/calc/calc_test.go": true, // TestAdd calls Add
		"internal/render/render.go": true, // Format calls Add
		"cmd/direct.go":             true, // Direct calls Add
		"cmd/main.go":               true, // Run → Format → Add (depth 2)
	}

	impacted, err := c.Impact(ctx, "Add", 4)
	if err != nil {
		t.Fatalf("Impact(Add): %v", err)
	}
	if len(impacted) == 0 {
		t.Skip("Impact returned no results — is the index populated?")
	}

	truePos, falsePos := 0, 0
	for _, sym := range impacted {
		fp := filepath.ToSlash(sym.FilePath)
		if addAllowed[fp] {
			truePos++
		} else {
			falsePos++
			t.Errorf("false positive: %s in %s is not a caller of Add", sym.Name, fp)
		}
	}
	precision := float64(truePos) / float64(truePos+falsePos)
	t.Logf("precision = %d/%d (%.0f%%)", truePos, truePos+falsePos, 100*precision)
	if precision < 0.80 {
		t.Errorf("precision %.0f%% below 80%% threshold", 100*precision)
	}
}
