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
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("before\nMATCH one\nMATCH two\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
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
	plain := run("--context", "0")
	if strings.Contains(plain, "before") || strings.Contains(plain, "after") {
		t.Fatalf("explicit context=0 was ignored: %s", plain)
	}
	contextual := run()
	for _, text := range []string{"before", "after", "MATCH one", "MATCH two", "// root: " + dir} {
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
