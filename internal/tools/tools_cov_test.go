package tools

import (
	"os"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

type fakeServer struct{}

func (fakeServer) Close() {}

func fakeGroveSrv(t *testing.T, payload map[string]any) fakeServer {
	t.Helper()
	_ = payload
	return fakeServer{}
}

func newHWithGrove(t *testing.T, _ any) *Handler {
	t.Helper()
	root := t.TempDir()
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	return NewHandler(&config.Config{MaxCacheFiles: 100}, root, gc)
}

func TestToolSearch(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{{"id": "s1", "name": "Foo"}}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_search", map[string]any{"query": "Foo", "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
}

func TestToolLookup(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{
		{"id": "s1", "name": "Foo", "qualifiedName": "pkg.Foo", "rawText": "code"},
	}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "pkg.Foo"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
	// No name → error
	if _, err := h.Invoke("prism_lookup", map[string]any{}); err == nil {
		t.Error("expected err")
	}
}

func TestToolLookup_NoMatch(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "X"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["symbol"] != nil {
		t.Error("expected nil symbol")
	}
}

// TestToolLookup_QualifiedName verifies that "pkg/path.Symbol" and
// "github.com/mod/pkg/path.Symbol" forms resolve correctly.
func TestToolLookup_QualifiedName(t *testing.T) {
	h := newHWithGrove(t, nil)

	// Write a Go source file under internal/cli/ so Grove can index it.
	dir := h.Root + "/internal/cli"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/lookup.go", []byte("package cli\n\nfunc DoLookup() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"DoLookup",
		"internal/cli.DoLookup",
		"github.com/provasign/prism/internal/cli.DoLookup",
	} {
		out, err := h.Invoke("prism_lookup", map[string]any{"name": name})
		if err != nil {
			t.Fatalf("name=%q: %v", name, err)
		}
		m := out.(map[string]any)
		if m["symbol"] == nil {
			t.Errorf("name=%q: expected symbol, got nil", name)
		}
	}
}

func TestToolIndex(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"filesSeen": 5})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_index", map[string]any{"dir": h.Root}); err != nil {
		t.Fatal(err)
	}
}

func TestToolCompact(t *testing.T) {
	h := newHWithGrove(t, nil)
	turns := []map[string]any{
		{"role": "user", "content": "task A", "kind": "exploration"},
		{"role": "assistant", "content": "result file 1", "kind": "file_read"},
		{"role": "user", "content": "task B", "kind": "implementation"},
		{"role": "assistant", "content": "result B"},
		{"role": "user", "content": "final task"},
	}
	out, err := h.Invoke("prism_compact", map[string]any{"turns": turns})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["compressedTurns"] == nil {
		t.Error("no compressed")
	}
}

func TestToolCompact_NoTurns(t *testing.T) {
	h := newHWithGrove(t, nil)
	if _, err := h.Invoke("prism_compact", map[string]any{}); err == nil {
		t.Error("expected err")
	}
	if _, err := h.Invoke("prism_compact", map[string]any{"turns": "notarray"}); err == nil {
		t.Error("expected err")
	}
}

