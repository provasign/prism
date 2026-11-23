package mcp

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// TestPlanTargets turns a test-plan answer into one target per line in
// each runner's own addressing (GAP-06): the focused and transitive tiers,
// not the run-for-safety set. Targets, never command lines: Prism does not
// hand agents ready-to-run commands.
//
//	go:     ./internal/session -run '^(TestTracker|TestDrift)$'
//	python: tests/test_shapes.py::TestShape::test_area
//	java:   ShapeTest#testArea
//	rust:   shapes::tests::area
//	other:  the test file
func TestPlanTargets(out any) []string {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	var plan struct {
		Candidates  []testPlanTarget `json:"candidates"`
		FileTargets []testPlanTarget `json:"fileTargets"`
	}
	if json.Unmarshal(raw, &plan) != nil {
		return nil
	}
	goRuns := map[string]map[string]bool{} // package dir -> test names
	seen := map[string]bool{}
	var targets []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	covered := map[string]bool{}
	for _, c := range plan.Candidates {
		if c.Symbol == nil {
			continue
		}
		s := c.Symbol
		file := normalizePath(s.File)
		name := s.Name
		switch {
		case s.Language == "go" || strings.HasSuffix(file, ".go"):
			leaf := name[strings.LastIndexByte(name, '.')+1:]
			if !strings.HasPrefix(leaf, "Test") && !strings.HasPrefix(leaf, "Benchmark") && !strings.HasPrefix(leaf, "Example") && !strings.HasPrefix(leaf, "Fuzz") {
				continue // a helper: its file still runs through the package
			}
			dir := "./" + path.Dir(file)
			if path.Dir(file) == "." {
				dir = "."
			}
			if goRuns[dir] == nil {
				goRuns[dir] = map[string]bool{}
			}
			goRuns[dir][leaf] = true
		case s.Language == "python" || strings.HasSuffix(file, ".py"):
			add(file + "::" + strings.ReplaceAll(name, ".", "::"))
		case s.Language == "java" || s.Language == "kotlin":
			if i := strings.LastIndexByte(name, '.'); i > 0 {
				cls := name[:i]
				cls = cls[strings.LastIndexByte(cls, '.')+1:]
				add(cls + "#" + name[i+1:])
			} else {
				add(file)
			}
		case s.Language == "rust" || strings.HasSuffix(file, ".rs"):
			mod := strings.TrimSuffix(strings.TrimPrefix(file, "src/"), ".rs")
			mod = strings.TrimSuffix(strings.TrimSuffix(mod, "/mod"), "lib")
			mod = strings.Trim(strings.ReplaceAll(mod, "/", "::"), ":")
			t := strings.ReplaceAll(name, ".", "::")
			if mod != "" && mod != "main" && !strings.HasPrefix(file, "tests/") {
				t = mod + "::" + t
			}
			add(t)
		default:
			add(file)
		}
		covered[file] = true
	}
	dirs := make([]string, 0, len(goRuns))
	for d := range goRuns {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		names := make([]string, 0, len(goRuns[d]))
		for n := range goRuns[d] {
			names = append(names, n)
		}
		sort.Strings(names)
		add(d + " -run '^(" + strings.Join(names, "|") + ")$'")
	}
	// File-level targets for tests the symbol pass did not name (JS/TS
	// runners take paths; any language without a symbol-level candidate).
	for _, f := range plan.FileTargets {
		file := normalizePath(f.File)
		if covered[file] || strings.HasSuffix(file, ".go") {
			continue
		}
		add(file)
	}
	return targets
}
