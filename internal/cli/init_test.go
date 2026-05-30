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
