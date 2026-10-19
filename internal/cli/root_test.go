package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chi pr1158: `cd middleware && prism read content_charset_test.go` built a
// second index in middleware/.grove. The CLI must answer from the project root.
func TestCLIFromSubdirectoryUsesProjectRoot(t *testing.T) {
	root := setupCLIProject(t)
	writeCLIFile(t, root, "prism.yaml", "version: 2\n")
	writeCLIFile(t, root, "middleware/charset.go", "package middleware\n\nfunc ContentCharset() {}\n")
	writeCLIFile(t, root, "middleware/charset_test.go", "package middleware\n\n// tests for ContentCharset\n")
	if rc := cmdIndex([]string{root}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(filepath.Join(root, "middleware"))

	out := captureStdout(func() {
		if rc := cmdRead([]string{"charset_test.go"}); rc != 0 {
			t.Fatalf("read rc=%d", rc)
		}
	})
	if !strings.Contains(out, "tests for ContentCharset") {
		t.Fatalf("read from subdirectory did not find the file:\n%s", out)
	}
	out = captureStdout(func() {
		if rc := cmdSearch([]string{"ContentCharset", "--scope", "text"}); rc != 0 {
			t.Fatalf("search rc=%d", rc)
		}
	})
	if !strings.Contains(out, "middleware/charset.go") {
		t.Fatalf("search did not answer from the root:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "middleware", ".grove")); err == nil {
		t.Fatal("a second index was built in the subdirectory")
	}
}

func TestCLIRootPrefersPrismYAMLOverStrayIndex(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, root, "prism.yaml", "version: 2\n")
	if err := os.MkdirAll(filepath.Join(root, "sub", ".grove"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "sub"))
	got, sub := cliRoot("")
	want, _ := filepath.EvalSymlinks(root)
	if got != want || sub != "sub" {
		t.Fatalf("cliRoot = %q, %q; want %q, sub", got, sub, want)
	}
	if got, sub := cliRoot(filepath.Join(root, "sub")); sub != "" || !strings.HasSuffix(got, "/sub") {
		t.Fatalf("explicit dir must be used as given: %q %q", got, sub)
	}
}

func TestRebaseArgs(t *testing.T) {
	args := map[string]any{
		"file": "a.go", "path": []string{"x", "../top.go"}, "glob": "*.go",
		"ranges": []any{map[string]any{"file": "b.go", "from": 1, "to": 2}},
	}
	rebaseArgs(args, "pkg/sub")
	if args["file"] != "pkg/sub/a.go" || args["glob"] != "pkg/sub/*.go" {
		t.Fatalf("%v", args)
	}
	if p := args["path"].([]string); p[0] != "pkg/sub/x" || p[1] != "pkg/top.go" {
		t.Fatalf("%v", p)
	}
	if f := args["ranges"].([]any)[0].(map[string]any)["file"]; f != "pkg/sub/b.go" {
		t.Fatalf("%v", f)
	}
}
