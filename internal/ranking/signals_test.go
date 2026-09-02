package ranking

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestNewSignalComputer(t *testing.T) {
	c := NewSignalComputer("/tmp")
	if c.WorkspaceRoot != "/tmp" {
		t.Error("root")
	}
	if c.gitCache == nil {
		t.Error("cache")
	}
}

func TestCompute(t *testing.T) {
	c := NewSignalComputer("")
	v := c.Compute(context.Background(), grove.SymbolRecord{FilePath: "x.go"}, 2, true, false)
	if v.GraphDistance == 0 {
		t.Error("graph dist")
	}
	if v.TestRelevance != 1.0 {
		t.Error("test rel")
	}
	v = c.Compute(context.Background(), grove.SymbolRecord{}, 0, false, true)
	if v.TestRelevance != 0.5 {
		t.Error("test rel sameFile")
	}
	// unreachable
	v = c.Compute(context.Background(), grove.SymbolRecord{}, math.MaxInt, false, false)
	if v.GraphDistance != 0 {
		t.Error("unreachable")
	}
	if v.TestRelevance != 0 {
		t.Error("test rel none")
	}
}

func TestGitStats_NoRepo(t *testing.T) {
	c := NewSignalComputer(t.TempDir())
	s := c.gitStats("x.go")
	if s.LastEditDays != 365 {
		t.Errorf("got %d", s.LastEditDays)
	}
	// cached
	_ = c.gitStats("x.go")
}

func TestGitStats_EmptyRoot(t *testing.T) {
	c := NewSignalComputer("")
	s := c.gitStats("x.go")
	if s.LastEditDays != 365 {
		t.Errorf("got %d", s.LastEditDays)
	}
}

func TestGitStats_RealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		_ = cmd.Run()
	}
	_ = os.WriteFile(filepath.Join(dir, "f.go"), []byte("package x"), 0o644)
	for _, args := range [][]string{
		{"add", "."}, {"commit", "-m", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		_ = cmd.Run()
	}
	c := NewSignalComputer(dir)
	s := c.gitStats("f.go")
	if s.CommitCount90d < 1 {
		t.Errorf("commits %d", s.CommitCount90d)
	}
}

func TestRunGit_Error(t *testing.T) {
	if _, err := runGit("/nonexistent", "log"); err == nil {
		t.Error("expected err")
	}
}

func TestClamp01(t *testing.T) {
	if clamp01(-1) != 0 || clamp01(2) != 1 || clamp01(0.5) != 0.5 {
		t.Error("clamp01")
	}
}

func TestSelectProfile_Unknown(t *testing.T) {
	p := SelectProfile("nonexistent-xyz")
	if p.Name == "" {
		t.Error("should return default")
	}
}
