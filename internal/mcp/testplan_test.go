package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func planFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	planWrite(t, dir, "go.mod", "module example.com/plan\n\ngo 1.26\n")
	planWrite(t, dir, "core.go", "package plan\nfunc Value() int { return 1 }\nfunc Wrapper() int { return Value() }\nfunc Unrelated() int { return 7 }\n")
	planWrite(t, dir, "core_test.go", "package plan\nimport \"testing\"\nfunc helper() int { return Wrapper() }\nfunc TestValue(t *testing.T) { if helper()!=1 { t.Fatal(\"bad\") } }\nfunc TestUnrelated(t *testing.T) { if Unrelated()!=7 {t.Fatal(\"bad\")} }\n")
	planGit(t, dir, "init", "-q")
	planGit(t, dir, "add", ".")
	planGit(t, dir, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "-qm", "base")
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	return NewHandler(config.Default(), dir, gc), dir
}

func planWrite(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func planGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, out)
	}
}

func planInvoke(t *testing.T, h *Handler) testPlanResult {
	t.Helper()
	out, err := h.Invoke("prism_test_plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var r testPlanResult
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTestPlanWorkingDiff(t *testing.T) {
	h, dir := planFixture(t)
	if r := planInvoke(t, h); r.Status != "no changes" {
		t.Fatalf("clean: %+v", r)
	}
	planWrite(t, dir, "core.go", "package plan\nfunc Value() int { return 2 }\nfunc Wrapper() int { return Value() }\nfunc Unrelated() int { return 7 }\n")
	r := planInvoke(t, h)
	found := map[string]bool{}
	for _, c := range r.Candidates {
		found[c.Symbol.Name] = true
	}
	if !found["TestValue"] || !found["helper"] || found["TestUnrelated"] {
		t.Fatalf("transitive selection: %+v; gaps=%v", r.Candidates, r.Gaps)
	}
	if len(r.ChangedSymbols) != 1 || r.ChangedSymbols[0].Name != "Value" {
		t.Fatalf("seeds: %+v", r.ChangedSymbols)
	}
	if r.Status != "candidates found" || len(r.FileTargets) != 1 {
		t.Fatalf("result: %+v", r)
	}
	for _, c := range r.Candidates {
		if c.Symbol.Name == "TestValue" && len(c.Evidence) < 3 {
			t.Fatalf("missing indirect evidence: %+v", c)
		}
	}
	second := planInvoke(t, h)
	if !reflect.DeepEqual(r, second) {
		t.Fatalf("non-deterministic plans:\n%+v\n%+v", r, second)
	}
	text, ok := renderTestPlanAsText(testPlanMap(r))
	if !ok || !strings.Contains(text, "TestValue") || !strings.Contains(text, "boundary:") || strings.Contains(text, "return 2") {
		t.Fatalf("text: %s", text)
	}
}

func TestTestPlanCommentsAndUntracked(t *testing.T) {
	h, dir := planFixture(t)
	planWrite(t, dir, "core.go", "// just a comment\npackage plan\nfunc Value() int { return 1 }\nfunc Wrapper() int { return Value() }\nfunc Unrelated() int { return 7 }\n")
	r := planInvoke(t, h)
	if len(r.Candidates)+len(r.ChangedSymbols) != 0 {
		t.Fatalf("comment selected tests: %+v", r)
	}
	planWrite(t, dir, "new_test.go", "package plan\nimport \"testing\"\nfunc TestNew(t *testing.T) { Value() }\n")
	planWrite(t, dir, "fixture.json", "{\"mode\":2}\n")
	r = planInvoke(t, h)
	found := false
	for _, f := range r.FileTargets {
		found = found || f.File == "new_test.go"
	}
	if !found || !strings.Contains(strings.Join(r.Gaps, " "), "fixture.json") {
		t.Fatalf("untracked: %+v", r)
	}
}

func TestTestPlanRemovalAndConfigGaps(t *testing.T) {
	h, dir := planFixture(t)
	planWrite(t, dir, "core.go", "package plan\nfunc Wrapper() int { return 1 }\nfunc Unrelated() int { return 7 }\n")
	planWrite(t, dir, "go.mod", "module example.com/plan\n\ngo 1.26.0\n")
	r := planInvoke(t, h)
	gaps := strings.Join(r.Gaps, " ")
	if !strings.Contains(gaps, "removed Value") || !strings.Contains(gaps, "go.mod") {
		t.Fatalf("missing gaps: %+v", r)
	}
	if err := os.Remove(filepath.Join(dir, "core.go")); err != nil {
		t.Fatal(err)
	}
	r = planInvoke(t, h)
	if !strings.Contains(strings.Join(r.Gaps, " "), "deleted file") {
		t.Fatalf("deleted: %+v", r)
	}
}

func TestTestPlanRenameReportsOldDependencyGap(t *testing.T) {
	h, dir := planFixture(t)
	planWrite(t, dir, "core.go", "package plan\nfunc Renamed() int { return 1 }\nfunc Wrapper() int { return Renamed() }\nfunc Unrelated() int { return 7 }\n")
	r := planInvoke(t, h)
	if !strings.Contains(strings.Join(r.Gaps, " "), "old graph dependencies unavailable") {
		t.Fatalf("rename gap missing: %+v", r)
	}
	found := false
	for _, c := range r.Candidates {
		found = found || c.Symbol.Name == "TestValue"
	}
	if !found {
		t.Fatalf("current callers missing after rename: %+v", r)
	}
}

func TestTestPlanQualifiedIdentity(t *testing.T) {
	syms := []grove.SymbolRecord{
		{ID: "a", Name: "Value", FilePath: "a.go", Kind: "function"},
		{ID: "b", Name: "Value", FilePath: "b.go", Kind: "function"},
		{ID: "test-a", Name: "TestValue", FilePath: "a_test.go", Kind: "function"},
		{ID: "test-b", Name: "TestValue", FilePath: "b_test.go", Kind: "function"},
	}
	edges := []grove.Edge{{From: "test-a", To: "a", Type: "uses-type"}, {From: "test-b", To: "b", Type: "calls"}}
	r := testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"a": true})
	if len(r.Candidates) != 1 || r.Candidates[0].Symbol.ID != "test-a" {
		t.Fatalf("name collision: %+v", r)
	}
}

