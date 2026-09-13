package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCmdVerifyStrictMatchesMCPGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.py")
	if err := os.WriteFile(path, []byte("class API:\n    def run(self, x): return x\ndef use(a: API):\n    return a.run(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@test", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(path, []byte("class API:\n    def run(self, x, y): return x + y\ndef use(a: API):\n    return a.run(1, 2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		strict bool
		want   int
	}{
		{false, 0},
		{true, 1},
	} {
		args := []string{dir, "--format", "json"}
		if tc.strict {
			args = append(args, "--strict")
		}
		var code int
		output := captureStdout(func() { code = cmdVerify(args) })
		if code != tc.want {
			t.Fatalf("cmdVerify(%v) exit = %d, want %d; output: %s", args, code, tc.want, output)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("cmdVerify(%v) output: %v: %s", args, err, output)
		}
		if result["verdict"] != "review" || result["gateFailure"] != tc.strict {
			t.Fatalf("cmdVerify(%v) result = %v", args, result)
		}
	}
}
