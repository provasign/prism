package mcp

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

const testPlanBoundary = "Static graph candidates, not measured test coverage. Test identification uses file-path conventions; inline or framework tests may not be recognized. Runtime dispatch, reflection, external consumers, and dependencies absent from the index may be missed. Choose test commands and broader validation using these gaps and repository requirements."

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
		return nil, fmt.Errorf("git diff %s: %w", base, err)
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
		if p == ".grove" || strings.HasPrefix(p, ".grove/") {
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
	r := testPlanResult{Base: base, Status: "no changes", ChangedFiles: []string{}, ChangedSymbols: []testPlanSymbol{}, Candidates: []testPlanTarget{}, FileTargets: []testPlanTarget{}, Gaps: []string{}, Boundary: testPlanBoundary}
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
		for _, issue := range status.Readiness {
			r.Gaps = append(r.Gaps, fmt.Sprintf("analysis readiness (%s): %s; %s", issue.Language, issue.Problem, issue.Fix))
		}
		for _, note := range status.Native {
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
			old, e = exec.CommandContext(ctx, "git", "-C", h.Root, "show", base+":"+gitPrefix(h.Root)+p).Output()
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
		for _, s := range fd.Added {
			addSeed(s)
		}
		for _, c := range fd.Changed {
			if c.After != nil {
				addSeed(*c.After)
			}
		}
		for _, c := range fd.Renamed {
			if c.After != nil {
				addSeed(*c.After)
			}
			if c.Before != nil {
				r.ChangedSymbols = append(r.ChangedSymbols, planSymbol(*c.Before))
				r.Gaps = append(r.Gaps, p+": renamed "+displayQN(*c.Before)+"; old graph dependencies unavailable")
			}
		}
		for _, s := range fd.Removed {
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
	r.Gaps = uniqueSorted(r.Gaps)
	sort.Slice(r.ChangedSymbols, func(i, j int) bool { return planSymbolKey(r.ChangedSymbols[i]) < planSymbolKey(r.ChangedSymbols[j]) })
	if len(r.Candidates)+len(r.FileTargets) > 0 {
		r.Status = "candidates found"
	}
	return testPlanMap(r), nil
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
			r.Gaps = append(r.Gaps, "changed symbol absent from refreshed graph: "+seed)
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
				r.Gaps = append(r.Gaps, "unresolved graph endpoint: "+id)
				continue
			}
			p := normalizePath(s.FilePath)
			if isTestFilePath(p) {
				if s.Kind == "function" || s.Kind == "method" || s.Kind == "constructor" {
					t := targets[id]
					if t == nil {
						ps := planSymbol(s)
						classification := "symbol candidate"
						if s.Language == "go" && !strings.HasPrefix(s.Name, "Test") && !strings.HasPrefix(s.Name, "Example") && !strings.HasPrefix(s.Name, "Fuzz") && !strings.HasPrefix(s.Name, "Benchmark") {
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
					ft = &testPlanTarget{File: p, Classification: "file", Origins: []string{}, Evidence: pathTo(id), Reason: "reached test file; individual runnable cases may not be indexed"}
					fileTargets[p] = ft
				}
				ft.Origins = append(ft.Origins, seed)
				if depth[id] < len(ft.Evidence) {
					ft.Evidence = pathTo(id)
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
	precise := testPlanResult{FileTargets: append([]testPlanTarget(nil), r.FileTargets...)}
	planTraverse(ctx, &precise, syms, preciseEdges, preciseSeeds)
	planTraverse(ctx, r, syms, edges, seeds)
	files := map[string]testPlanTarget{}
	for _, t := range precise.FileTargets {
		files[t.File] = t
	}
	for i, t := range r.FileTargets {
		if p, ok := files[t.File]; ok {
			r.FileTargets[i].Classification = "focused file"
			r.FileTargets[i].Evidence = p.Evidence
			r.FileTargets[i].Reason = "changed test or non-heuristic dependency from an innermost changed symbol; run first"
		} else {
			r.FileTargets[i].Classification = "coarse fallback"
			r.FileTargets[i].Reason = "import, heuristic, or enclosing-symbol reachability; review for broader validation"
		}
	}
}

func renderTestPlanAsText(out map[string]any) (string, bool) {
	for key := range out {
		switch key {
		case "base", "status", "changedFiles", "changedSymbols", "candidates", "fileTargets", "gaps", "boundary":
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
	fmt.Fprintf(&b, "test-plan vs %s: %s\n%d changed files, %d changed symbols, %d symbol candidates, %d file targets\n", r.Base, r.Status, len(r.ChangedFiles), len(r.ChangedSymbols), len(r.Candidates), len(r.FileTargets))
	b.WriteString("Run focused file targets first; choose runner syntax from the repository. Coarse fallbacks are separate: broaden for applicable gaps or required checks, explaining why. Test helpers/fixtures are not necessarily runnable files.\n")
	fmt.Fprintf(&b, "boundary: %s\n", r.Boundary)
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "gap: %s\n", g)
	}
	b.WriteString("Complete file targets follow. Symbol IDs, origins, and graph evidence are available with --format json.\n")
	byFile := map[string][]testPlanTarget{}
	for _, c := range r.Candidates {
		byFile[c.File] = append(byFile[c.File], c)
	}
	ordered := append([]testPlanTarget(nil), r.FileTargets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Classification != "coarse fallback" && ordered[j].Classification == "coarse fallback"
	})
	section := ""
	for _, t := range ordered {
		group := "Focused targets: changed tests or non-heuristic paths from innermost changed symbols. Run these first."
		if t.Classification == "coarse fallback" {
			group = "Broader fallback inventory: import, heuristic, or enclosing-symbol paths. Review against gaps; these are not additional mandatory test files."
		}
		if group != section {
			fmt.Fprintln(&b, group)
			section = group
		}
		fmt.Fprint(&b, t.File)
		if t.Classification == "coarse fallback" {
			b.WriteByte('\n')
			continue
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
	fmt.Fprintf(&b, "End of test-plan: %d file targets, %d gaps. If this footer is missing, read the remaining output before choosing tests.\n", len(r.FileTargets), len(r.Gaps))
	return b.String(), true
}

func writePlanEvidence(b *strings.Builder, path []testPlanStep) {
	for _, s := range path {
		fmt.Fprintf(b, "    %s -> %s [%s; source=%s; confidence=%g]\n", s.From, s.To, s.Relation, s.Source, s.Confidence)
	}
}
