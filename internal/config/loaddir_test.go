package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromDir_NoFile(t *testing.T) {
	c, err := LoadFromDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != "default" || c.MaxCacheFiles == 0 {
		t.Error("default not applied")
	}
}

func TestLoadFromDir_FullYAML(t *testing.T) {
	dir := t.TempDir()
	yml := `# comment
grove_url: http://custom:1234
grove_binary: "/usr/bin/grove"
model: 'claude-opus-4-1'
profile: thorough
unknown_key: ignored
malformedline
`
	if err := os.WriteFile(filepath.Join(dir, "prism.yaml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// CON-08: retired keys load silently; an unknown key and a malformed
	// line are reported, the unknown one with the nearest known key.
	if len(c.Problems) != 2 {
		t.Fatalf("problems: %q", c.Problems)
	}
	if c.Model != "claude-opus-4-1" {
		t.Errorf("model %q", c.Model)
	}
	if c.Profile != "thorough" {
		t.Errorf("profile %q", c.Profile)
	}
}

func TestLoadFromDir_ReadError(t *testing.T) {
	dir := t.TempDir()
	// Create as directory not file to force read error
	if err := os.MkdirAll(filepath.Join(dir, "prism.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromDir(dir); err == nil {
		t.Error("expected read error")
	}
}

// CON-08: init's keys come from the one parser, with sources; a typo names
// the key it was probably meant to be; invalid values are reported.
func TestParseInitKeysSourcesAndProblems(t *testing.T) {
	c := Parse([]byte("version: 2\nharnesses: \"claude,codex\"\nmcp_surface: \"compact\"\ninterface: cli\nport: 9000\narch_dney: a -> b\nci_comment: maybe\nci_fail_on: arch, nope\n"))
	if c.Version != "2" || c.Harnesses != "claude,codex" || c.MCPSurface != "compact" || c.Interface != "cli" || c.Port != 9000 {
		t.Fatalf("init keys: %+v", c)
	}
	if c.Sources["harnesses"] != "prism.yaml:2" || c.Sources["port"] != "prism.yaml:5" {
		t.Fatalf("sources: %v", c.Sources)
	}
	joined := strings.Join(c.Problems, "\n")
	for _, want := range []string{`unknown key "arch_dney"`, `did you mean "arch_deny"?`, `ci_comment "maybe"`, `unknown kind "nope"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems lack %q:\n%s", want, joined)
		}
	}
	for _, kv := range c.Keys() {
		if kv[0] == "model" && kv[2] != "default" {
			t.Errorf("model source %q, want default", kv[2])
		}
	}
}
