package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
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

// jackson pr6039 #51: `lookup BeanDeserializerBase fields:[signature,body]`
// returned 82,816 chars and the host rejected the result. A class over the
// cap delivers its header plus a member outline; a function over the cap
// delivers its first lines and the read call for the rest.
func TestLookupCapsOversizedBodies(t *testing.T) {
	var cls strings.Builder
	cls.WriteString("package big;\n\npublic class Big {\n    private int count;\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&cls, "    public int method%03d(int x) {\n        int y = x + %d;\n        return y * 2;\n    }\n", i, i)
	}
	cls.WriteString("}\n")
	var fn strings.Builder
	fn.WriteString("package p\n\nfunc Huge() int {\n\tx := 0\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&fn, "\tx += %d\n", i)
	}
	fn.WriteString("\treturn x\n}\n")
	srv := compactFixture(t, map[string]string{"src/big/Big.java": cls.String(), "huge.go": fn.String()})

	for _, args := range []map[string]any{
		{"name": "Big"},
		{"name": "Big", "fields": []any{"signature", "body"}},
		{"name": []any{"Big", "Huge"}},
	} {
		out := callCompact(t, srv, "lookup", args)
		if len(out) > bodyCapMaxBytes {
			t.Fatalf("lookup %v delivered %d chars, over the %d cap", args, len(out), bodyCapMaxBytes)
		}
		if !strings.Contains(out, "BODY CAPPED") || !strings.Contains(out, "method119(int x)") || !strings.Contains(out, "private int count") {
			t.Fatalf("lookup %v: capped class lacks the member outline:\n%s", args, out)
		}
		if strings.Contains(out, "int y = x + 7;") {
			t.Fatalf("lookup %v: member bodies delivered past the cap", args)
		}
	}
	out := callCompact(t, srv, "lookup", map[string]any{"name": "Huge"})
	if !strings.Contains(out, "BODY CAPPED") || !strings.Contains(out, "op=read") || strings.Contains(out, "x += 399") {
		t.Fatalf("oversized function not capped with a read continuation:\n%s", out)
	}
	// Numbered lines stay truthful: the first delivered line is the header.
	if !strings.Contains(out, "3\tfunc Huge() int {") {
		t.Fatalf("capped function lost its line numbers:\n%s", out[:minInt(len(out), 400)])
	}
	// Small bodies are untouched.
	small := callCompact(t, compactFixture(t, map[string]string{"a.go": "package p\n\nfunc Small() int { return 1 }\n"}), "lookup", map[string]any{"name": "Small"})
	if strings.Contains(small, "BODY CAPPED") || !strings.Contains(small, "return 1") {
		t.Fatalf("small body changed:\n%s", small)
	}
}

// jackson pr6019 #92: `search readRootValue` (scope=text) named the
// definition in its headline but inlined two callers' bodies, because
// assignment-shaped call lines outscored the declaration line.
func TestSearchDeliversTheNamedDefinitionFirst(t *testing.T) {
	caller := func(cls string) string {
		return "package x;\n\npublic class " + cls + " {\n    Object _readValue(Ctx ctxt, Object p) {\n        Object result;\n        if (p == null) {\n            result = null;\n        } else {\n            result = ctxt.readRootValue(p, null);\n        }\n        return result;\n    }\n}\n"
	}
	srv := compactFixture(t, map[string]string{
		"src/main/java/x/AMapper.java": caller("AMapper"),
		"src/main/java/x/BReader.java": caller("BReader"),
		"src/main/java/x/Ctx.java":     "package x;\n\npublic class Ctx {\n    /*\n     * Extended API\n     */\n\n    public Object readRootValue(Object p,\n            Object valueToUpdate)\n    {\n        return p;\n    }\n}\n",
	})
	for _, scope := range []string{"text", "both"} {
		out := callCompact(t, srv, "search", map[string]any{"terms": []any{"readRootValue"}, "scope": scope})
		// Numbered source lines are delivered bodies; grep lines are not.
		def := strings.Index(out, "8\t    public Object readRootValue(Object p,")
		call := strings.Index(out, "9\t            result = ctxt.readRootValue(p, null);")
		if def < 0 {
			t.Fatalf("scope=%s: definition body not delivered:\n%s", scope, out)
		}
		if call >= 0 && call < def {
			t.Fatalf("scope=%s: a caller body came before the definition:\n%s", scope, out)
		}
	}
}

