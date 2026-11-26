package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

const testPlanBoundary = "Static graph candidates, not measured test coverage. Tests are identified by file-path conventions and by test attributes (Rust #[test], Python test_*, JUnit @Test); other framework tests may not be recognized. Run-first tiers follow resolved call paths and order tests; the run-for-safety tier is the package-level dependency closure and is the set to treat as complete over the indexed graph. Runtime dispatch, reflection, external consumers, and dependencies absent from the index may be missed. Choose test commands and broader validation using these gaps and repository requirements."

// These records intentionally omit source bodies and runner-specific selectors.
type testPlanSymbol struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"language"`
	Kind     string `json:"kind"`
}

type testPlanStep struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Relation   string  `json:"relation"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type testPlanTarget struct {
	Symbol         *testPlanSymbol `json:"symbol,omitempty"`
	File           string          `json:"file"`
	Classification string          `json:"classification"`
	Origins        []string        `json:"origins"`
	Evidence       []testPlanStep  `json:"evidence"`
	Reason         string          `json:"reason,omitempty"`
	// CallHops: call, type-use and inheritance steps on the evidence
	// path from the nearest changed symbol (structural steps not counted).
	CallHops int `json:"callHops"`
	// Sources: what selected the file: "graph", "coverage", or both.
	Sources []string `json:"sources,omitempty"`
	// seedHops: per changed symbol, the call hops on its shortest path.
	seedHops map[string]int
}

type testPlanResult struct {
	Base           string           `json:"base"`
	Status         string           `json:"status"`
	ChangedFiles   []string         `json:"changedFiles"`
	ChangedSymbols []testPlanSymbol `json:"changedSymbols"`
	Candidates     []testPlanTarget `json:"candidates"`
	FileTargets    []testPlanTarget `json:"fileTargets"`
	Gaps           []string         `json:"gaps"`
	Boundary       string           `json:"boundary"`
	// LinkedTests: test files in linked repositories that reference a
	// changed symbol, with the lines that do. LinkedScope says what was
	// searched there.
	LinkedTests []string `json:"linkedTests,omitempty"`
	LinkedScope string   `json:"linkedScope,omitempty"`
	// Safety: the "run for safety" tier, every test file in a package
	// that transitively depends on a changed file, by package distance.
	Safety          []testPlanPackage `json:"safety"`
	SafetyFileCount int               `json:"safetyFileCount"`
	// SafetyNotCovered: what the package closure cannot see.
	SafetyNotCovered string `json:"safetyNotCovered,omitempty"`
	// Coverage: what recorded coverage files say about the changed lines.
	Coverage []planCoverage `json:"coverage,omitempty"`
	// root: the repository root, for reading test attributes.
	root string
	// changes: changed symbols' spans on both sides, for coverage.
	changes []planChange
}

func planSymbol(s grove.SymbolRecord) testPlanSymbol {
	return testPlanSymbol{ID: s.ID, Name: displayQN(s), File: normalizePath(s.FilePath), Line: s.Span.Start, Language: s.Language, Kind: s.Kind}
}

// Use NUL-delimited paths and disable rename detection: both sides of a move
// participate, and unusual Git path names do not get quoted or split on lines.
func testPlanChanges(ctx context.Context, root, base string) (map[string]string, error) {
	if !validGitBase(base) {
		return nil, fmt.Errorf("invalid git base ref")
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-status", "-z", "--no-renames", "--end-of-options", base, "--", ".").Output()
	if err != nil {
		return nil, gitDiffError(root, base, err)
	}
	prefix := gitPrefix(root)
	changes := map[string]string{}
	parts := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		changes[strings.TrimPrefix(parts[i+1], prefix)] = parts[i]
	}
	out, err = exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z", "--", ".").Output()
	if err != nil {
		return nil, fmt.Errorf("git untracked files: %w", err)
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			changes[strings.TrimPrefix(p, prefix)] = "A"
		}
	}
	for p := range changes {
		// Bytecode caches are build output, not changes (an unignored
		// __pycache__ listed .pyc files as changed tests).
		if p == ".grove" || strings.HasPrefix(p, ".grove/") || strings.Contains("/"+p, "/__pycache__/") || strings.HasSuffix(p, ".pyc") {
			delete(changes, p)
		}
	}
	return changes, nil
}

