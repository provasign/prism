package mcp

import (
	"path"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// testPlanPackage is one package (directory) of the "run for safety" tier:
// it depends, directly or through other packages, on a changed file.
type testPlanPackage struct {
	Package string `json:"package"`
	// Hops: package dependency steps from the nearest changed package
	// (0 = a changed file is in this package).
	Hops      int      `json:"hops"`
	TestFiles []string `json:"testFiles"`
}

// planSafetyNotCovered lists what the package closure cannot see. It is
// printed with the tier, never only in documentation (MKT-08).
const planSafetyNotCovered = "tests chosen by a runner's name pattern rather than code dependency, tests that load code by reflection, " +
	"string or configuration (DI containers, plugin registries, dynamic imports), and integration or end-to-end tests outside this repository"

// planDependencyKinds are the edge types a package depends through. Every
// tier of evidence counts, heuristic included: this tier may over-select,
// it must not miss.
var planDependencyKinds = map[string]bool{
	"calls": true, "uses-type": true, "implements": true, "extends": true,
	"overrides": true, "imports": true, "reads": true, "writes": true,
}

// planSafety computes the "run for safety" tier: every test file in a
// package that transitively depends on a package holding a changed file,
// ordered by package distance. Static method-level selection is good for
// ordering and unsafe for selecting (MKT-08); package-level closure is the
// set test-plan can call complete over the indexed graph.
func planSafety(r *testPlanResult, syms []grove.SymbolRecord, edges []grove.Edge, tests *testCodeClassifier) {
	fileOf := make(map[string]string, len(syms))
	testFiles := map[string]bool{}
	for _, s := range syms {
		p := normalizePath(s.FilePath)
		fileOf[s.ID] = p
		if isTestFilePath(p) || tests.kind(s) == "test" {
			testFiles[p] = true
		}
	}
	resolve := func(id string) (string, bool) {
		if p, ok := fileOf[id]; ok {
			return p, true
		}
		if p, ok := strings.CutPrefix(id, "file:"); ok && p != "" {
			return normalizePath(p), true
		}
		return "", false
	}
	pkgOf := func(file string) string {
		d := path.Dir(file)
		if d == "." || d == "" {
			return "."
		}
		return d
	}
	dependents := map[string]map[string]bool{}
	for _, e := range edges {
		if !planDependencyKinds[e.Type] {
			continue
		}
		from, okF := resolve(e.From)
		to, okT := resolve(e.To)
		if !okF || !okT {
			continue
		}
		fp, tp := pkgOf(from), pkgOf(to)
		if fp == tp {
			continue
		}
		if dependents[tp] == nil {
			dependents[tp] = map[string]bool{}
		}
		dependents[tp][fp] = true
	}
	hops := map[string]int{}
	var queue []string
	for _, f := range r.ChangedFiles {
		p := pkgOf(normalizePath(f))
		if _, seen := hops[p]; !seen {
			hops[p] = 0
			queue = append(queue, p)
		}
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		next := make([]string, 0, len(dependents[p]))
		for d := range dependents[p] {
			if _, seen := hops[d]; !seen {
				next = append(next, d)
			}
		}
		sort.Strings(next)
		for _, d := range next {
			hops[d] = hops[p] + 1
			queue = append(queue, d)
		}
	}
	byPkg := map[string][]string{}
	for f := range testFiles {
		if _, ok := hops[pkgOf(f)]; ok {
			byPkg[pkgOf(f)] = append(byPkg[pkgOf(f)], f)
		}
	}
	r.Safety = []testPlanPackage{}
	r.SafetyFileCount = 0
	for p, files := range byPkg {
		sort.Strings(files)
		r.Safety = append(r.Safety, testPlanPackage{Package: p, Hops: hops[p], TestFiles: files})
		r.SafetyFileCount += len(files)
	}
	sort.Slice(r.Safety, func(i, j int) bool {
		if r.Safety[i].Hops != r.Safety[j].Hops {
			return r.Safety[i].Hops < r.Safety[j].Hops
		}
		return r.Safety[i].Package < r.Safety[j].Package
	})
	if len(r.Safety) > 0 {
		r.SafetyNotCovered = planSafetyNotCovered
	}
}
