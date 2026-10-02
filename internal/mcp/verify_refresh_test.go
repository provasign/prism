package mcp

import "testing"

// urfave/cli forced run: a symlinked doc the indexer skipped turned verify
// into "review" for an unrelated one-file edit.
func TestRefreshErrorsForKeepsOnlyChangedFiles(t *testing.T) {
	changed := map[string][]lineRange{"command_run.go": nil}
	errs := []string{
		"docs/CODE_OF_CONDUCT.md: not a regular file: /tmp/x/docs/CODE_OF_CONDUCT.md",
		"command_run.go: parse error",
		"walk aborted",
	}
	got := refreshErrorsFor(errs, changed)
	if len(got) != 2 || got[0] != errs[1] || got[1] != errs[2] {
		t.Fatalf("want the changed-file error and the pathless error, got %v", got)
	}
}
