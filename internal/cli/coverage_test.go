package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCLIFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func setupCLIProject(t *testing.T) string {
	dir := t.TempDir()
	_ = writeCLIFile(t, dir, "main.go", "package main\n\nfunc Main() {}\n")
	_ = writeCLIFile(t, dir, "main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) { Main() }\n")
	return dir
}

func TestCmdIndexAndStatus_Smoke(t *testing.T) {
	dir := setupCLIProject(t)
	if got := cmdIndex([]string{dir}); got != 0 {
		t.Fatalf("cmdIndex=%d", got)
	}
	if got := cmdStatus([]string{dir}); got != 0 {
		t.Fatalf("cmdStatus=%d", got)
	}
}

func TestCmdQueryAndSearchAndLookup_Smoke(t *testing.T) {
	dir := setupCLIProject(t)
	if got := cmdIndex([]string{dir}); got != 0 {
		t.Fatalf("cmdIndex=%d", got)
	}
	if got := cmdQuery([]string{"main entry point", "--limit", "10", "--profile", "default", dir}); got != 0 {
		t.Fatalf("cmdQuery=%d", got)
	}
	if got := cmdSearch([]string{"Main", "--limit", "5", dir}); got != 0 {
		t.Fatalf("cmdSearch=%d", got)
	}
	// Query one known symbol from search path.
	if got := cmdLookup([]string{"Main", dir}); got != 0 {
		t.Fatalf("cmdLookup=%d", got)
	}
}

func TestCmdRead_Smoke(t *testing.T) {
	dir := setupCLIProject(t)
	if got := cmdRead([]string{"main.go", dir}); got != 0 {
		t.Fatalf("cmdRead=%d", got)
	}
}

func TestCmdUsageErrors(t *testing.T) {
	if got := cmdQuery([]string{}); got != 2 {
		t.Fatalf("cmdQuery usage=%d", got)
	}
	if got := cmdRead([]string{}); got != 2 {
		t.Fatalf("cmdRead usage=%d", got)
	}
	if got := cmdSearch([]string{}); got != 2 {
		t.Fatalf("cmdSearch usage=%d", got)
	}
	if got := cmdLookup([]string{}); got != 2 {
		t.Fatalf("cmdLookup usage=%d", got)
	}
}

func TestCmdCompact_BadAndGoodStdin(t *testing.T) {
	dir := setupCLIProject(t)

	r1, w1, _ := os.Pipe()
	_, _ = w1.WriteString("not-json")
	_ = w1.Close()
	old := os.Stdin
	os.Stdin = r1
	if got := cmdCompact([]string{dir}); got != 2 {
		t.Fatalf("cmdCompact bad stdin=%d", got)
	}
	os.Stdin = old

	r2, w2, _ := os.Pipe()
	_, _ = w2.WriteString(`[{"role":"user","content":"hello"}]`)
	_ = w2.Close()
	os.Stdin = r2
	if got := cmdCompact([]string{dir}); got != 0 {
		t.Fatalf("cmdCompact good stdin=%d", got)
	}
	os.Stdin = old
}

func TestCmdFeedbackAndSavings_Smoke(t *testing.T) {
	dir := setupCLIProject(t)

	if got := cmdFeedback([]string{}); got != 2 {
		t.Fatalf("cmdFeedback usage=%d", got)
	}
	if got := cmdFeedback([]string{"--rating", "10", dir}); got != 2 {
		t.Fatalf("cmdFeedback invalid rating=%d", got)
	}
	if got := cmdFeedback([]string{"--rating", "4", "--tool", "prism_query", "--notes", "ok", dir}); got != 0 {
		t.Fatalf("cmdFeedback=%d", got)
	}
	if got := cmdSavings([]string{dir}); got != 0 {
		t.Fatalf("cmdSavings=%d", got)
	}
}

func TestPruneOldLedgers_CoverageSmoke(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	newf := filepath.Join(dir, "new.json")
	_ = os.WriteFile(old, []byte("{}"), 0o644)
	_ = os.WriteFile(newf, []byte("{}"), 0o644)
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	pruneOldLedgers(dir, time.Hour)
	if _, err := os.Stat(old); err == nil {
		t.Fatalf("old ledger not pruned")
	}
	if _, err := os.Stat(newf); err != nil {
		t.Fatalf("new ledger should remain: %v", err)
	}
}

func TestInvokeWithPersistentLedger_Smoke(t *testing.T) {
	dir := setupCLIProject(t)
	out, err := invokeWithPersistentLedger(dir, "prism_savings", nil)
	if err != nil {
		t.Fatalf("invokeWithPersistentLedger: %v", err)
	}
	if out == nil {
		t.Fatal("expected output")
	}
}

func TestRun_DispatchSmoke(t *testing.T) {
	if got := Run([]string{}); got != 0 {
		t.Fatalf("Run empty=%d", got)
	}
	if got := Run([]string{"help"}); got != 0 {
		t.Fatalf("Run help=%d", got)
	}
	if got := Run([]string{"version"}); got != 0 {
		t.Fatalf("Run version=%d", got)
	}
	if got := Run([]string{"unknown-subcmd"}); got != 2 {
		t.Fatalf("Run unknown=%d", got)
	}
}

func TestCmdMCP_ErrorPath(t *testing.T) {
	dir := t.TempDir()
	fileRoot := writeCLIFile(t, dir, "not-a-dir.txt", "x")
	if got := cmdMCP([]string{fileRoot}); got != 1 {
		t.Fatalf("cmdMCP error path=%d", got)
	}
}

func TestCmdMCP_EOFReturnsZero(t *testing.T) {
	dir := setupCLIProject(t)

	r, w, _ := os.Pipe()
	_ = w.Close() // immediate EOF
	oldIn := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldIn }()

	if got := cmdMCP([]string{dir}); got != 0 {
		t.Fatalf("cmdMCP eof=%d", got)
	}
}

func TestRun_SubcommandUsagePaths(t *testing.T) {
	if got := Run([]string{"query"}); got != 2 {
		t.Fatalf("Run query usage=%d", got)
	}
	if got := Run([]string{"read"}); got != 2 {
		t.Fatalf("Run read usage=%d", got)
	}
	if got := Run([]string{"search"}); got != 2 {
		t.Fatalf("Run search usage=%d", got)
	}
	if got := Run([]string{"lookup"}); got != 2 {
		t.Fatalf("Run lookup usage=%d", got)
	}
	if got := Run([]string{"feedback"}); got != 2 {
		t.Fatalf("Run feedback usage=%d", got)
	}
}

func TestCLIHelpersAndConfigPaths(t *testing.T) {
	dir := setupCLIProject(t)

	if got := cmdConfig([]string{dir}); got != 0 {
		t.Fatalf("cmdConfig=%d", got)
	}

	if p := detectSelfPath(); p == "" {
		t.Fatal("detectSelfPath returned empty path")
	}
	if p := detectGrovePath(); p == "" {
		t.Fatal("detectGrovePath returned empty path")
	}

	// newClient should reject a file path used as a repo root.
	badRoot := writeCLIFile(t, t.TempDir(), "not-dir.txt", "x")
	if _, _, err := newClient(badRoot); err == nil {
		t.Fatal("expected newClient to fail for non-directory root")
	}

	// isExecutable helper branches.
	execFile := writeCLIFile(t, t.TempDir(), "bin/tool", "#!/bin/sh\n")
	if err := os.Chmod(execFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(execFile) {
		t.Fatal("expected executable file")
	}
	if isExecutable(filepath.Dir(execFile)) {
		t.Fatal("directory must not be executable target")
	}
}