// Go's syntax tree without comments avoids selecting tests for formatting and
// comment edits. Other languages retain the structural differ's decisions.
func planGoSyntax(content []byte) (string, bool) {
	// Comments can carry compiler/build instructions; cgo also consumes a
	// comment preamble. Do not classify those edits as ordinary comments.
	if bytes.Contains(content, []byte(`"C"`)) {
		return "", false
	}
	var directives []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:") || strings.HasPrefix(line, "// +build") || strings.HasPrefix(line, "//export ") {
			directives = append(directives, line)
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "input.go", content, 0)
	if err != nil {
		return "", false
	}
	var b bytes.Buffer
	if err = printer.Fprint(&b, token.NewFileSet(), f); err != nil {
		return "", false
	}
	return b.String() + strings.Join(directives, "\n"), true
}

func (h *Handler) toolTestPlan(ctx context.Context, args map[string]any) (any, error) {
	if v, ok := args["base"]; ok {
		if _, valid := v.(string); !valid {
			return nil, fmt.Errorf("test-plan: base must be a Git ref string")
		}
	}
	base := stringArg(args, "base", "HEAD")
	changes, err := testPlanChanges(ctx, h.Root, base)
	if err != nil {
		return nil, err
	}
	r := testPlanResult{Base: base, Status: "no changes", ChangedFiles: []string{}, ChangedSymbols: []testPlanSymbol{}, Candidates: []testPlanTarget{}, FileTargets: []testPlanTarget{}, Gaps: []string{}, Boundary: testPlanBoundary, root: h.Root}
	for p := range changes {
		r.ChangedFiles = append(r.ChangedFiles, p)
	}
	sort.Strings(r.ChangedFiles)
	if len(changes) == 0 {
		return testPlanMap(r), nil
	}
	r.Status = "no candidates found"
	indexed, err := h.Grove.Index(ctx, h.Root)
	if err != nil {
		r.Gaps = append(r.Gaps, "index refresh failed: "+err.Error())
	} else {
		r.Gaps = append(r.Gaps, indexed.Errors...)
	}
	// Status retains the last analyzer diagnostics across no-op refreshes;
	// the Index result alone can merely say "no file changes" on a repeat.
	status, statusErr := h.Grove.Status(ctx)
	if statusErr != nil {
		r.Gaps = append(r.Gaps, "analysis status unavailable: "+statusErr.Error())
	} else {
		// Only the languages this diff touches: a C# analyzer note on a Go
		// change is noise (BUG-15).
		langs := planDiffLanguages(changes)
		for _, issue := range status.Readiness {
			if !planLanguageInDiff(langs, issue.Language) {
				continue
			}
			r.Gaps = append(r.Gaps, fmt.Sprintf("analysis readiness (%s): %s; %s", issue.Language, issue.Problem, issue.Fix))
		}
		for _, note := range status.Native {
			if name, _, ok := strings.Cut(note, ":"); ok && !planLanguageInDiff(langs, name) {
				continue
			}
			lower := strings.ToLower(note)
			if strings.Contains(lower, "no file changes") || strings.Contains(lower, "no changed files in its languages") || strings.Contains(lower, "not applicable") {
				continue
			}
			if strings.Contains(lower, "skipped") || strings.Contains(lower, "failed") || strings.Contains(lower, "error") {
				r.Gaps = append(r.Gaps, "native analysis: "+note)
			}
		}
	}
	syms, edges, err := h.Grove.SnapshotGraph(ctx)
	if err != nil {
		return nil, fmt.Errorf("test-plan graph snapshot: %w", err)
	}
	byFile := map[string][]grove.SymbolRecord{}
	for _, s := range syms {
		byFile[normalizePath(s.FilePath)] = append(byFile[normalizePath(s.FilePath)], s)
	}
	seeds := map[string]bool{}
	addSeed := func(s grove.SymbolRecord) {
		if s.Kind == "document" || s.Kind == "file" {
			return
		}
		if !seeds[s.ID] {
			seeds[s.ID] = true
			r.ChangedSymbols = append(r.ChangedSymbols, planSymbol(s))
		}
	}
	for _, p := range r.ChangedFiles {
		if err := ctx.Err(); err != nil {
			r.Gaps = append(r.Gaps, "change analysis incomplete: "+err.Error())
			break
		}
		status := changes[p]
		var before []grove.SymbolRecord
		var old []byte
		if status != "A" {
			var e error
			old, e = exec.CommandContext(ctx, "git", "-C", h.Root, "show", "--end-of-options", base+":"+gitPrefix(h.Root)+p).Output()
			if e != nil {
				r.Gaps = append(r.Gaps, p+": cannot read base content: "+e.Error())
				continue
			}
			before, e = h.Grove.PreviewFileSymbols(p, old)
			if e != nil {
				r.Gaps = append(r.Gaps, p+": cannot parse base content: "+e.Error())
				continue
			}
		}
		if status == "D" {
			for _, s := range before {
				r.ChangedSymbols = append(r.ChangedSymbols, planSymbol(s))
			}
			r.Gaps = append(r.Gaps, p+": deleted file; base graph dependencies unavailable, use broader validation")
			continue
		}
		content, e := os.ReadFile(filepath.Join(h.Root, filepath.FromSlash(p)))
		if e != nil {
			r.Gaps = append(r.Gaps, p+": cannot read working file: "+e.Error())
			continue
		}
		if strings.HasSuffix(p, ".go") && status != "A" {
			a, aok := planGoSyntax(old)
			b, bok := planGoSyntax(content)
			if aok && bok && a == b {
				continue
			}
		}
		if isTestFilePath(p) {
			r.FileTargets = append(r.FileTargets, testPlanTarget{File: p, Classification: "file", Origins: []string{p}, Evidence: []testPlanStep{}, Reason: "directly changed test file"})
		}
		fd, e := h.Grove.DiffFileContent(before, p, content)
		if e != nil {
			r.Gaps = append(r.Gaps, p+": structural diff failed: "+e.Error())
			continue
		}
		start := len(r.ChangedSymbols)
		span := func(s *grove.SymbolRecord) *[2]int {
			if s == nil || s.Kind == "document" || s.Kind == "file" {
				return nil
			}
			return &[2]int{s.Span.Start, s.Span.End}
		}
		record := func(before, after *grove.SymbolRecord) {
			name := ""
			for _, s := range []*grove.SymbolRecord{after, before} {
				if s != nil && name == "" {
					name = displayQN(*s)
				}
			}
			if b, a := span(before), span(after); b != nil || a != nil {
				r.changes = append(r.changes, planChange{File: p, Name: name, Before: b, After: a})
			}
		}
		for _, s := range fd.Added {
			addSeed(s)
			record(nil, &s)
		}
		for _, c := range fd.Changed {
			if c.After != nil {
				addSeed(*c.After)
			}
			record(c.Before, c.After)
		}
		for _, c := range fd.Renamed {
			record(c.Before, c.After)
			if c.After != nil {
				addSeed(*c.After)
			}
			if c.Before != nil {
				r.ChangedSymbols = append(r.ChangedSymbols, planSymbol(*c.Before))
				r.Gaps = append(r.Gaps, p+": renamed "+displayQN(*c.Before)+"; old graph dependencies unavailable")
			}
		}
		for _, s := range fd.Removed {
			record(&s, nil)
			r.ChangedSymbols = append(r.ChangedSymbols, planSymbol(s))
			r.Gaps = append(r.Gaps, p+": removed "+displayQN(s)+"; old graph dependencies unavailable")
		}
		codeSymbols := 0
		for _, s := range byFile[p] {
			if s.Kind != "document" && s.Kind != "file" {
				codeSymbols++
			}
		}
		if codeSymbols == 0 {
			r.Gaps = append(r.Gaps, p+": no indexed symbols; configuration, fixtures, generated inputs, or excluded files need broader validation")
		} else if len(r.ChangedSymbols) == start {
			r.Gaps = append(r.Gaps, p+": change outside attributable symbols; review file-level dependencies")
		}
	}
	planInventory(ctx, &r, syms, edges, seeds)
	planSafety(&r, syms, edges, &testCodeClassifier{root: h.Root})
	planApplyCoverage(&r, h.Root, stringsArg(args, "coverage"), syms)
	r.Gaps = uniqueSorted(r.Gaps)
	sort.Slice(r.ChangedSymbols, func(i, j int) bool { return planSymbolKey(r.ChangedSymbols[i]) < planSymbolKey(r.ChangedSymbols[j]) })
	if len(r.Candidates)+len(r.FileTargets)+r.SafetyFileCount > 0 {
		r.Status = "candidates found"
	}
	h.planLinkedTests(ctx, &r)
	return testPlanMap(r), nil
}

