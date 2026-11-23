package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ciFixture(t *testing.T, baseYAML string) string {
	t.Helper()
	dir := t.TempDir()
	writeCLIFile(t, dir, "go.mod", "module example.com/ci\n\ngo 1.22\n")
	writeCLIFile(t, dir, "a.go", "package ci\n\nfunc Sum(a int) int { return a }\n")
	writeCLIFile(t, dir, "b.go", "package ci\n\nfunc One() int { return Sum(1) }\n\nfunc Two() int { return Sum(2) }\n")
	if baseYAML != "" {
		writeCLIFile(t, dir, "prism.yaml", baseYAML)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "base")
	if rc := cmdIndex([]string{dir, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	// The change: Sum gains a parameter; One is updated, Two is missed.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package ci\n\nfunc Sum(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package ci\n\nfunc One() int { return Sum(1, 0) }\n\nfunc Two() int { return Sum(2) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// MKT-01: prism ci fails a change that misses a caller, annotates it, and
// takes its settings from the base revision only.
func TestCIGateFailsOnMissedCaller(t *testing.T) {
	dir := ciFixture(t, "")
	var rc int
	out := captureStdout(func() { _ = captureStderr(func() { rc = cmdCI([]string{dir, "--format", "github"}) }) })
	if rc != exitFinding || !strings.Contains(out, "::error file=b.go,line=5") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	sarif := captureStdout(func() { _ = captureStderr(func() { _ = cmdCI([]string{dir, "--format", "sarif"}) }) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(sarif), &doc); err != nil || doc["version"] != "2.1.0" {
		t.Fatalf("sarif: %v\n%s", err, sarif)
	}
	js := captureStdout(func() { _ = captureStderr(func() { _ = cmdCI([]string{dir, "--format", "json"}) }) })
	var rep ciReport
	if err := json.Unmarshal([]byte(js), &rep); err != nil || rep.SchemaVersion != 1 || rep.Verdict != "fail" || len(rep.Findings) != 1 {
		t.Fatalf("json: %v %+v", err, rep)
	}
}

func TestCIGateSettingsComeFromBase(t *testing.T) {
	// Base says only arch fails the gate: the missed site is a warning.
	dir := ciFixture(t, "ci_fail_on: arch\n")
	var rc int
	out := captureStdout(func() { _ = captureStderr(func() { rc = cmdCI([]string{dir, "--format", "github"}) }) })
	if rc != exitOK || !strings.Contains(out, "::warning file=b.go,line=5") {
		t.Fatalf("base ci_fail_on: arch: rc=%d\n%s", rc, out)
	}
	// A change that tries to switch its own gate off has no effect.
	dir = ciFixture(t, "")
	writeCLIFile(t, dir, "prism.yaml", "ci_fail_on: []\n")
	_ = captureStdout(func() { _ = captureStderr(func() { rc = cmdCI([]string{dir}) }) })
	if rc != exitFinding {
		t.Fatalf("working-tree prism.yaml must not change the gate: rc=%d", rc)
	}
}
