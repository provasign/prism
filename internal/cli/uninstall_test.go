package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MKT-05: --dry-run writes nothing; uninstall removes exactly what init
// added, keeping foreign bytes and anything a user added later.
func TestInitDryRunAndUninstallRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settings := "{\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"shale capture && true\"\n          }\n        ]\n      }\n    ]\n  }\n}\n"
	mustWriteFile(t, filepath.Join(dir, ".claude", "settings.json"), settings)
	mustWriteFile(t, filepath.Join(dir, "CLAUDE.md"), "# Mine\n")

	out := captureStdout(func() {
		if rc := cmdInit([]string{"--dry-run", "--harness", "claude", "--interface", "mcp", dir}); rc != 0 {
			t.Fatalf("dry-run rc=%d", rc)
		}
	})
	if !strings.Contains(out, "nothing was written") || !strings.Contains(out, "+++ b/.mcp.json") {
		t.Fatalf("dry-run output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		t.Fatal("dry-run wrote .mcp.json")
	}

	captureStdout(func() {
		if rc := cmdInit([]string{"--harness", "claude", "--interface", "mcp", dir}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	var mcp map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err := json.Unmarshal(raw, &mcp); err != nil {
		t.Fatal(err)
	}
	mcp["mcpServers"].(map[string]any)["mine"] = map[string]any{"command": "mytool"}
	raw, _ = json.Marshal(mcp)
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), string(raw))
	f, _ := os.OpenFile(filepath.Join(dir, "CLAUDE.md"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\nLater note.\n")
	f.Close()

	captureStdout(func() {
		if rc := cmdUninstall([]string{dir}); rc != 0 {
			t.Fatalf("uninstall rc=%d", rc)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json")); string(got) != settings {
		t.Fatalf("settings.json changed:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); strings.Contains(string(got), "Prism") || !strings.Contains(string(got), "Later note.") {
		t.Fatalf("CLAUDE.md:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".mcp.json")); strings.Contains(string(got), "prism") || !strings.Contains(string(got), "mytool") {
		t.Fatalf(".mcp.json:\n%s", got)
	}
	for _, gone := range []string{"prism.yaml", "AGENTS.md", manifestPath} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s left behind", gone)
		}
	}
}