func TestTestPlanGraph(t *testing.T) {
	symbol := func(id, file string) grove.SymbolRecord {
		return grove.SymbolRecord{ID: id, Name: id, FilePath: file, Kind: "function", Language: "go", Span: grove.SpanInfo{Start: 1, End: 1}}
	}
	syms := []grove.SymbolRecord{symbol("value", "prod.go"), symbol("other", "elsewhere.go"), symbol("helper", "fake_test.go"), symbol("TestA", "fake_test.go"), symbol("TestB", "b_test.go"), symbol("not-selected", "unrelated_test.go")}
	edge := func(from, to, kind string) grove.Edge {
		return grove.Edge{From: from, To: to, Type: kind, Source: "native", Confidence: 1}
	}
	edges := []grove.Edge{edge("helper", "value", "calls"), edge("TestA", "helper", "calls"), edge("helper", "TestA", "calls"), edge("TestA", "other", "calls"), edge("TestB", "value", "calls")}
	r := testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"value": true, "other": true})
	if len(r.Candidates) != 3 || len(r.FileTargets) != 2 {
		t.Fatalf("cycle/mocks: %+v", r)
	}
	for _, c := range r.Candidates {
		if c.Symbol.ID == "TestA" && len(c.Origins) != 2 {
			t.Fatalf("origins: %+v", c)
		}
	}
	want := r
	for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
		edges[i], edges[j] = edges[j], edges[i]
	}
	r = testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"other": true, "value": true})
	if !reflect.DeepEqual(want, r) {
		t.Fatalf("edge order changed result")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	r = testPlanResult{}
	planTraverse(cancelled, &r, syms, edges, map[string]bool{"value": true})
	if len(r.Gaps) == 0 {
		t.Fatal("cancelled traversal lost gap")
	}
}

