// Package testtool gates tests on host tools (git, rg, python3, go).
package testtool

import (
	"os"
	"os/exec"
	"testing"
)

// Require skips t when tool is not on PATH, and fails it instead under
// PRISM_CI=1: a CI job with a missing tool used to pass by skipping the
// tests that needed it (REL-07).
func Require(t testing.TB, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err == nil {
		return
	}
	if os.Getenv("PRISM_CI") == "1" {
		t.Fatalf("%s not on PATH (PRISM_CI=1 makes a missing tool a failure)", tool)
	}
	t.Skipf("%s not on PATH", tool)
}

// Want reports whether tool is on PATH, for a test that covers it only
// when present; under PRISM_CI=1 a missing tool fails the test instead.
func Want(t testing.TB, tool string) bool {
	t.Helper()
	if _, err := exec.LookPath(tool); err == nil {
		return true
	}
	if os.Getenv("PRISM_CI") == "1" {
		t.Fatalf("%s not on PATH (PRISM_CI=1 makes a missing tool a failure)", tool)
	}
	return false
}