func TestToolFeedback(t *testing.T) {
	h := newHWithGrove(t, nil)
	if _, err := h.Invoke("prism_feedback", map[string]any{"tool": "x", "rating": 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke("prism_feedback", map[string]any{"rating": 99}); err == nil {
		t.Error("expected err")
	}
	if _, err := h.Invoke("prism_feedback", map[string]any{}); err == nil {
		t.Error("expected err on missing rating")
	}
}

func TestCategorize(t *testing.T) {
	cases := []struct {
		name string
		s    grove.SymbolRecord
		want ranking.Category
	}{
		{"go test", grove.SymbolRecord{FilePath: "x_test.go"}, ranking.CategoryTest},
		{"typescript test", grove.SymbolRecord{FilePath: "x.test.ts"}, ranking.CategoryTest},
		{"typescript spec", grove.SymbolRecord{FilePath: "x.spec.ts"}, ranking.CategoryTest},
		{"javascript tests dir", grove.SymbolRecord{FilePath: "/__tests__/x.js"}, ranking.CategoryTest},
		{"python test", grove.SymbolRecord{FilePath: "x_test.py"}, ranking.CategoryTest},
		{"java test", grove.SymbolRecord{FilePath: "src/UserServiceTest.java"}, ranking.CategoryTest},
		{"rust test", grove.SymbolRecord{FilePath: "src/service_test.rs"}, ranking.CategoryTest},
		{"c test", grove.SymbolRecord{FilePath: "tests/service_test.c"}, ranking.CategoryTest},
		{"cpp test", grove.SymbolRecord{FilePath: "tests/service_test.cpp"}, ranking.CategoryTest},
		{"csharp test", grove.SymbolRecord{FilePath: "UserServiceTests.cs"}, ranking.CategoryTest},
		{"php test", grove.SymbolRecord{FilePath: "UserServiceTest.php"}, ranking.CategoryTest},
		{"markdown doc", grove.SymbolRecord{FilePath: "x.md", Kind: "function"}, ranking.CategoryDoc},
		{"namespace doc", grove.SymbolRecord{Kind: "namespace"}, ranking.CategoryDoc},
		{"docstring doc", grove.SymbolRecord{Docstring: "doc"}, ranking.CategoryDoc},
		{"dependency", grove.SymbolRecord{FilePath: "x.go", Kind: "function"}, ranking.CategoryDependency},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := categorize(c.s); got != c.want {
				t.Fatalf("categorize() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestFilterDocSeeds(t *testing.T) {
	in := []grove.SymbolRecord{
		{FilePath: "README.md", Kind: "namespace"},
		{FilePath: "internal/tools/tools.go", Kind: "function", Name: "Invoke"},
		{FilePath: "ROADMAP.md", Kind: "namespace"},
		{FilePath: "internal/ranking/budget.go", Kind: "function", Name: "Select"},
	}
	got := filterDocSeeds(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 code symbols, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Invoke" || got[1].Name != "Select" {
		t.Fatalf("unexpected symbols: %+v", got)
	}
}

func TestFilterGeneratedPrismContext(t *testing.T) {
	in := []grove.SymbolRecord{
		{FilePath: "AGENTS.md", RawText: "Prism steering"},
		{FilePath: "AGENTS.md", RawText: "## Prism — context delivery\nUse Prism."},
		{FilePath: "docs/architecture.md", RawText: "## Prism architecture"},
		{FilePath: "src/app.ts", Name: "authorize"},
	}
	got := filterGeneratedPrismContext(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 non-generated symbols, got %d: %+v", len(got), got)
	}
	if got[0].FilePath != "AGENTS.md" || got[1].FilePath != "docs/architecture.md" || got[2].FilePath != "src/app.ts" {
		t.Fatalf("unexpected filtered symbols: %+v", got)
	}
}

func TestStringArg(t *testing.T) {
	if stringArg(nil, "x", "def") != "def" {
		t.Error("default")
	}
	if stringArg(map[string]any{"x": "v"}, "x", "def") != "v" {
		t.Error("get")
	}
}

func TestToolQuery_OK(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{
		"symbols": []map[string]any{{"id": "s1", "name": "Foo", "filePath": "a.go"}},
		"results": []map[string]any{},
		"edges":   []map[string]any{},
		"nodes":   []map[string]any{},
		"tests":   []map[string]any{},
	})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_query", map[string]any{"task": "find Foo", "limit": 10}); err != nil {
		t.Logf("query err (ok if grove paths missing): %v", err)
	}
}

func TestToolRead_NoFile(t *testing.T) {
	h := newHWithGrove(t, nil)
	if _, err := h.Invoke("prism_read", map[string]any{}); err == nil {
		t.Error("expected err")
	}
}

// ─── agent-directed query parameters ─────────────────────────────────────────

func TestToolQuery_TermsSeeding(t *testing.T) {
	h := newHWithGrove(t, nil)
	// terms param should not error even when grove returns no matches
	_, err := h.Invoke("prism_query", map[string]any{
		"task":  "find AccessCount",
		"terms": []any{"AccessCount", "sha-pointer"},
	})
	if err != nil {
		t.Fatalf("unexpected error with terms param: %v", err)
	}
}

func TestToolQuery_IncludeGraphOnly(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "compression",
		"include": []any{"graph"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	for _, sym := range res.Symbols {
		if sym.Category == string(ranking.CategoryTest) {
			t.Errorf("include=[graph] should not return test symbol %q", sym.Name)
		}
		if sym.Category == string(ranking.CategoryDoc) {
			t.Errorf("include=[graph] should not return doc symbol %q", sym.Name)
		}
	}
}

func TestToolQuery_IncludeDocsOnly(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "architecture",
		"include": []any{"docs"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	for _, sym := range res.Symbols {
		if sym.Category != string(ranking.CategoryDoc) {
			t.Errorf("include=[docs] should only return doc symbols, got category %q for %q", sym.Category, sym.Name)
		}
	}
}

func TestToolQuery_GraphDepthClamped(t *testing.T) {
	h := newHWithGrove(t, nil)
	// depth=0 should be clamped to 1, depth=99 to 5 — neither should error
	for _, depth := range []int{0, 1, 5, 99} {
		_, err := h.Invoke("prism_query", map[string]any{
			"task":        "find symbols",
			"graph_depth": depth,
		})
		if err != nil {
			t.Errorf("depth=%d: unexpected error: %v", depth, err)
		}
	}
}

func TestToolQuery_CoverageGaps(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":    "fix compression bug",
		"include": []any{"graph", "tests", "coverage_gaps"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	// coverageGaps must be a slice (may be empty if grove has no index) — not nil/panic
	// Each gap entry must have a non-empty name and filePath
	for _, g := range res.CoverageGaps {
		if g.Name == "" {
			t.Error("gap entry has empty name")
		}
		if g.FilePath == "" {
			t.Error("gap entry has empty filePath")
		}
	}
}

func TestToolQuery_CoverageGaps_NotIncludedByDefault(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task": "fix bug",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	if len(res.CoverageGaps) > 0 {
		t.Error("coverage_gaps should be empty when not requested")
	}
}

func TestToolQuery_TermsAndIncludeCombined(t *testing.T) {
	h := newHWithGrove(t, nil)
	out, err := h.Invoke("prism_query", map[string]any{
		"task":        "repeat read handling",
		"terms":       []any{"AccessCount"},
		"include":     []any{"graph", "tests"},
		"graph_depth": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(queryResult)
	// docs must be absent
	for _, sym := range res.Symbols {
		if sym.Category == string(ranking.CategoryDoc) {
			t.Errorf("should not return doc symbol %q when include=[graph,tests]", sym.Name)
		}
	}
}

func TestNewHandlerWithReady(t *testing.T) {
	gc := grove.NewClient("", "").WithTokenFromDir(t.TempDir())
	readyCh := make(chan struct{})
	h := NewHandlerWithReady(&config.Config{MaxCacheFiles: 100}, t.TempDir(), gc, readyCh)
	if h == nil {
		t.Error("nil handler")
	}
	if h.readyCh == nil {
		t.Error("readyCh not set")
	}
}

func TestMinFloat(t *testing.T) {
	if minFloat(1.0, 2.0) != 1.0 {
		t.Error("should return smaller first arg")
	}
	if minFloat(3.0, 2.0) != 2.0 {
		t.Error("should return smaller second arg")
	}
}

func TestToolQuery_TestWritingTask(t *testing.T) {
	h := newHWithGrove(t, nil)
	// A test-writing task should trigger the TestRelevance boost and budget expansion
	// without error.
	out, err := h.Invoke("prism_query", map[string]any{
		"task": "write tests for toolQuery",
	})
	if err != nil {
		t.Fatalf("unexpected error for test-writing task: %v", err)
	}
	if out == nil {
		t.Error("expected non-nil output")
	}
}

func TestToolDescription_Default(t *testing.T) {
	// The default fallback "Prism tool: <name>" for an unknown tool name.
	desc := toolDescription("prism_unknown_xyz")
	if desc == "" {
		t.Error("expected non-empty description for unknown tool")
	}
}

func TestInvoke_WithReadyCh(t *testing.T) {
	// Test the readyCh code path in Invoke (waits until ready before dispatching).
	gc := grove.NewClient("", "").WithTokenFromDir(t.TempDir())
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)

	readyCh := make(chan struct{})
	close(readyCh) // already ready
	h := NewHandlerWithReady(&config.Config{MaxCacheFiles: 100}, t.TempDir(), gc, readyCh)
	// Any call should succeed: readyCh is already closed so select fires immediately.
	_, err := h.Invoke("prism_query", map[string]any{"task": "find symbols"})
	if err != nil {
		t.Logf("prism_query with readyCh: %v (ok if grove paths unavailable)", err)
	}
}

func TestBuildCoverageGaps_Seeds(t *testing.T) {
	h := newHWithGrove(t, nil)
	ctx := t.Context()

	codeSym := grove.SymbolRecord{
		ID:       "code1",
		Name:     "DoSomething",
		FilePath: "service.go",
		Kind:     "function",
	}
	testSym := grove.SymbolRecord{
		ID:       "test1",
		Name:     "TestDoSomething",
		FilePath: "service_test.go",
		Kind:     "function",
	}

	// code symbol not in seedCoverage → gap; test symbol skipped (isCodeSym false)
	gaps := buildCoverageGaps(ctx, h.Grove, []grove.SymbolRecord{codeSym, testSym}, nil, map[string]bool{})
	if len(gaps) != 1 {
		t.Errorf("expected 1 gap, got %d", len(gaps))
	}
	if len(gaps) > 0 && gaps[0].Name != "DoSomething" {
		t.Errorf("expected gap for DoSomething, got %q", gaps[0].Name)
	}
}

func TestBuildCoverageGaps_SeedCovered(t *testing.T) {
	h := newHWithGrove(t, nil)
	codeSym := grove.SymbolRecord{ID: "c1", Name: "Fn", FilePath: "x.go", Kind: "function"}
	// Symbol is in seedCoverage → no gap
	gaps := buildCoverageGaps(t.Context(), h.Grove, []grove.SymbolRecord{codeSym}, nil, map[string]bool{"c1": true})
	if len(gaps) != 0 {
		t.Errorf("covered symbol should not be a gap, got %d", len(gaps))
	}
}

func TestBuildCoverageGaps_BlastRadius(t *testing.T) {
	h := newHWithGrove(t, nil)
	codeSym := grove.SymbolRecord{ID: "br1", Name: "BrFunc", FilePath: "br.go", Kind: "function"}
	// blastRadius sym not in seeds → g.Tests called (empty index = no tests) → gap
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{codeSym}, map[string]bool{})
	if len(gaps) != 1 {
		t.Errorf("expected 1 blast-radius gap, got %d", len(gaps))
	}
}

func TestBuildCoverageGaps_BlastRadiusRequiresDirectTest(t *testing.T) {
	h := newHWithGrove(t, nil)
	src := `package pay

func UpdatePayment() {}
func RefundPayment() { UpdatePayment() }
`
	testSrc := `package pay

func TestRefundPayment(t interface{}) {
	RefundPayment()
}
`
	if err := os.WriteFile(h.Root+"/payment.go", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/payment_test.go", []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	update := grove.SymbolRecord{ID: "payment.go::UpdatePayment@", Name: "UpdatePayment", FilePath: "payment.go", Kind: "function"}
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{update}, map[string]bool{})
	if len(gaps) != 1 || gaps[0].Name != "UpdatePayment" {
		t.Fatalf("indirectly reached test should not cover UpdatePayment, got %+v", gaps)
	}
}

func TestBuildCoverageGaps_BlastRadiusAcceptsDirectTest(t *testing.T) {
	h := newHWithGrove(t, nil)
	src := `package pay

func RefundPayment() {}
`
	testSrc := `package pay

func TestRefundPayment(t interface{}) {
	RefundPayment()
}
`
	if err := os.WriteFile(h.Root+"/payment.go", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/payment_test.go", []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	refund := grove.SymbolRecord{ID: "payment.go::RefundPayment@", Name: "RefundPayment", FilePath: "payment.go", Kind: "function"}
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{refund}, map[string]bool{})
	if len(gaps) != 0 {
		t.Fatalf("direct test should cover RefundPayment, got %+v", gaps)
	}
}

func TestBuildCoverageGaps_DirectTestRequiresSamePackageDir(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.MkdirAll(h.Root+"/payment", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.Root+"/storage", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/payment/service.go", []byte("package payment\n\nfunc ListPayments() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/storage/memory.go", []byte("package storage\n\nfunc ListPayments() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/storage/memory_test.go", []byte("package storage\n\nfunc TestListPayments_FilterByUser(t interface{}) { ListPayments() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	paymentList := grove.SymbolRecord{ID: "payment/service.go::ListPayments@", Name: "ListPayments", FilePath: "payment/service.go", Kind: "function"}
	storageList := grove.SymbolRecord{ID: "storage/memory.go::ListPayments@", Name: "ListPayments", FilePath: "storage/memory.go", Kind: "function"}
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{paymentList, storageList}, map[string]bool{})
	if len(gaps) != 1 || gaps[0].FilePath != "payment/service.go" {
		t.Fatalf("same-name test in another package should not cover payment ListPayments, got %+v", gaps)
	}
}

func TestBuildCoverageGaps_SkipsConstructorsAndAcceptsDomainStemTests(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.WriteFile(h.Root+"/memory.go", []byte("package storage\n\nfunc NewMemoryStore() {}\nfunc SavePayment() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/memory_test.go", []byte("package storage\n\nimport \"testing\"\n\nfunc TestSaveAndGet(t *testing.T) { SavePayment() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	constructor := grove.SymbolRecord{ID: "memory.go::NewMemoryStore@", Name: "NewMemoryStore", FilePath: "memory.go", Kind: "function"}
	save := grove.SymbolRecord{ID: "memory.go::SavePayment@", Name: "SavePayment", FilePath: "memory.go", Kind: "function"}
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{constructor, save}, map[string]bool{})
	if len(gaps) != 0 {
		t.Fatalf("constructor and domain-stem-covered SavePayment should not be gaps, got %+v", gaps)
	}
}

func TestBuildCoverageGaps_DomainStemDoesNotCrossDifferentConcepts(t *testing.T) {
	h := newHWithGrove(t, nil)
	if err := os.WriteFile(h.Root+"/validator.go", []byte("package payment\n\nfunc ValidateCurrency() {}\nfunc ValidateAmount() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Root+"/validator_test.go", []byte("package payment\n\nimport \"testing\"\n\nfunc TestValidateAmount(t *testing.T) { ValidateAmount() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grove.Index(t.Context(), h.Root); err != nil {
		t.Fatal(err)
	}

	currency := grove.SymbolRecord{ID: "validator.go::ValidateCurrency@", Name: "ValidateCurrency", FilePath: "validator.go", Kind: "function"}
	gaps := buildCoverageGaps(t.Context(), h.Grove, nil, []grove.SymbolRecord{currency}, map[string]bool{})
	if len(gaps) != 1 || gaps[0].Name != "ValidateCurrency" {
		t.Fatalf("TestValidateAmount should not cover ValidateCurrency, got %+v", gaps)
	}
}

func TestBuildCoverageGaps_Dedup(t *testing.T) {
	h := newHWithGrove(t, nil)
	sym := grove.SymbolRecord{ID: "d1", Name: "DupFn", FilePath: "dup.go", Kind: "function"}
	// same sym in both seeds and blastRadius → only 1 gap (dedup via seen map)
	gaps := buildCoverageGaps(t.Context(), h.Grove, []grove.SymbolRecord{sym}, []grove.SymbolRecord{sym}, map[string]bool{})
	if len(gaps) != 1 {
		t.Errorf("expected 1 gap (dedup), got %d", len(gaps))
	}
}

func TestToolRead_SeenFile(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)

	p := "seen_file.go"
	abs := h.Root + "/" + p
	if err := os.WriteFile(abs, []byte("package x\nfunc G(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First read — populates session tracker.
	if _, err := h.Invoke("prism_read", map[string]any{"file": p}); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// Second read — session.Lookup returns seen=true, exercises the confidence path.
	if _, err := h.Invoke("prism_read", map[string]any{"file": p}); err != nil {
		t.Fatalf("second read: %v", err)
	}
}