func TestTestPlanTypesAndFileFallback(t *testing.T) {
	syms := []grove.SymbolRecord{
		{ID: "type", Name: "Type", FilePath: "prod.go", Kind: "interface"},
		{ID: "member", Name: "Method", FilePath: "prod.go", Kind: "method"},
		{ID: "impl", Name: "Method", FilePath: "impl.go", Kind: "method"},
		{ID: "callback", Name: "<module>", FilePath: "value.spec.js", Kind: "module"},
	}
	edges := []grove.Edge{{From: "type", To: "member", Type: "contains"}, {From: "impl", To: "member", Type: "overrides"}, {From: "callback", To: "impl", Type: "calls"}}
	r := testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"type": true})
	if len(r.FileTargets) != 1 || r.FileTargets[0].File != "value.spec.js" {
		t.Fatalf("file fallback: %+v", r)
	}
}

func TestTestPlanSeparatesImportFallback(t *testing.T) {
	syms := []grove.SymbolRecord{
		{ID: "value", Name: "Value", FilePath: "prod.py", Kind: "function"},
		{ID: "helper", Name: "helper", FilePath: "helper.py", Kind: "function"},
		{ID: "helper2", Name: "helper2", FilePath: "helper2.py", Kind: "function"},
		{ID: "helper3", Name: "helper3", FilePath: "helper3.py", Kind: "function"},
		{ID: "precise", Name: "test_precise", FilePath: "tests/test_precise.py", Kind: "function"},
		{ID: "coarse", Name: "test_coarse", FilePath: "tests/test_coarse.py", Kind: "function"},
	}
	edges := []grove.Edge{
		{From: "helper", To: "value", Type: "calls"},
		{From: "helper2", To: "helper", Type: "calls"},
		{From: "helper3", To: "helper2", Type: "calls"},
		{From: "precise", To: "helper3", Type: "calls"},
		{From: "precise", To: "value", Type: "imports"},
		{From: "coarse", To: "value", Type: "imports"},
	}
	r := testPlanResult{}
	planInventory(t.Context(), &r, syms, edges, map[string]bool{"value": true})
	if len(r.FileTargets) != 2 || len(r.Candidates) != 2 {
		t.Fatalf("lost conservative targets: %+v", r)
	}
	for _, f := range r.FileTargets {
		want := "focused file"
		if f.File == "tests/test_coarse.py" {
			want = "coarse fallback"
		}
		if f.Classification != want {
			t.Fatalf("incorrect evidence classification: %+v", f)
		}
	}
}

func TestTestPlanEnclosingAndHeuristicFallback(t *testing.T) {
	syms := []grove.SymbolRecord{
		{ID: "class", FilePath: "prod.py", Kind: "class", Span: grove.SpanInfo{Start: 1, End: 20}},
		{ID: "method", FilePath: "prod.py", Kind: "method", Span: grove.SpanInfo{Start: 3, End: 5}},
		{ID: "direct", FilePath: "tests/test_direct.py", Kind: "function"},
		{ID: "classuser", FilePath: "tests/test_class.py", Kind: "function"},
		{ID: "heuristic", FilePath: "tests/test_heuristic.py", Kind: "function"},
	}
	edges := []grove.Edge{
		{From: "direct", To: "method", Type: "calls", Source: "native"},
		{From: "classuser", To: "class", Type: "uses-type", Source: "native"},
		{From: "heuristic", To: "method", Type: "calls", Source: "heuristic"},
	}
	r := testPlanResult{}
	planInventory(t.Context(), &r, syms, edges, map[string]bool{"class": true, "method": true})
	if len(r.FileTargets) != 3 {
		t.Fatalf("lost fallback targets: %+v", r)
	}
	for _, f := range r.FileTargets {
		want := "coarse fallback"
		if f.File == "tests/test_direct.py" {
			want = "focused file"
		}
		if f.Classification != want {
			t.Fatalf("incorrect focused target: %+v", f)
		}
	}
}

func TestTestPlanInterfaces(t *testing.T) {
	for _, base := range []string{"--output=x", "", "HEAD\n--stat"} {
		if _, err := testPlanChanges(t.Context(), t.TempDir(), base); err == nil {
			t.Fatalf("accepted base %q", base)
		}
	}
	if _, err := (&Handler{}).toolTestPlan(t.Context(), map[string]any{"base": 12}); err == nil {
		t.Fatal("base type silently ignored")
	}
}

