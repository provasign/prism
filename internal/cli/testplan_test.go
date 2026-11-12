package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestTestPlanCLI(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, dir, "go.mod", "module example.com/plancli\n\ngo 1.26\n")
	writeCLIFile(t, dir, "core.go", "package plancli\nfunc Value() int {return 1}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=t@t", "commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	writeCLIFile(t, dir, "new_test.go", "package plancli\nimport \"testing\"\nfunc TestNew(t *testing.T){Value()}\n")
	var rc int
	text := captureStdout(func() { rc = Run([]string{"test-plan", dir, "--format", "text"}) })
	if rc != 0 || !strings.Contains(text, "TestNew") || !strings.Contains(text, "boundary:") {
		t.Fatalf("text rc=%d: %s", rc, text)
	}
	data := captureStdout(func() { rc = Run([]string{"test-plan", dir, "--base", "HEAD", "--format", "json"}) })
	var result map[string]any
	if rc != 0 || json.Unmarshal([]byte(data), &result) != nil || result["status"] != "candidates found" {
		t.Fatalf("JSON rc=%d: %s", rc, data)
	}
	if !strings.Contains(commandHelp("test-plan"), "--base") {
		t.Fatal("missing command help")
	}
}

func TestTestPlanInventoryIsNotTruncated(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	text := strings.Repeat("candidate\n", 10000) + "gap: final gap\n"
	if fitShellOutput("test-plan", text) != text {
		t.Fatal("inventory truncated")
	}
}

// test-plan is a command for people; nothing written into a repository's
// agent steering may point agents at it.
func TestTestPlanIsNotInAgentSteering(t *testing.T) {
	for name, text := range map[string]string{
		"CLI":          cliSteeringInstructionsForOS("darwin"),
		"MCP fallback": steeringBlock(),
	} {
		if strings.Contains(text, "test-plan") || strings.Contains(text, "test_plan") {
			t.Errorf("%s steering mentions test-plan", name)
		}
	}
}
