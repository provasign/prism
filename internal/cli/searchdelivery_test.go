package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/mcp"
)

func TestSearchCLIExplicitZeroContextAndMergedDefault(t *testing.T) {
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("CONTEXT_BEFORE\nMATCH one\nMATCH two\nCONTEXT_AFTER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := cmdIndex([]string{dir}); rc != 0 {
		t.Fatalf("index exited %d", rc)
	}
	run := func(extra ...string) string {
		t.Helper()
		args := append([]string{"MATCH", "--dir", dir, "--scope", "text", "--format", "text"}, extra...)
		return captureStdout(func() {
			if rc := cmdSearch(args); rc != 0 {
				t.Fatalf("search exited %d", rc)
			}
		})
	}
	plain := run("--context", "0", "--no-bodies")
	if strings.Contains(plain, "CONTEXT_BEFORE") || strings.Contains(plain, "CONTEXT_AFTER") {
		t.Fatalf("explicit context=0 was ignored: %s", plain)
	}
	contextual := run("--no-bodies")
	for _, text := range []string{"CONTEXT_BEFORE", "CONTEXT_AFTER", "MATCH one", "MATCH two", "// root: " + dir} {
		if strings.Count(contextual, text) != 1 {
			t.Fatalf("expected exactly one %q in merged context: %s", text, contextual)
		}
	}
}

func TestSearchCLIInvalidContextFails(t *testing.T) {
	for _, args := range [][]string{
		{"MATCH", "--context"}, {"MATCH", "--context", "-1"}, {"MATCH", "--context", "wrong"},
	} {
		if got := cmdSearch(args); got != 2 {
			t.Fatalf("%v: expected usage error, got %d", args, got)
		}
	}
}

func TestSearchCLIIncludeBodiesMatchesMCPOption(t *testing.T) {
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.go"),
		[]byte("package sample\n\nfunc Target() int {\n\treturn 7\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if rc := cmdSearch([]string{"Target", "--dir", dir, "--scope", "symbols", "--format", "text"}); rc != 0 {
			t.Fatalf("search exited %d", rc)
		}
	})
	if !strings.Contains(out, "Exact source for this small, complete locator result") ||
		!strings.Contains(out, "return 7") {
		t.Fatalf("CLI did not use compact MCP's default small-result delivery: %s", out)
	}
	if strings.Contains(out, "locator result — use") {
		t.Fatalf("CLI retained locator guidance after delivering the complete compact result: %s", out)
	}
	locator := captureStdout(func() {
		if rc := cmdSearch([]string{"Target", "--dir", dir, "--scope", "symbols", "--no-bodies", "--format", "text"}); rc != 0 {
			t.Fatalf("search exited %d", rc)
		}
	})
	if strings.Contains(locator, "Exact source") || !strings.Contains(locator, "func Target()") {
		t.Fatalf("CLI --no-bodies did not retain only the locator: %s", locator)
	}
}

func TestSearchCLIContextPayloadPreservesEvidenceWithFewerBytes(t *testing.T) {
	out := map[string]any{
		"root": "/repo", "textBackend": "rg",
		"textHits": []any{map[string]any{
			"file": "src/repeated/long/package/name/search/fixture.go",
			"hits": []any{
				map[string]any{"line": 2, "text": "MATCH one", "before": []string{"before"}, "after": []string{"MATCH two", "after"}},
				map[string]any{"line": 3, "text": "MATCH two", "before": []string{"before", "MATCH one"}, "after": []string{"after"}},
			},
		}},
	}
	legacy := captureStdout(func() { printOutput(out, formatText) })
	merged, ok := mcp.RenderSearchText(out)
	if !ok {
		t.Fatal("could not render fixture")
	}
	for _, evidence := range []string{"before", "MATCH one", "MATCH two", "after", "fixture.go", "// root: /repo"} {
		if !strings.Contains(legacy, evidence) || strings.Count(merged, evidence) != 1 {
			t.Fatalf("evidence lost/duplicated: %q in %s", evidence, merged)
		}
	}
	if len(merged)*2 >= len(legacy) {
		t.Fatalf("expected >50%% fixture payload reduction: %d -> %d", len(legacy), len(merged))
	}
	t.Logf("same source evidence: legacy CLI %d bytes, merged CLI %d bytes (%.1f%% fewer); not a model-token/cost measurement",
		len(legacy), len(merged), 100*(1-float64(len(merged))/float64(len(legacy))))
}