func TestTestPlanLongChain(t *testing.T) {
	const n = 3000
	syms := make([]grove.SymbolRecord, n)
	edges := make([]grove.Edge, 0, n-1)
	for i := 0; i < n; i++ {
		id := fmt.Sprint(i)
		file := "prod.go"
		if i == n-1 {
			file = "prod_test.go"
		}
		syms[i] = grove.SymbolRecord{ID: id, Name: id, FilePath: file, Kind: "function"}
		if i > 0 {
			edges = append(edges, grove.Edge{From: id, To: fmt.Sprint(i - 1), Type: "calls"})
		}
	}
	r := testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"0": true})
	if len(r.Candidates) != 1 || len(r.Candidates[0].Evidence) != n-1 {
		t.Fatalf("long chain: %d candidates", len(r.Candidates))
	}
}

// test-plan is a CLI command for people (`prism test-plan`), not an agent
// tool: paired runs found agents never call it unsteered, and the steered
// version did not pay for itself. Keep it off every agent-facing surface.
func TestTestPlanIsNotExposedToAgents(t *testing.T) {
	h, dir := planFixture(t)
	planWrite(t, dir, "new_test.go", "package plan\nimport \"testing\"\nfunc TestNew(t *testing.T) {Value()}\n")
	if out, err := NewServer(h).dispatch("tools/call", json.RawMessage(`{"name":"prism_test_plan","arguments":{}}`)); err == nil {
		t.Fatalf("MCP served prism_test_plan: %v", out)
	}
	if _, _, err := expandCompactCall(map[string]any{"op": "test_plan", "args": map[string]any{}}); err == nil {
		t.Fatal("compact gateway accepted op=test_plan")
	}
	for _, s := range append(ToolSchemas(), CompactToolSchemas()...) {
		if b, _ := json.Marshal(s); strings.Contains(string(b), "test_plan") {
			t.Fatalf("tool schema advertises test-plan: %s", b)
		}
	}
	for name, text := range map[string]string{"legacy": serverInstructions, "compact": compactServerInstructions} {
		if strings.Contains(text, "test_plan") || strings.Contains(text, "test-plan") {
			t.Errorf("%s server instructions steer agents to test-plan", name)
		}
	}
	// The CLI path still answers.
	out, err := h.Invoke("prism_test_plan", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := h.RenderToolText(false, "prism_test_plan", nil, out.(map[string]any))
	if !ok || !strings.Contains(text, "TestNew") {
		t.Fatalf("CLI inventory missing the new test: %v\n%s", ok, text)
	}
}

func TestTestPlanTextPrioritizesDecisions(t *testing.T) {
	r := testPlanResult{Base: "HEAD", Status: "candidates found", Boundary: testPlanBoundary, Gaps: []string{"old dependencies unavailable"}}
	for i := 0; i < 100; i++ {
		s := testPlanSymbol{ID: strings.Repeat("long-identity", 30), Name: "TestCase", File: "tests/test_api.py"}
		r.ChangedSymbols = append(r.ChangedSymbols, s)
		r.Candidates = append(r.Candidates, testPlanTarget{Symbol: &s, File: s.File})
	}
	r.FileTargets = []testPlanTarget{{File: "tests/test_api.py"}, {File: "tests/test_other.py"}}
	text, ok := renderTestPlanAsText(testPlanMap(r))
	if !ok || len(text) > 2000 || strings.Index(text, "gap:") > strings.Index(text, "tests/test_api.py") || !strings.Contains(text, "tests/test_other.py") || !strings.Contains(text, "End of test-plan: 2 file targets, 1 gaps") {
		t.Fatalf("decision summary missing or buried: %s", text)
	}
	if strings.Contains(text, "long-identity") || !strings.Contains(text, "100 symbol candidates (details in JSON)") {
		t.Fatalf("evidence should remain in JSON: %s", text)
	}
}

func TestTestPlanPreservesLiteralToolNames(t *testing.T) {
	s := testPlanSymbol{ID: "tests/test_api.py::prism_query", Name: "prism_query", File: "tests/test_api.py", Kind: "function", Language: "python"}
	r := testPlanResult{Base: "HEAD", Status: "candidates found", Boundary: testPlanBoundary,
		Candidates:  []testPlanTarget{{Symbol: &s, File: s.File, Classification: "symbol candidate"}},
		FileTargets: []testPlanTarget{{File: s.File, Classification: "file"}},
	}
	h := &Handler{}
	a, aok := h.RenderToolText(false, "prism_test_plan", nil, testPlanMap(r))
	b, bok := h.RenderToolText(true, "prism_test_plan", nil, testPlanMap(r))
	if !aok || !bok || a != b || strings.Contains(b, "op=query") {
		t.Fatalf("inventory identifiers rewritten: %s", b)
	}
}

func TestTestPlanImportFallbackAndMissingEndpoints(t *testing.T) {
	syms := []grove.SymbolRecord{
		{ID: "value", Name: "Value", FilePath: "prod.go", Kind: "function"},
		{ID: "file:prod.go", Name: "prod.go", FilePath: "prod.go", Kind: "file"},
		{ID: "file:value.spec.js", Name: "value.spec.js", FilePath: "value.spec.js", Kind: "file"},
	}
	edges := []grove.Edge{{From: "file:value.spec.js", To: "file:prod.go", Type: "imports", Source: "astkit", Confidence: 1}, {From: "missing-caller", To: "value", Type: "calls"}}
	r := testPlanResult{}
	planTraverse(t.Context(), &r, syms, edges, map[string]bool{"value": true})
	if len(r.Candidates) != 0 || len(r.FileTargets) != 1 || r.FileTargets[0].File != "value.spec.js" || len(r.Gaps) == 0 {
		t.Fatalf("coarse fallback: %+v", r)
	}
}

func TestTestPlanBuildDirectivesAreNotComments(t *testing.T) {
	a, ok := planGoSyntax([]byte("//go:build linux\n\npackage p\nfunc F(){}\n"))
	if !ok {
		t.Fatal("parse")
	}
	b, ok := planGoSyntax([]byte("//go:build darwin\n\npackage p\nfunc F(){}\n"))
	if !ok || a == b {
		t.Fatal("build directives ignored")
	}
}

func TestTestPlanMissingCompilerPersistsAcrossCalls(t *testing.T) {
	h, dir := planFixture(t)
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	planWrite(t, dir, "core.go", "package plan\nfunc Value() int {return 2}\nfunc Wrapper() int {return Value()}\nfunc Unrelated() int {return 7}\n")
	a, b := planInvoke(t, h), planInvoke(t, h)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("diagnostics changed across calls:\n%v\n%v", a.Gaps, b.Gaps)
	}
	if !strings.Contains(strings.Join(a.Gaps, " "), "no go.mod") {
		t.Fatalf("missing compiler not reported: %v", a.Gaps)
	}
}

