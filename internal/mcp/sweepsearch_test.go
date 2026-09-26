package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// Regression tests for search/query/output gaps seen in real benchmark runs
// (rerun + pinned beds, 2026-09-25). Each test replays the shape of the call
// an agent made.

func compactFixture(t *testing.T, files map[string]string) *Server {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	return NewCompactServer(NewHandler(config.Default(), root, gc))
}

func callCompact(t *testing.T, srv *Server, op string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "prism", "arguments": map[string]any{"op": op, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := srv.dispatch("tools/call", raw)
	if rpcErr != nil {
		t.Fatalf("%s %v: %s", op, args, rpcErr.Message)
	}
	var b strings.Builder
	for _, c := range result.(map[string]any)["content"].([]map[string]string) {
		b.WriteString(c["text"])
	}
	return b.String()
}

var srcLayoutPython = map[string]string{
	"src/requests/models.py": "from .utils import requote_uri\n\n\nclass PreparedRequest:\n    def prepare_url(self, url):\n        return requote_uri(url)\n",
	"src/requests/utils.py":  "def requote_uri(uri):\n    return uri\n",
	"tests/test_utils.py":    "from requests.utils import requote_uri\n\n\ndef test_requote_uri():\n    assert requote_uri('a') == 'a'\n",
}

// requests pr7315 (rerun #6/#7, pinned #100): paths=["requests/models.py"] in
// a src/ layout returned "no matches ... retry broader/shorter" for every
// term. The filter matched no file; say so and name the real path.
func TestScopedSearchNamesAFilterThatMatchesNoFiles(t *testing.T) {
	srv := compactFixture(t, srcLayoutPython)
	for _, scope := range []string{"symbols", "text", "both"} {
		out := callCompact(t, srv, "search", map[string]any{
			"terms": []any{"requote_uri", "prepare_url"}, "scope": scope,
			"paths": []any{"requests/models.py", "requests/utils.py"},
		})
		if !strings.Contains(out, "FILTER MATCHED NO FILES") ||
			!strings.Contains(out, "src/requests/models.py") || !strings.Contains(out, "src/requests/utils.py") {
			t.Fatalf("scope=%s: filter miss not named with the real paths:\n%s", scope, out)
		}
		if strings.Contains(out, "Retry broader/shorter") {
			t.Errorf("scope=%s: blamed the terms for a filter miss:\n%s", scope, out)
		}
	}
	// Single term takes the flat path.
	out := callCompact(t, srv, "search", map[string]any{"terms": "requote_uri", "paths": "requests/utils.py"})
	if !strings.Contains(out, "FILTER MATCHED NO FILES") || !strings.Contains(out, "src/requests/utils.py") {
		t.Fatalf("single-term filter miss not named:\n%s", out)
	}
	// A glob that selects nothing.
	out = callCompact(t, srv, "search", map[string]any{"terms": "requote_uri", "glob": "**/tools/jackson/core/JsonParser.java"})
	if !strings.Contains(out, "FILTER MATCHED NO FILES") || !strings.Contains(out, "matches no file") {
		t.Fatalf("empty glob not named:\n%s", out)
	}
	// A valid filter with a missing term keeps the normal term guidance.
	out = callCompact(t, srv, "search", map[string]any{"terms": "no_such_name_here", "paths": "src/requests/utils.py"})
	if strings.Contains(out, "FILTER") {
		t.Fatalf("valid filter reported as a miss:\n%s", out)
	}
	// One valid and one missing path: search the valid one, flag the other.
	out = callCompact(t, srv, "search", map[string]any{"terms": "requote_uri", "paths": []any{"src/requests/utils.py", "requests/models.py"}})
	if !strings.Contains(out, "FILTER PARTLY INVALID") || !strings.Contains(out, "src/requests/models.py") {
		t.Fatalf("partly invalid filter not named:\n%s", out)
	}
}

// jackson pr6019 #85: query with a glob naming a dependency file said "no
// symbols matched ... check term spelling".
func TestScopedQueryNamesAFilterThatMatchesNoFiles(t *testing.T) {
	srv := compactFixture(t, srcLayoutPython)
	out := callCompact(t, srv, "query", map[string]any{"terms": []any{"requote_uri"}, "paths": []any{"requests/utils.py"}})
	if !strings.Contains(out, "FILTER MATCHED NO FILES") || !strings.Contains(out, "src/requests/utils.py") {
		t.Fatalf("query filter miss not named:\n%s", out)
	}
	if strings.Contains(out, "check term spelling") {
		t.Fatalf("query blamed term spelling for a filter miss:\n%s", out)
	}
	out = callCompact(t, srv, "query", map[string]any{"terms": []any{"assignCurrentValue"}, "glob": []any{"**/tools/jackson/core/JsonParser.java"}})
	if !strings.Contains(out, "FILTER MATCHED NO FILES") {
		t.Fatalf("query glob miss not named:\n%s", out)
	}
}

// click pr3471 #26: scope=symbols glob=["**/types.py"] returned zero symbols;
// text scope with the same glob found all of them.
func TestSymbolScopeHonorsDoubleStarGlob(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"src/click/types.py": "class ParamType:\n    def shell_complete(self, ctx, param, incomplete):\n        return []\n\n\nclass Choice(ParamType):\n    def shell_complete(self, ctx, param, incomplete):\n        return list(self.choices)\n",
		"src/click/core.py":  "class Option:\n    def shell_complete(self, ctx, incomplete):\n        return []\n",
	})
	for _, scope := range []string{"symbols", "both"} {
		out := callCompact(t, srv, "search", map[string]any{"terms": []any{"shell_complete", "class Choice"}, "scope": scope, "glob": []any{"**/types.py"}})
		if !strings.Contains(out, "Choice.shell_complete") || !strings.Contains(out, "ParamType.shell_complete") {
			t.Fatalf("scope=%s: **/types.py returned no symbols:\n%s", scope, out)
		}
		if strings.Contains(out, "Option.shell_complete") {
			t.Fatalf("scope=%s: glob leaked core.py:\n%s", scope, out)
		}
	}
}
