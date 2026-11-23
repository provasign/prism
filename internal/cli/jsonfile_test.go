package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// CON-07: a settings file with a foreign hook holding && and keys in the
// user's order comes back byte-identical outside the key Prism adds.
func TestEncodeJSONFilePreservesForeignBytes(t *testing.T) {
	original := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "shale capture claude > /dev/null 2>&1 && echo <ok>"
          }
        ]
      }
    ]
  },
  "zeta": 1,
  "alpha": [
    "b",
    "a"
  ]
}`
	var doc map[string]any
	if err := json.Unmarshal([]byte(original), &doc); err != nil {
		t.Fatal(err)
	}
	doc["enabledMcpjsonServers"] = []any{"prism"}
	out, err := encodeJSONFile(doc, []byte(original))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSuffix(original, "\n}") + `,
  "enabledMcpjsonServers": [
    "prism"
  ]
}`
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestEncodeJSONFileNewFileAndTypedValues(t *testing.T) {
	out, err := encodeJSONFile(map[string]any{"b": 1, "a": mcpEntry{Command: "prism", Args: []string{"mcp"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "{\n  \"a\": {\n    \"command\": \"prism\"") {
		t.Fatalf("layout:\n%s", out)
	}
}

// CON-07 end to end: init keeps another tool's hook byte-for-byte, and
// leaves an unparseable settings file alone and exits 1.
func TestInitLeavesForeignSettingsBytes(t *testing.T) {
	dir := t.TempDir()
	foreign := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "command -v shale >/dev/null 2>&1 && shale capture claude || true"
          }
        ]
      }
    ]
  }
}`
	mustWriteFile(t, dir+"/.claude/settings.json", foreign+"\n")
	captureStdout(func() {
		if rc := cmdInit([]string{"--harness", "claude", "--interface", "mcp", dir}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	raw, _ := os.ReadFile(dir + "/.claude/settings.json")
	if !strings.HasPrefix(string(raw), strings.TrimSuffix(foreign, "\n  }\n}")) {
		t.Fatalf("foreign hook bytes changed:\n%s", raw)
	}

	bad := t.TempDir()
	mustWriteFile(t, bad+"/.claude/settings.json", "{ // a comment\n}\n")
	var rc int
	captureStdout(func() { rc = cmdInit([]string{"--harness", "claude", "--interface", "mcp", bad}) })
	if got, _ := os.ReadFile(bad + "/.claude/settings.json"); string(got) != "{ // a comment\n}\n" {
		t.Fatalf("unparseable settings overwritten:\n%s", got)
	}
	if rc != exitFinding {
		t.Fatalf("init rc=%d over an unwritable settings file, want %d", rc, exitFinding)
	}
}