// planLinkedTests lists the tests in linked repositories that reference a
// changed symbol. Their runners live in those repositories; the plan names
// the files and leaves the commands to the reader, as for local targets.
func (h *Handler) planLinkedTests(ctx context.Context, r *testPlanResult) {
	if !h.workspaceConfigured() {
		return
	}
	var targets []linkTarget
	for _, s := range r.ChangedSymbols {
		targets = append(targets, targetFor(h.Root, s.Language, s.Kind, s.File, s.Name))
	}
	lines, scope, _ := h.linkedConsumers(ctx, targets)
	r.LinkedScope = scope
	other := 0
	var header string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "// repo "):
			header = l
			if strings.Contains(l, "NOT SEARCHED") || strings.Contains(l, "STALE") || strings.Contains(l, "NAME-MATCHED ONLY") {
				r.LinkedTests = append(r.LinkedTests, l)
				header = ""
			}
		case strings.Contains(l, "[test]"):
			if header != "" {
				r.LinkedTests = append(r.LinkedTests, header)
				header = ""
			}
			r.LinkedTests = append(r.LinkedTests, l)
		default:
			other++
		}
	}
	if other > 0 {
		r.Gaps = uniqueSorted(append(r.Gaps, fmt.Sprintf("%d non-test site(s) in linked repositories reference a changed symbol; the tests of that code in those repositories are not traced from here (change-impact lists the sites)", other)))
	}
}

