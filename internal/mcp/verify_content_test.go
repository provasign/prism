package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestToolVerify_StringContentAdvisoryDoesNotMaskContractChanges(t *testing.T) {
	for _, tc := range []struct {
		name, before, after, verdict string
		advisory                     bool
	}{
		{"unexported text", `const helpText = "old"`, `const helpText = "new"`, "complete", true},
		{"concatenated instructions", `const compactInstructions = "old " + "text"`, `const compactInstructions = "new " + "text"`, "complete", true},
		{"exported text", `const HelpText = "old"`, `const HelpText = "new"`, "review", false},
		{"numeric value", `const timeout = 1`, `const timeout = 2`, "review", false},
		{"renamed text", `const helpText = "old"`, `const renamedText = "old"`, "review", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\n\n"+text+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(tc.before)
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/sample\n\ngo 1.26\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir,
					"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			git("init", "-q")
			git("add", "-A")
			git("commit", "-q", "-m", "base")
			write(tc.after)
			gc := grove.NewClient("", "").WithTokenFromDir(dir)
			if err := gc.EnsureRunning(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(gc.Shutdown)
			h := NewHandler(config.Default(), dir, gc)
			out, err := h.Invoke("prism_verify", map[string]any{"strict": true})
			if err != nil {
				t.Fatal(err)
			}
			m := out.(map[string]any)
			if m["verdict"] != tc.verdict {
				t.Fatalf("verdict = %v, want %s; unverified=%v advisories=%v", m["verdict"], tc.verdict, m["unverifiedSeeds"], m["contentAdvisories"])
			}
			if m["gateFailure"] != (tc.verdict == "review") {
				t.Fatalf("strict gateFailure=%v for %s", m["gateFailure"], tc.verdict)
			}
			advisories, _ := m["contentAdvisories"].([]string)
			if (len(advisories) == 1) != tc.advisory {
				t.Fatalf("content advisories = %v, want advisory=%v", advisories, tc.advisory)
			}
			text, ok := renderVerifyAsText(m)
			if !ok || strings.Contains(text, "CONTENT ADVISORIES") != tc.advisory {
				t.Fatalf("text output omitted/misstated advisory: %s", text)
			}
			if tc.advisory && !strings.Contains(text, "content behavior above was not assessed") {
				t.Fatalf("complete verdict overstated content coverage: %s", text)
			}
		})
	}
}
