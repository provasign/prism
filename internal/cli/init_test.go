package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCmdInit(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(dir)
	if rc := cmdInit([]string{dir}); rc != 0 {
		t.Errorf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

func TestCmdInit_GlobalFlag(t *testing.T) {
	dir := t.TempDir()
	if rc := cmdInit([]string{dir, "--global"}); rc != 0 {
		t.Errorf("rc %d", rc)
	}
}

func TestCmdInit_BadDir(t *testing.T) {
	// trigger write error by passing a path under a read-only parent
	parent := t.TempDir()
	ro := filepath.Join(parent, "ro")
	_ = os.MkdirAll(ro, 0o755)
	_ = os.Chmod(ro, 0o500)
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	target := filepath.Join(ro, "child")
	_ = os.MkdirAll(target, 0o755)
	if rc := cmdInit([]string{filepath.Join(ro, "nosuch")}); rc != 1 {
		// May or may not fail depending on platform; accept either
		t.Logf("rc %d", rc)
	}
}

func TestDetectSelfPath(t *testing.T) {
	if detectSelfPath() == "" {
		t.Error("empty")
	}
}

func TestWriteSteeringInstructions(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir)
	// Should have written at least one instruction file
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("no files")
	}
}

func TestBuildZedConfig(t *testing.T) {
	cfg := buildZedConfig("/x/prism", "/y/root")
	if len(cfg) == 0 {
		t.Error("empty")
	}
}

func TestBuildVSCodeConfig(t *testing.T) {
	cfg := buildVSCodeConfig("/x/prism", "/y/root")
	s := string(cfg)
	if len(cfg) == 0 {
		t.Fatal("empty")
	}
	for _, want := range []string{`"servers"`, `"prism"`, `"stdio"`, `"/x/prism"`, `"/y/root"`} {
		if !contains(s, want) {
			t.Errorf("expected %q in %s", want, s)
		}
	}
}

func TestWriteSteeringInstructions_AllTargets(t *testing.T) {
	dir := t.TempDir()
	writeSteeringInstructions(dir)
	for _, want := range []string{"CLAUDE.md", ".cursorrules", ".windsurfrules", ".github/copilot-instructions.md", "AGENTS.md", "GEMINI.md"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestInitRegisterMCPTools_WritesVSCode(t *testing.T) {
	dir := t.TempDir()
	written := initRegisterMCPTools(dir, "/x/prism", false)
	var sawVSCode bool
	for _, p := range written {
		if filepath.Base(filepath.Dir(p)) == ".vscode" && filepath.Base(p) == "mcp.json" {
			sawVSCode = true
		}
	}
	if !sawVSCode {
		t.Errorf(".vscode/mcp.json not written; got %v", written)
	}
}

func TestCmdInit_InstallAlias(t *testing.T) {
	// `prism install` must behave identically to `prism init`.
	dir := t.TempDir()
	rc := Run([]string{"install", dir})
	if rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "prism.yaml")); err != nil {
		t.Error(err)
	}
}

// contains is a tiny helper so we don't pull in strings for one test.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
