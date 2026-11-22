package mcp

import (
	"os/exec"
	"strings"
	"testing"
)

// BUG-16: a non-git directory and an unknown --base name their cause
// instead of "exit status 129/128".
func TestGitDiffErrorNamesCause(t *testing.T) {
	plain := t.TempDir()
	_, err := gitChangedRanges(plain, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("non-git dir: %v", err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_, err = gitChangedRanges(repo, "no-such-ref")
	if err == nil || !strings.Contains(err.Error(), `unknown revision "no-such-ref"`) {
		t.Fatalf("bad ref: %v", err)
	}
	if _, err := gitChangedRanges(repo, "HEAD"); err != nil {
		t.Fatalf("good ref: %v", err)
	}
}