func testPlanMap(r testPlanResult) map[string]any {
	// Conversion gives the shared CLI/MCP render path its conventional map shape.
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func planSymbolKey(s testPlanSymbol) string {
	return fmt.Sprintf("%s:%09d:%s:%s", s.File, s.Line, s.Name, s.ID)
}

func uniqueSorted(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

type planArc struct {
	next string
	step testPlanStep
}

// All traversals use graph IDs. A name is presentation, never a lookup key.
func planTraverse(ctx context.Context, r *testPlanResult, syms []grove.SymbolRecord, edges []grove.Edge, seeds map[string]bool) {
	tests := &testCodeClassifier{root: r.root}
	nodes := map[string]grove.SymbolRecord{}
	files := map[string][]string{}
	for _, s := range syms {
		nodes[s.ID] = s
		files[normalizePath(s.FilePath)] = append(files[normalizePath(s.FilePath)], s.ID)
	}
	adj := map[string][]planArc{}
	add := func(from, to, relation, source string, confidence float64) {
		adj[from] = append(adj[from], planArc{to, testPlanStep{From: from, To: to, Relation: relation, Source: source, Confidence: confidence}})
	}
	// File hubs keep coarse import propagation linear in symbols and edges,
	// rather than constructing every pair of importing/imported symbols.
	fileID := func(path string) string { return "file:" + path }
	dependencyID := func(path string) string { return "dependencies:" + path }
	for p, ids := range files {
		id := fileID(p)
		nodes[id] = grove.SymbolRecord{ID: id, Name: p, FilePath: p, Kind: "file"}
		dep := dependencyID(p)
		nodes[dep] = grove.SymbolRecord{ID: dep, Name: p, FilePath: p, Kind: "file"}
		for _, symbol := range ids {
			add(symbol, dep, "in-file", "structural", 1)
			add(id, symbol, "file-member", "structural", 1)
		}
	}
	unresolvedImports := 0
	for _, e := range edges {
		switch e.Type {
		case "calls", "uses-type":
			add(e.To, e.From, e.Type+" (in)", e.Source, e.Confidence)
		case "implements", "extends", "overrides":
			add(e.To, e.From, e.Type+" (in)", e.Source, e.Confidence)
			add(e.From, e.To, e.Type+" (out)", e.Source, e.Confidence)
		case "contains", "defines":
			add(e.From, e.To, e.Type, e.Source, e.Confidence)
		case "imports":
			// Imports are coarse: reach every symbol in the importing file.
			from, fok := nodes[e.From]
			to, tok := nodes[e.To]
			if fok && tok {
				add(dependencyID(normalizePath(to.FilePath)), fileID(normalizePath(from.FilePath)), "imports (file)", e.Source, e.Confidence)
			} else {
				unresolvedImports++
			}
		}
	}
	if unresolvedImports > 0 {
		r.Gaps = append(r.Gaps, fmt.Sprintf("%d import edges have external or unresolved targets; file-level dependency propagation does not cover them", unresolvedImports))
	}
	for id := range adj {
		sort.Slice(adj[id], func(i, j int) bool {
			a, b := adj[id][i], adj[id][j]
			ak, bk := a.next+":"+a.step.Relation+":"+a.step.Source, b.next+":"+b.step.Relation+":"+b.step.Source
			if ak == bk {
				return a.step.Confidence > b.step.Confidence
			}
			return ak < bk
		})
	}
	ids := make([]string, 0, len(seeds))
	for id := range seeds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	targets := map[string]*testPlanTarget{}
	fileTargets := map[string]*testPlanTarget{}
	for i := range r.FileTargets {
		t := r.FileTargets[i]
		fileTargets[t.File] = &t
	}
	for _, seed := range ids {
		if _, ok := nodes[seed]; !ok {
			r.Gaps = append(r.Gaps, "changed symbol absent from refreshed graph: "+planIDName(seed))
			continue
		}
		depth := map[string]int{seed: 0}
		parents := map[string]testPlanStep{}
		pathTo := func(id string) []testPlanStep {
			path := make([]testPlanStep, depth[id])
			for i := len(path) - 1; i >= 0; i-- {
				path[i] = parents[id]
				id = path[i].From
			}
			return path
		}
		queue := []string{seed}
		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				r.Gaps = append(r.Gaps, "graph traversal incomplete: "+err.Error())
				goto done
			}
			id := queue[0]
			queue = queue[1:]
			s, ok := nodes[id]
			if !ok {
				r.Gaps = append(r.Gaps, "unresolved graph endpoint: "+planIDName(id))
				continue
			}
			p := normalizePath(s.FilePath)
			kind := tests.kind(s)
			if isTestFilePath(p) || kind != "" {
				if (s.Kind == "function" || s.Kind == "method" || s.Kind == "constructor") && kind != "" {
					t := targets[id]
					if t == nil {
						ps := planSymbol(s)
						classification := "symbol candidate"
						if kind == "helper" {
							classification = "helper candidate"
						}
						t = &testPlanTarget{Symbol: &ps, File: p, Classification: classification, Origins: []string{}, Evidence: pathTo(id)}
						targets[id] = t
					}
					t.Origins = append(t.Origins, seed)
					if depth[id] < len(t.Evidence) {
						t.Evidence = pathTo(id)
					}
				}
				ft := fileTargets[p]
				if ft == nil {
					reason := "reached test file; individual runnable cases may not be indexed"
					if !isTestFilePath(p) {
						reason = "inline tests in this file reach the change"
					}
					ft = &testPlanTarget{File: p, Classification: "file", Origins: []string{}, Evidence: pathTo(id), Reason: reason}
					fileTargets[p] = ft
				}
				ft.Origins = append(ft.Origins, seed)
				if depth[id] < len(ft.Evidence) {
					ft.Evidence = pathTo(id)
				}
				if ft.seedHops == nil {
					ft.seedHops = map[string]int{}
				}
				if h, ok := ft.seedHops[seed]; !ok || planCallHops(pathTo(id)) < h {
					ft.seedHops[seed] = planCallHops(pathTo(id))
				}
			}
			for _, a := range adj[id] {
				if _, seen := depth[a.next]; seen {
					continue
				}
				depth[a.next] = depth[id] + 1
				parents[a.next] = a.step
				queue = append(queue, a.next)
			}
		}
	}
done:
	for _, t := range targets {
		t.Origins = uniqueSorted(t.Origins)
		r.Candidates = append(r.Candidates, *t)
	}
	r.FileTargets = []testPlanTarget{}
	for _, t := range fileTargets {
		t.Origins = uniqueSorted(t.Origins)
		r.FileTargets = append(r.FileTargets, *t)
	}
	sort.Slice(r.Candidates, func(i, j int) bool {
		return planSymbolKey(*r.Candidates[i].Symbol) < planSymbolKey(*r.Candidates[j].Symbol)
	})
	sort.Slice(r.FileTargets, func(i, j int) bool { return r.FileTargets[i].File < r.FileTargets[j].File })
}

