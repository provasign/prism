package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CON-06: init run from a binary outside PATH writes no machine path into
// the files meant to be committed. The test binary itself lives in a temp
// build directory, so its directory must not appear anywhere.
func TestInitSharedConfigIsPortable(t *testing.T) {
	dir := t.TempDir()
	self := detectSelfPath()
	captureStdout(func() {
		if rc := cmdInit([]string{"--harness", "claude,cursor,vscode,gemini,codex", "--interface", "mcp", dir}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	for _, rel := range []string{".mcp.json", ".cursor/mcp.json", ".vscode/mcp.json", ".gemini/settings.json", ".codex/config.toml"} {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("%s not written: %v", rel, err)
		}
		real, _ := filepath.EvalSymlinks(dir)
		for _, bad := range []string{filepath.Dir(self), dir, real} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("%s names the machine path %s:\n%s", rel, bad, raw)
			}
		}
		if !strings.Contains(string(raw), `"prism"`) {
			t.Errorf("%s does not run plain prism:\n%s", rel, raw)
		}
	}
}

// CON-06: doctor flags an absolute and a missing command in shared config.
func TestSharedConfigWarnings(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"prism":{"command":"/nonexistent/bin/prism","args":["mcp"]}}}`)
	mustWriteFile(t, filepath.Join(dir, ".cursor", "mcp.json"), `{"mcpServers":{"prism":{"command":"prism","args":["mcp"]}}}`)
	mustWriteFile(t, filepath.Join(dir, ".codex", "config.toml"), "[mcp_servers.prism]\ncommand = \"/opt/x/prism\"\nargs = [\"mcp\"]\n")
	w := sharedConfigWarnings(dir)
	if len(w) != 2 || !strings.Contains(w[1], ".codex/config.toml") || !strings.Contains(w[0], ".mcp.json") || !strings.Contains(w[0], "does not exist") {
		t.Fatalf("warnings: %q", w)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// CON-11: a query in a directory that is no project does not create .grove;
// an explicit prism index still builds there.
func TestQueryRefusesNonProjectDir(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "notes.txt"), "hello\n")
	var rc int
	stderr := captureStderr(func() { _ = captureStdout(func() { rc = cmdLookup([]string{"Hello", "--dir", dir}) }) })
	if rc != exitUsage || !strings.Contains(stderr, "no project here") {
		t.Fatalf("lookup rc=%d stderr=%q", rc, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".grove")); err == nil {
		t.Fatal("a refused query created .grove")
	}
	captureStdout(func() {
		if rc := cmdIndex([]string{dir}); rc != 0 {
			t.Fatalf("explicit index rc=%d", rc)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, ".grove")); err != nil {
		t.Fatal("explicit index did not build")
	}
}

// GAP-09: status and doctor count files the graph does not cover.
func TestNoGraphSummary(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.go"), "package a\n")
	mustWriteFile(t, filepath.Join(dir, "lib", "x.rb"), "x\n")
	mustWriteFile(t, filepath.Join(dir, "lib", "y.rb"), "y\n")
	if got := noGraphSummary(dir); !strings.Contains(got, "2 Ruby file(s)") {
		t.Fatalf("summary: %q", got)
	}
	if got := noGraphSummary(t.TempDir()); got != "" {
		t.Fatalf("empty dir: %q", got)
	}
}
