package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	_ = writeCLIFile(t, dir, "main.go", "package main\n\n"+strings.Repeat("// fixture padding for cache pointer savings\n", 20)+"func Main() {}\n")
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
	if got := cmdQuery([]string{"main entry point", "--terms", "Main,init", dir}); got != 0 {
		t.Fatalf("cmdQuery --terms=%d", got)
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

func TestInvokeWithPersistentLedger_PersistsSessionCache(t *testing.T) {
	dir := setupCLIProject(t)
	if _, err := invokeWithPersistentLedger(dir, "prism_read", map[string]any{"file": "main.go"}); err != nil {
		t.Fatalf("first prism_read: %v", err)
	}
	out, err := invokeWithPersistentLedger(dir, "prism_read", map[string]any{"file": "main.go"})
	if err != nil {
		t.Fatalf("second prism_read: %v", err)
	}
	got, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if got["strategy"] != "sha-pointer" {
		t.Fatalf("expected second CLI read to use persisted sha-pointer cache, got %v", got["strategy"])
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

	// newClient should reject a file path used as a repo root.
	badRoot := writeCLIFile(t, t.TempDir(), "not-dir.txt", "x")
	if _, _, err := newClient(badRoot); err == nil {
		t.Fatal("expected newClient to fail for non-directory root")
	}

}

// TestCmdErrorPaths_FileAsDir exercises the error-return branches of several
// cmd functions. Using a regular file as the project root causes
// config.LoadFromDir to return ENOTDIR (not IsNotExist), which propagates as
// a non-nil error through newClient and invokeWithPersistentLedger.
// On Windows, ERROR_PATH_NOT_FOUND is treated as IsNotExist, so the function
// returns success instead of an error — skip there.
func TestCmdErrorPaths_FileAsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ENOTDIR treated as IsNotExist on Windows")
	}
	f, err := os.CreateTemp("", "prism_not_a_dir*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())
	notADir := f.Name()

	if rc := cmdConfig([]string{notADir}); rc != 1 {
		t.Errorf("cmdConfig file-as-dir: want rc=1, got %d", rc)
	}
	if rc := cmdStatus([]string{notADir}); rc != 1 {
		t.Errorf("cmdStatus file-as-dir: want rc=1, got %d", rc)
	}
	if rc := cmdSavings([]string{notADir}); rc != 1 {
		t.Errorf("cmdSavings file-as-dir: want rc=1, got %d", rc)
	}
	if rc := cmdIndex([]string{notADir}); rc != 1 {
		t.Errorf("cmdIndex file-as-dir: want rc=1, got %d", rc)
	}
}