// Preserve coarse reachability, but distinguish it from symbol-level evidence.
// A shortest path in the combined graph may use an import even when a longer
// precise path exists, so compute precise reachability independently.
func planInventory(ctx context.Context, r *testPlanResult, syms []grove.SymbolRecord, edges []grove.Edge, seeds map[string]bool) {
	preciseEdges := make([]grove.Edge, 0, len(edges))
	for _, e := range edges {
		if e.Type != "imports" && e.Source != "heuristic" {
			preciseEdges = append(preciseEdges, e)
		}
	}
	// Structural diffs include enclosing classes/module bodies when a nested
	// method changes. Keep those conservative seeds in the full inventory, but
	// prioritize the innermost changed symbols for the focused pass.
	preciseSeeds := map[string]bool{}
	for id := range seeds {
		preciseSeeds[id] = true
	}
	changedByFile := map[string][]grove.SymbolRecord{}
	for _, s := range syms {
		if seeds[s.ID] {
			changedByFile[s.FilePath] = append(changedByFile[s.FilePath], s)
		}
	}
	for _, changed := range changedByFile {
		for _, parent := range changed {
			for _, child := range changed {
				if parent.Span.Start <= child.Span.Start && parent.Span.End >= child.Span.End && (parent.Span.Start < child.Span.Start || parent.Span.End > child.Span.End) {
					delete(preciseSeeds, parent.ID)
					break
				}
			}
		}
	}
	precise := testPlanResult{FileTargets: append([]testPlanTarget(nil), r.FileTargets...), root: r.root}
	planTraverse(ctx, &precise, syms, preciseEdges, preciseSeeds)
	planTraverse(ctx, r, syms, edges, seeds)
	files := map[string]testPlanTarget{}
	for _, t := range precise.FileTargets {
		files[t.File] = t
	}
	// Precise call paths run through dispatchers: in this repository a
	// change three calls from its own tests also reached 105 other test
	// files through the tool dispatcher, all labelled focused (BUG-15).
	// Per changed symbol, a file is focused when it is at most one call
	// hop farther than that symbol's nearest test file; the rest are
	// transitive, listed nearest first.
	nearest := map[string]int{}
	for _, t := range precise.FileTargets {
		for seed, h := range t.seedHops {
			if n, ok := nearest[seed]; !ok || h < n {
				nearest[seed] = h
			}
		}
	}
	for i, t := range r.FileTargets {
		if p, ok := files[t.File]; ok {
			r.FileTargets[i].Evidence = p.Evidence
			r.FileTargets[i].CallHops = planCallHops(p.Evidence)
			focused := len(p.seedHops) == 0
			for seed, h := range p.seedHops {
				focused = focused || h <= nearest[seed]+1
			}
			if focused {
				r.FileTargets[i].Classification = "focused file"
				r.FileTargets[i].Reason = "changed test or non-heuristic dependency from an innermost changed symbol; run first"
			} else {
				r.FileTargets[i].Classification = "transitive file"
				r.FileTargets[i].Reason = fmt.Sprintf("reached through %d call hops, farther than the nearest tests of the same change; run after the focused files", r.FileTargets[i].CallHops)
			}
		} else {
			r.FileTargets[i].Classification = "coarse fallback"
		}
	}
	// Import, heuristic and enclosing-symbol reach is covered by the
	// package-level "run for safety" tier (planSafety); the run-first
	// tiers keep only precise call paths.
	kept := r.FileTargets[:0]
	runFirst := map[string]bool{}
	for _, t := range r.FileTargets {
		if t.Classification != "coarse fallback" {
			kept = append(kept, t)
			runFirst[t.File] = true
		}
	}
	r.FileTargets = kept
	cands := r.Candidates[:0]
	for _, c := range r.Candidates {
		if runFirst[c.File] {
			cands = append(cands, c)
		}
	}
	r.Candidates = cands
}

