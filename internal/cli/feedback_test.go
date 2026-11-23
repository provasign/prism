package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// BUG-12: CLI feedback is persisted and listable; CLI drift refuses
// instead of reporting an empty success.
func TestFeedbackPersistsAndLists(t *testing.T) {
	dir := setupCLIProject(t)
	for _, notes := range []string{"first", "second"} {
		var rc int
		_ = captureStdout(func() { rc = cmdFeedback([]string{"--tool", "prism_search", "--rating", "4", "--notes", notes, dir}) })
		if rc != 0 {
			t.Fatalf("feedback rc=%d", rc)
		}
	}
	var rc int
	out := captureStdout(func() { rc = cmdFeedback([]string{"--list", dir}) })
	if rc != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, `"notes":"second"`) {
		t.Fatalf("list rc=%d:\n%s", rc, out)
	}
}

func TestDriftCLIRefuses(t *testing.T) {
	var rc int
	stderr := captureStderr(func() { rc = cmdDrift(nil) })
	if rc != 2 || !strings.Contains(stderr, "MCP only") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}

// BUG-21: map --component with an unknown name fails and lists the names.
func TestMapUnknownComponentFails(t *testing.T) {
	dir := setupCLIProject(t)
	var rc int
	stderr := captureStderr(func() { _ = captureStdout(func() { rc = cmdMap([]string{dir, "--component", "nope"}) }) })
	if rc == 0 || !strings.Contains(stderr, `no component "nope"`) {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}

// SEC-02: serve writes a fresh token 0600 under .grove, or reads the
// operator's token file; a short token is refused.
func TestServeToken(t *testing.T) {
	root := t.TempDir()
	tok, path, err := serveToken(root, "")
	if err != nil || len(tok) != 64 || path != filepath.Join(root, ".grove", "serve.token") {
		t.Fatalf("tok=%q path=%q err=%v", tok, path, err)
	}
	// Windows has no Unix permission bits; the 0600 check is Unix-only.
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("token file mode: %v %v", fi, err)
	}
	own := filepath.Join(root, "mine")
	if err := os.WriteFile(own, []byte("0123456789abcdef-operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := serveToken(root, own); err != nil || got != "0123456789abcdef-operator" {
		t.Fatalf("token file: %q %v", got, err)
	}
	if err := os.WriteFile(own, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := serveToken(root, own); err == nil {
		t.Fatal("short token accepted")
	}
	if rc := cmdServe([]string{"--listen", "0.0.0.0", root}); rc != 2 {
		t.Fatalf("non-loopback listen without --token-file: rc=%d", rc)
	}
}
