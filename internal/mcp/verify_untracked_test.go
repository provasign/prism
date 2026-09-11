package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitChangedRangesIncludesUntrackedNonIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.go", "package fixture\n")
	write(".gitignore", "ignored.txt\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@test", "commit", "-qm", "base"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	write("new/nested.go", "package new\n\nfunc Added() {}")
	write("ignored.txt", "not part of the worktree diff")
	ranges, err := gitChangedRanges(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := ranges["new/nested.go"]
	if len(got) != 1 || got[0] != (lineRange{start: 1, end: 3}) {
		t.Fatalf("untracked file range = %+v, want [{1 3}]", got)
	}
	if _, included := ranges["ignored.txt"]; included {
		t.Fatal("ignored untracked file must not participate in verification")
	}
}
