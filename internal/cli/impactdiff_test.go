package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GAP-05: change-impact --diff reports, per changed symbol, the sites the
// diff did not touch (from the base callers too, since a broken call no
// longer resolves) and the direct callers of a body-only change.
func TestChangeImpactDiff(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, dir, "go.mod", "module example.com/df\n\ngo 1.22\n")
	writeCLIFile(t, dir, "a.go", "package df\n\nfunc Sum(a int) int { return a }\n\nfunc Twice(x int) int { return x * 2 }\n")
	writeCLIFile(t, dir, "b.go", "package df\n\nfunc One() int { return Sum(1) }\n\nfunc Two() int { return Sum(2) }\n\nfunc Use() int { return Twice(3) }\n")
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	// Signature change, one caller updated; body change to Twice.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package df\n\nfunc Sum(a, b int) int { return a + b }\n\nfunc Twice(x int) int { return x + x }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package df\n\nfunc One() int { return Sum(1, 0) }\n\nfunc Two() int { return Sum(2) }\n\nfunc Use() int { return Twice(3) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rc int
	out := captureStdout(func() { _ = captureStderr(func() { rc = cmdChangeImpact([]string{"--diff", dir}) }) })
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	sum := out[strings.Index(out, "Sum ("):]
	if !strings.Contains(sum, "not touched by the diff:") || !strings.Contains(sum, "b.go:5") {
		t.Fatalf("untouched caller Two (b.go:5) not reported:\n%s", out)
	}
	if !strings.Contains(out, "Twice (") || !strings.Contains(out, "behaviour may change") || !strings.Contains(out, "Use  b.go:7") {
		t.Fatalf("body change callers not reported:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "risk") {
		t.Fatalf("no risk score:\n%s", out)
	}
}