// A reproducible synthetic selection benchmark. Output bytes are not tokens.
func BenchmarkTestPlanInventory(b *testing.B) {
	syms := []grove.SymbolRecord{{ID: "value", Name: "Value", FilePath: "prod.go", Kind: "function"}}
	edges := []grove.Edge{}
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("test-%05d", i)
		syms = append(syms, grove.SymbolRecord{ID: id, Name: id, FilePath: fmt.Sprintf("tests/%s_test.go", id), Kind: "function"})
		if i < 20 {
			edges = append(edges, grove.Edge{From: id, To: "value", Type: "calls", Source: "native", Confidence: 1})
		}
	}
	var broad strings.Builder
	for _, s := range syms[1:] {
		fmt.Fprintf(&broad, "%s:%d %s\n", s.FilePath, s.Span.Start, s.Name)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := testPlanResult{Base: "HEAD", Status: "candidates found", Boundary: testPlanBoundary}
		planTraverse(b.Context(), &r, syms, edges, map[string]bool{"value": true})
		text, _ := renderTestPlanAsText(testPlanMap(r))
		b.ReportMetric(float64(len(text)), "selected-output-bytes")
		b.ReportMetric(float64(len(r.Candidates)), "selected-symbols")
		b.ReportMetric(float64(len(syms)-1), "all-test-symbols")
		b.ReportMetric(float64(broad.Len()), "all-discovery-bytes")
	}
}
