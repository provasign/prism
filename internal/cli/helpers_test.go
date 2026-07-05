package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setHome points both HOME (unix) and USERPROFILE (what os.UserHomeDir reads
// on Windows) at dir, keeping global writers (Codex, Zed, Claude) off the
// real user configs on every platform.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestRun_Help(t *testing.T) {
	if Run([]string{}) != 0 {
		t.Error("no args")
	}
	if Run([]string{"--help"}) != 0 {
		t.Error("--help")
	}
	if Run([]string{"-h"}) != 0 {
		t.Error("-h")
	}
	if Run([]string{"help"}) != 0 {
		t.Error("help")
	}
	if Run([]string{"version"}) != 0 {
		t.Error("version")
	}
	if Run([]string{"nonsense-cmd"}) != 2 {
		t.Error("unknown")
	}
}

func TestMustAbs(t *testing.T) {
	absIn := filepath.Join(t.TempDir(), "x")
	if got := mustAbs(absIn); got != absIn {
		t.Errorf("abs: got %q want %q", got, absIn)
	}
	if !filepath.IsAbs(mustAbs("rel")) {
		t.Error("rel→abs")
	}
}

func TestDirArg(t *testing.T) {
	if dirArg([]string{"a", "b"}, 0, "def") != "a" {
		t.Error("get")
	}
	if dirArg([]string{"-flag"}, 0, "def") != "def" {
		t.Error("flag skipped")
	}
	if dirArg([]string{}, 0, "def") != "def" {
		t.Error("empty")
	}
}

func TestPrintJSON(t *testing.T) {
	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printJSON(map[string]int{"a": 1})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	var m map[string]int
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["a"] != 1 {
		t.Errorf("got %+v", m)
	}
}

func TestLedgerPathForRoot(t *testing.T) {
	p := ledgerPathForRoot("/x/y/z")
	if !strings.Contains(p, "prism") {
		t.Errorf("got %s", p)
	}
	if !strings.HasSuffix(p, ".json") {
		t.Errorf("got %s", p)
	}
}

func TestPruneOldLedgers_Cov(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	_ = os.WriteFile(old, []byte("{}"), 0o644)
	past := time.Now().Add(-60 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)

	fresh := filepath.Join(dir, "fresh.json")
	_ = os.WriteFile(fresh, []byte("{}"), 0o644)

	// other files ignored
	_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)

	pruneOldLedgers(dir, 30*24*time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old not pruned")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh pruned")
	}
}

func TestPruneOldLedgers_BadDir_Cov(t *testing.T) {
	pruneOldLedgers("/nonexistent/path/xxxx", time.Hour)
}

func TestCmdConfig(t *testing.T) {
	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := cmdConfig([]string{})
	_ = w.Close()
	os.Stdout = old
	_, _ = io.Copy(io.Discard, r)
	if rc != 0 {
		t.Errorf("rc %d", rc)
	}
}

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestPrintOutput_JSON(t *testing.T) {
	got := captureStdout(func() {
		printOutput(map[string]int{"x": 42}, formatJSON)
	})
	var m map[string]int
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["x"] != 42 {
		t.Errorf("got %v", m)
	}
}

func TestPrintOutput_Text_Lookup(t *testing.T) {
	input := map[string]any{
		"symbol":  map[string]any{"name": "MyFunc", "filePath": "pkg/foo.go"},
		"content": "func MyFunc() {}\n",
	}
	got := captureStdout(func() { printOutput(input, formatText) })
	if !strings.Contains(got, "pkg/foo.go") {
		t.Errorf("missing filePath: %q", got)
	}
	if !strings.Contains(got, "MyFunc") {
		t.Errorf("missing name: %q", got)
	}
	if !strings.Contains(got, "func MyFunc()") {
		t.Errorf("missing content: %q", got)
	}
}