// jackson pr6030 #33: term "2883" matched UnwrappedPropertyConflict2883Test
// and then every nested member through its qualified name; the text match in
// BeanSerializerBase (the real edit site) was not displayed (0 of 8).
func TestNumericTermDoesNotFloodWithMembersAndKeepsTextMatches(t *testing.T) {
	var test strings.Builder
	test.WriteString("package x;\n\npublic class Conflict2883Test {\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&test, "    public void testUnwrappedPropertyConflictScenarioNumber%02dWithAVeryLongDescriptiveName(Object first, Object second, Object third) {\n        run(%d);\n    }\n", i, i)
	}
	test.WriteString("}\n")
	files := map[string]string{"src/test/java/x/Conflict2883Test.java": test.String(),
		"src/main/java/x/BeanSerializerBase.java": "package x;\n\npublic class BeanSerializerBase {\n    void check() {\n        // [databind#2883]: multiple properties map to same unwrapped name\n        run();\n    }\n}\n"}
	for i := 0; i < 7; i++ {
		files[fmt.Sprintf("src/test/java/x/Other%d.java", i)] = fmt.Sprintf("package x;\n// see #2883 %s\nclass Other%d {}\n", strings.Repeat("filler ", 40), i)
	}
	srv := compactFixture(t, files)
	out := callCompact(t, srv, "search", map[string]any{"terms": []any{"2883", "UnwrappedPropertyHandler"}, "scope": "both"})
	if strings.Contains(out, "Conflict2883Test.testUnwrappedPropertyConflictScenarioNumber") {
		t.Fatalf("members listed through the class name:\n%s", out)
	}
	if !strings.Contains(out, "member(s) of Conflict2883Test matched only through its name") {
		t.Fatalf("collapsed members not disclosed:\n%s", out)
	}
	if !strings.Contains(out, "BeanSerializerBase.java:5") && !strings.Contains(out, "[databind#2883]") {
		t.Fatalf("the text match at the edit site was not displayed:\n%s", out)
	}
}

// Text matches keep a floor under the shared budget even when symbols of the
// same term are too large to be collapsed.
func TestSearchPresentationKeepsATextFloorPerTerm(t *testing.T) {
	var symbols []map[string]any
	for i := 0; i < 150; i++ {
		symbols = append(symbols, map[string]any{
			"name": fmt.Sprintf("Needle%d", i), "qualifiedName": fmt.Sprintf("Needle%d", i), "kind": "class",
			"filePath": fmt.Sprintf("src/very/long/path/to/Needle%d.java", i), "matchKind": "name-prefix",
			"span":      map[string]any{"start": 1, "end": 40},
			"signature": "public class Needle " + strings.Repeat("LongGenericParameter", 4),
		})
	}
	var groups []map[string]any
	for i := 0; i < 8; i++ {
		groups = append(groups, map[string]any{"file": fmt.Sprintf("src/main/F%d.java", i), "hits": []any{
			map[string]any{"line": 5, "text": "// Needle " + strings.Repeat("context ", 60)},
		}})
	}
	out := map[string]any{"symbols": symbols, "textHits": groups}
	boundSearchPresentation(out)
	boundSymbolPresentation(out)
	if n := len(anySlice(out["textHits"])); n < 3 {
		t.Fatalf("text matches displayed = %d, want at least 3", n)
	}
	text, _ := renderSearchAsText(out)
	if ranking.EstimateTokens(text) > 4000 {
		t.Fatalf("budget exceeded: %d tokens", ranking.EstimateTokens(text))
	}
}

// gin: symbol search for "Errors" listed every symbol in errors.go (matched
// by path) after the two name matches.
func TestSymbolSearchCapsPathOnlyMatches(t *testing.T) {
	var errs strings.Builder
	errs.WriteString("package gin\n\ntype errorMsgs []string\n\nfunc (a errorMsgs) Errors() []string { return a }\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&errs, "\nfunc helper%d() int { return %d }\n", i, i)
	}
	srv := compactFixture(t, map[string]string{"errors.go": errs.String(),
		"context.go": "package gin\n\ntype Context struct {\n\tErrors errorMsgs\n}\n"})
	out := callCompact(t, srv, "search", map[string]any{"terms": "Errors", "scope": "symbols"})
	if strings.Count(out, "[path]") > pathOnlySymbolCap {
		t.Fatalf("path-only matches not capped:\n%s", out)
	}
	if !strings.Contains(out, "matched only by file path (errors.go)") || !strings.Contains(out, "errorMsgs.Errors") {
		t.Fatalf("path cap not disclosed or name match lost:\n%s", out)
	}
}

// gin: symbol search labelled real test functions (TestErrorSlice) and test
// helpers "[test double]". Only mock/fake/stub code is a test double.
func TestSearchLabelsTestFunctionsAsTestsNotDoubles(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"errors.go":      "package gin\n\nfunc ErrorSlice() []error { return nil }\n",
		"errors_test.go": "package gin\n\nimport \"testing\"\n\nfunc TestErrorSlice(t *testing.T) { _ = ErrorSlice() }\n",
		"mock_errors.go": "package gin\n\nfunc MockErrorSlice() []error { return nil }\n",
	})
	out := callCompact(t, srv, "search", map[string]any{"terms": "ErrorSlice", "scope": "symbols"})
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "function TestErrorSlice"):
			if strings.Contains(line, "[test double]") || !strings.Contains(line, "[test]") {
				t.Errorf("test function mislabelled: %q", line)
			}
		case strings.Contains(line, "function MockErrorSlice"):
			if !strings.Contains(line, "[test double]") {
				t.Errorf("mock not labelled a test double: %q", line)
			}
		case strings.Contains(line, "function ErrorSlice "):
			if strings.Contains(line, "[test") {
				t.Errorf("production symbol labelled test: %q", line)
			}
		}
	}
	if !strings.Contains(out, "function TestErrorSlice") {
		t.Fatalf("test function missing:\n%s", out)
	}
}
