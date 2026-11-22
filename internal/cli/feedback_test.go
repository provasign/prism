package cli

import (
	"strings"
	"testing"
)

// BUG-12: CLI feedback is persisted and listable; CLI drift refuses
// instead of reporting an empty success.
func TestFeedbackPersistsAndLists(t *testing.T) {
	dir := setupCLIProject(t)
	for _, notes := range []string{"first", "second"} {
		var rc int
		_ = captureStdout(func() { rc = cmdFeedback([]string{"--tool", "prism_search", "--rating", "4", "--notes", notes, dir}) })
		if rc != 0 {
			t.Fatalf("feedback rc=%d", rc)
		}
	}
	var rc int
	out := captureStdout(func() { rc = cmdFeedback([]string{"--list", dir}) })
	if rc != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, `"notes":"second"`) {
		t.Fatalf("list rc=%d:\n%s", rc, out)
	}
}

func TestDriftCLIRefuses(t *testing.T) {
	var rc int
	stderr := captureStderr(func() { rc = cmdDrift(nil) })
	if rc != 2 || !strings.Contains(stderr, "MCP only") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}