func TestPrintOutput_Text_Read(t *testing.T) {
	input := map[string]any{
		"file":    "internal/foo.go",
		"content": "package foo\n",
	}
	got := captureStdout(func() { printOutput(input, formatText) })
	if !strings.Contains(got, "internal/foo.go") {
		t.Errorf("missing file: %q", got)
	}
	if !strings.Contains(got, "package foo") {
		t.Errorf("missing content: %q", got)
	}
}

func TestPrintOutput_Text_Read_ShaPointer(t *testing.T) {
	input := map[string]any{
		"file":     "internal/foo.go",
		"strategy": "sha-pointer",
		"content":  "abc123",
	}
	got := captureStdout(func() { printOutput(input, formatText) })
	if !strings.Contains(got, "[cached") {
		t.Errorf("expected cached marker: %q", got)
	}
	if strings.Contains(got, "abc123") {
		t.Errorf("sha should not appear as content: %q", got)
	}
}

func TestPrintOutput_Text_Query(t *testing.T) {
	input := map[string]any{
		"budgetUsed": 100,
		"symbols": []any{
			map[string]any{
				"filePath": "pkg/a.go",
				"name":     "DoThing",
				"category": "target",
				"content":  "func DoThing() {}\n",
			},
		},
	}
	got := captureStdout(func() { printOutput(input, formatText) })
	if !strings.Contains(got, "pkg/a.go") {
		t.Errorf("missing filePath: %q", got)
	}
	if !strings.Contains(got, "[target]") {
		t.Errorf("missing category: %q", got)
	}
	if !strings.Contains(got, "func DoThing") {
		t.Errorf("missing content: %q", got)
	}
}

func TestPrintOutput_Text_Query_CoverageGaps(t *testing.T) {
	input := map[string]any{
		"budgetUsed": 50,
		"symbols":    []any{},
		"coverageGaps": []any{
			map[string]any{"name": "UntestedFn", "filePath": "pkg/b.go"},
		},
	}
	got := captureStdout(func() { printOutput(input, formatText) })
	if !strings.Contains(got, "coverage_gaps") {
		t.Errorf("missing coverage_gaps header: %q", got)
	}
	if !strings.Contains(got, "UntestedFn") {
		t.Errorf("missing gap name: %q", got)
	}
}

func TestPrintOutput_Lean_Query(t *testing.T) {
	input := map[string]any{
		"budgetUsed": 200,
		"timingMs":   99,
		"phase":      "fts",
		"symbols": []any{
			map[string]any{
				"filePath": "pkg/c.go",
				"name":     "Lean",
				"category": "caller",
				"content":  "func Lean() {}\n",
				"score":    0.99,
				"id":       "abc",
			},
		},
	}
	got := captureStdout(func() { printOutput(input, formatLean) })
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("lean output not valid JSON: %v\n%s", err, got)
	}
	if _, hasPhase := m["phase"]; hasPhase {
		t.Error("lean should strip phase")
	}
	if _, hasTiming := m["timingMs"]; hasTiming {
		t.Error("lean should strip timingMs")
	}
	syms, _ := m["symbols"].([]any)
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	sym := syms[0].(map[string]any)
	if sym["filePath"] != "pkg/c.go" {
		t.Errorf("filePath lost: %v", sym)
	}
	if _, hasScore := sym["score"]; hasScore {
		t.Error("lean should strip score from symbol")
	}
}

func TestPrintOutput_Lean_Read(t *testing.T) {
	input := map[string]any{
		"file":       "pkg/d.go",
		"content":    "package d\n",
		"timingMs":   5,
		"disclosure": "full",
	}
	got := captureStdout(func() { printOutput(input, formatLean) })
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("lean read not valid JSON: %v\n%s", err, got)
	}
	if m["file"] != "pkg/d.go" {
		t.Errorf("file lost: %v", m)
	}
	if _, hasTiming := m["timingMs"]; hasTiming {
		t.Error("lean should strip timingMs")
	}
}