func renderTestPlanAsText(out map[string]any) (string, bool) {
	for key := range out {
		switch key {
		case "base", "status", "changedFiles", "changedSymbols", "candidates", "fileTargets", "gaps", "boundary", "linkedTests", "linkedScope",
			"safety", "safetyFileCount", "safetyNotCovered", "coverage":
		default:
			return "", false
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	var r testPlanResult
	if json.Unmarshal(data, &r) != nil || r.Status == "" || r.Boundary == "" {
		return "", false
	}
	var b strings.Builder
	focused, transitive, covered := 0, 0, 0
	for _, t := range r.FileTargets {
		switch t.Classification {
		case "transitive file":
			transitive++
		case "coverage file":
			covered++
		default:
			focused++
		}
	}
	runFirst := fmt.Sprintf("%d focused + %d transitive", focused, transitive)
	if covered > 0 {
		runFirst += fmt.Sprintf(" + %d from coverage only", covered)
	}
	fmt.Fprintf(&b, "test-plan vs %s: %s\n%d changed files, %d changed symbols; run first: %s test files; run for safety: %d test files in %d packages\n",
		r.Base, r.Status, len(r.ChangedFiles), len(r.ChangedSymbols), runFirst, r.SafetyFileCount, len(r.Safety))
	b.WriteString("Run the focused files first, then the transitive ones; the run-for-safety tier is the complete set over the indexed dependency graph. Choose runner syntax from the repository. Test helpers/fixtures are not necessarily runnable files.\n")
	fmt.Fprintf(&b, "boundary: %s\n", r.Boundary)
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "gap: %s\n", g)
	}
	byFile := map[string][]testPlanTarget{}
	for _, c := range r.Candidates {
		byFile[c.File] = append(byFile[c.File], c)
	}
	ordered := append([]testPlanTarget(nil), r.FileTargets...)
	tier := func(t testPlanTarget) int {
		switch t.Classification {
		case "coverage file":
			return 1
		case "transitive file":
			return 2
		}
		return 0
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if tier(a) != tier(b) {
			return tier(a) < tier(b)
		}
		if tier(a) == 2 && a.CallHops != b.CallHops {
			return a.CallHops < b.CallHops
		}
		return false
	})
	const transitiveShown = 15
	shown := 0
	section := ""
	for _, t := range ordered {
		group := "Run first — focused: changed tests, or tests a resolved call path ties to an innermost changed symbol."
		switch t.Classification {
		case "transitive file":
			group = "Run first — transitive: resolved call paths, farther than the focused files (often through a dispatcher); nearest first, with call hops."
		case "coverage file":
			group = "Run first — from recorded coverage only: a test here ran a changed line; the static graph did not reach it."
		}
		if group != section {
			fmt.Fprintln(&b, group)
			section = group
		}
		if t.Classification == "transitive file" {
			if shown++; shown > transitiveShown {
				continue
			}
			fmt.Fprintf(&b, "%s (%d hops)\n", t.File, t.CallHops)
			continue
		}
		fmt.Fprint(&b, t.File)
		if len(t.Sources) == 2 {
			b.WriteString(" [graph + coverage]")
		}
		cs := byFile[t.File]
		if len(cs) <= 5 {
			for _, c := range cs {
				fmt.Fprintf(&b, "; %s (%s)", c.Symbol.Name, c.Classification)
			}
		} else {
			fmt.Fprintf(&b, "; %d symbol candidates (details in JSON)", len(cs))
		}
		b.WriteByte('\n')
	}
	if shown > transitiveShown {
		fmt.Fprintf(&b, "... %d more transitive files, farther away (listed with --format json)\n", shown-transitiveShown)
	}
	if len(r.Safety) > 0 {
		b.WriteString("Run for safety — every test in a package that depends, directly or through other packages, on a changed file (package, test files, package hops; files listed with --format json):\n")
		for _, p := range r.Safety {
			fmt.Fprintf(&b, "  %s (%d, %d hops)\n", p.Package, len(p.TestFiles), p.Hops)
		}
		fmt.Fprintf(&b, "not covered by either tier: %s\n", r.SafetyNotCovered)
	}
	for _, c := range r.Coverage {
		kind := "suite-level (no per-test data)"
		if c.PerTest {
			kind = "per test"
		}
		fmt.Fprintf(&b, "coverage %s (%s, %s; matched against %s line numbers):\n", c.Source, c.Format, kind, c.Matched)
		for _, sym := range c.Symbols {
			switch {
			case sym.Instrumented < 0:
				fmt.Fprintf(&b, "  %s (%s): %d line(s) executed", sym.Symbol, sym.File, sym.Executed)
			default:
				fmt.Fprintf(&b, "  %s (%s): %d of %d instrumented line(s) executed", sym.Symbol, sym.File, sym.Executed, sym.Instrumented)
			}
			if sym.Executed == 0 {
				b.WriteString(" — not run by the recorded tests")
			}
			if n := len(sym.Tests); n > 0 {
				shown := sym.Tests
				if n > 5 {
					shown = shown[:5]
				}
				fmt.Fprintf(&b, "; by %s", strings.Join(shown, ", "))
				if n > 5 {
					fmt.Fprintf(&b, " +%d more", n-5)
				}
			}
			b.WriteByte('\n')
		}
	}
	if len(r.LinkedTests) > 0 {
		b.WriteString("Tests in linked repositories that reference a changed symbol (run them from their own repository):\n")
		for _, l := range r.LinkedTests {
			b.WriteString(l + "\n")
		}
	}
	if r.LinkedScope != "" {
		fmt.Fprintf(&b, "linked repositories: %s\n", r.LinkedScope)
	}
	fmt.Fprintf(&b, "End of test-plan: %d run-first files, %d run-for-safety files, %d gaps. If this footer is missing, read the remaining output before choosing tests.\n", len(r.FileTargets), r.SafetyFileCount, len(r.Gaps))
	return b.String(), true
}

