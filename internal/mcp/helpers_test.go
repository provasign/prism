package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
)

func TestIntArg(t *testing.T) {
	if intArg(map[string]any{"x": 5}, "x", 0) != 5 {
		t.Error("int")
	}
	if intArg(map[string]any{"x": int64(7)}, "x", 0) != 7 {
		t.Error("int64")
	}
	if intArg(map[string]any{"x": 3.0}, "x", 0) != 3 {
		t.Error("float")
	}
	if intArg(map[string]any{"x": json.Number("9")}, "x", 0) != 9 {
		t.Error("json.Number")
	}
	if intArg(map[string]any{}, "x", 42) != 42 {
		t.Error("default")
	}
	if intArg(map[string]any{"x": "str"}, "x", 11) != 11 {
		t.Error("string ignored")
	}
}

func TestMinInt(t *testing.T) {
	if minInt(1, 2) != 1 || minInt(5, 3) != 3 {
		t.Error("minInt")
	}
}

func TestSummarize(t *testing.T) {
	if summarize("   abc   ", 100) != "abc" {
		t.Error("trim")
	}
	if summarize("abcdef", 3) != "abc…" {
		t.Error("trunc")
	}
}

func TestSafePathWithinRoot_Cov(t *testing.T) {
	root := t.TempDir()
	if _, _, err := safePathWithinRoot(root, "x.go"); err != nil {
		t.Error(err)
	}
	if _, _, err := safePathWithinRoot(root, "../escape"); err == nil {
		t.Error("expected escape error")
	}
}

func TestToolRead_OK(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	p := filepath.Join(h.Root, "f.go")
	_ = os.WriteFile(p, []byte("package x\nfunc F(){}\n"), 0o644)
	out, err := h.Invoke("prism_read", map[string]any{"file": "f.go"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil")
	}
}

func TestToolRead_BadPath(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_read", map[string]any{"file": "../escape"}); err == nil {
		t.Error("expected err")
	}
}

func TestToolRead_NotFound(t *testing.T) {
	srv := fakeGroveSrv(t, map[string]any{"symbols": []map[string]any{}})
	defer srv.Close()
	h := newHWithGrove(t, srv)
	if _, err := h.Invoke("prism_read", map[string]any{"file": "nope.go"}); err == nil {
		t.Error("expected err")
	}
}

func TestIsMarkdownStringConst_DocConst(t *testing.T) {
	raw := "const x = `\n## Section\n\n| col | col2 |\n|---|---|\n- item one\n- item two\n`"
	if !isMarkdownStringConst(raw) {
		t.Error("expected markdown const to be detected")
	}
}

func TestIsMarkdownStringConst_ShortConst(t *testing.T) {
	if isMarkdownStringConst("const x = 42") {
		t.Error("short const should not be flagged")
	}
}

func TestIsMarkdownStringConst_NonMarkdown(t *testing.T) {
	raw := "const defaultBudget = 8000\n// line\n// line\n// line\n// line\n// line"
	if isMarkdownStringConst(raw) {
		t.Error("code const should not be flagged")
	}
}

func TestCategorize_MarkdownConst(t *testing.T) {
	raw := "const steeringInstructions = `\n## Prism\n\n| col | col2 |\n|---|---|\n- item one\n- item two\n`"
	sym := grove.SymbolRecord{
		Kind:    "const",
		RawText: raw,
	}
	if got := categorize(sym); got != ranking.CategoryDoc {
		t.Errorf("markdown const should be CategoryDoc, got %q", got)
	}
}

func TestIsTestWritingTask_Positive(t *testing.T) {
	for _, task := range []string{
		"write tests for buildAntiContextManifest",
		"add test coverage for toolQuery",
		"write test for Select",
		"tests for the ranking package",
		"coverage for the compression module",
		"need to test the new parameter",
	} {
		if !isTestWritingTask(task) {
			t.Errorf("expected %q to be detected as test-writing task", task)
		}
	}
}

func TestIsTestWritingTask_Negative(t *testing.T) {
	for _, task := range []string{
		"implement a new parameter",
		"fix the sha-pointer bug",
		"refactor toolRead",
	} {
		if isTestWritingTask(task) {
			t.Errorf("expected %q to NOT be a test-writing task", task)
		}
	}
}
