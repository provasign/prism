package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDir_NoFile(t *testing.T) {
	c, err := LoadFromDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.GroveURL == "" {
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
	if c.GroveURL != "http://custom:1234" {
		t.Errorf("url %q", c.GroveURL)
	}
	if c.GroveBinary != "/usr/bin/grove" {
		t.Errorf("bin %q", c.GroveBinary)
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
