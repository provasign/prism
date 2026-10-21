package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A compact CLI read is served from disk without the graph, as the MCP server
// serves reads before its first index: an unusable index does not fail a
// readable file, and a cold read does not build an index.
func TestReadDoesNotNeedTheGraph(t *testing.T) {
	broken := t.TempDir()
	writeCLIFile(t, broken, "README.md", "Readable source\n")
	writeCLIFile(t, broken, ".grove/grove.db", "invalid database bytes")
	out := captureStdout(func() {
		if rc := cmdRead([]string{"README.md", "--dir", broken}); rc != 0 {
			t.Fatalf("read with unusable index rc=%d, want 0", rc)
		}
	})
	if !strings.Contains(out, "Readable source") {
		t.Fatalf("file text not delivered:\n%s", out)
	}

	cold := t.TempDir()
	writeCLIFile(t, cold, "a.md", "one\ntwo\n")
	writeCLIFile(t, cold, "b.md", "three\n")
	out = captureStdout(func() {
		if rc := cmdRead([]string{"a.md", "--from", "2", "--dir", cold}); rc != 0 {
			t.Fatalf("ranged read rc=%d", rc)
		}
		if rc := cmdRead([]string{"a.md", "b.md", "--dir", cold}); rc != 0 {
			t.Fatalf("multi-file read rc=%d", rc)
		}
	})
	if !strings.Contains(out, "two") || !strings.Contains(out, "three") {
		t.Fatalf("reads not delivered:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cold, ".grove")); !os.IsNotExist(err) {
		t.Fatalf("a cold read built an index (.grove exists, err=%v)", err)
	}
}

// The graph-free path keeps the read's root boundary: a symlink or a path
// leaving the project is still refused.
func TestReadGraphFreeKeepsRootBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	writeCLIFile(t, outside, "secret.txt", "outside the root\n")
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	var rc int
	out := captureStdout(func() { rc = cmdRead([]string{"link.txt", "--dir", root}) })
	if rc == 0 || strings.Contains(out, "outside the root") {
		t.Fatalf("symlink outside the root was read: rc=%d\n%s", rc, out)
	}
	rel, err := filepath.Rel(root, filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out = captureStdout(func() { rc = cmdRead([]string{rel, "--dir", root}) })
	if rc == 0 || strings.Contains(out, "outside the root") {
		t.Fatalf("path outside the root was read: rc=%d\n%s", rc, out)
	}
}