// planDiffLanguages maps the changed files to analyzer and language names.
func planDiffLanguages(changes map[string]string) map[string]bool {
	out := map[string]bool{}
	for p := range changes {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".go":
			out["go"] = true
		case ".py", ".pyi":
			out["python"] = true
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
			out["js-ts"], out["typescript"], out["javascript"], out["tsx"] = true, true, true, true
		case ".java":
			out["java"] = true
		case ".kt", ".kts":
			out["kotlin"] = true
		case ".rs":
			out["rust"] = true
		case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh":
			out["c-cpp"], out["c"], out["cpp"] = true, true, true
		case ".cs":
			out["csharp"] = true
		case ".rb":
			out["ruby"] = true
		case ".php":
			out["php"] = true
		}
	}
	return out
}

// planLanguageInDiff keeps a note for a language of the diff, and any note
// whose prefix is not a language name this plan knows.
func planLanguageInDiff(langs map[string]bool, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "go", "python", "js-ts", "typescript", "javascript", "tsx", "java", "kotlin", "rust", "c-cpp", "c", "cpp", "csharp", "ruby", "php":
		return langs[name]
	}
	return true
}

// planCallHops counts the dependency steps on an evidence path; the
// structural steps (in-file, file-member, contains) are not calls.
func planCallHops(path []testPlanStep) int {
	n := 0
	for _, s := range path {
		switch {
		case strings.HasPrefix(s.Relation, "calls"), strings.HasPrefix(s.Relation, "uses-type"),
			strings.HasPrefix(s.Relation, "implements"), strings.HasPrefix(s.Relation, "extends"),
			strings.HasPrefix(s.Relation, "overrides"), strings.HasPrefix(s.Relation, "imports"):
			n++
		}
	}
	return n
}

// planIDName renders a symbol ID (path::Name@hash) as "Name (path)".
func planIDName(id string) string {
	if i := strings.LastIndexByte(id, '@'); i > 0 {
		id = id[:i]
	}
	if path, name, ok := strings.Cut(id, "::"); ok {
		return name + " (" + path + ")"
	}
	return id
}
