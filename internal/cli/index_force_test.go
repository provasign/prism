package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BUG-07: the engine's no-change hint says "index --force"; prism index
// must accept it and re-run the analyzers a no-change index skips.
func TestCmdIndexForceRerunsSkippedAnalyzers(t *testing.T) {
	dir := setupCLIProject(t)
	_ = writeCLIFile(t, dir, "go.mod", "module example.com/fixture\n\ngo 1.22\n")
	if got := cmdIndex([]string{dir}); got != 0 {
		t.Fatalf("first index = %d", got)
	}
	var rc int
	again := captureStdout(func() { rc = cmdIndex([]string{dir}) })
	if rc != 0 {
		t.Fatalf("second index = %d", rc)
	}
	if !strings.Contains(again, "skipped: no file changes since last index") || !strings.Contains(again, "index --force") {
		t.Fatalf("no-change index should skip analyzers and name index --force:\n%s", again)
	}
	forced := captureStdout(func() { rc = cmdIndex([]string{dir, "--force", "--format", "json"}) })
	if rc != 0 {
		t.Fatalf("index --force = %d\n%s", rc, forced)
	}
	if strings.Contains(forced, "skipped: no file changes") {
		t.Fatalf("index --force still skipped the analyzers:\n%s", forced)
	}
	// main.go, main_test.go and the fixture's prism.yaml.
	if !strings.Contains(forced, `"filesUpdated": 3`) || !strings.Contains(forced, "go: full pass") {
		t.Errorf("index --force should re-extract every file and re-run the Go pass:\n%s", forced)
	}
}

func TestCmdIndexRebuildRecreatesIndex(t *testing.T) {
	dir := setupCLIProject(t)
	if got := cmdIndex([]string{dir}); got != 0 {
		t.Fatalf("first index = %d", got)
	}
	db := filepath.Join(dir, ".grove", "grove.db")
	// A store that is not a database: open fails until it is rebuilt.
	if err := os.WriteFile(db, []byte("not a sqlite database, padded past the header size................................................"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(db + "-wal")
	_ = os.Remove(db + "-shm")
	var rc int
	stderr := captureStderr(func() { _ = captureStdout(func() { rc = cmdIndex([]string{dir}) }) })
	if rc == 0 || !strings.Contains(stderr, "prism index --rebuild") {
		t.Fatalf("index over a corrupt store: rc=%d, want a failure naming --rebuild:\n%s", rc, stderr)
	}
	out := captureStdout(func() { rc = cmdIndex([]string{dir, "--rebuild"}) })
	if rc != 0 {
		t.Fatalf("index --rebuild = %d\n%s", rc, out)
	}
	if got := cmdStatus([]string{dir}); got != 0 {
		t.Fatalf("status after rebuild = %d", got)
	}
}

func TestCmdIndexHelpListsForceAndRebuild(t *testing.T) {
	for _, flag := range []string{"--force", "--rebuild", "--allow-heuristic"} {
		if !strings.Contains(helpText, flag) {
			t.Errorf("helpText lacks %s", flag)
		}
	}
}
