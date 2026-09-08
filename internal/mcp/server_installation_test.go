package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindPrismInstallationConflicts(t *testing.T) {
	root := t.TempDir()
	runningDir := filepath.Join(root, "running")
	olderDir := filepath.Join(root, "older")
	sameDir := filepath.Join(root, "same")
	for _, dir := range []string{runningDir, olderDir, sameDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	name := "prism"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	running := filepath.Join(runningDir, name)
	older := filepath.Join(olderDir, name)
	same := filepath.Join(sameDir, name)
	for _, path := range []string{running, older, same} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(path string) (string, error) {
		switch filepath.Base(filepath.Dir(path)) {
		case "older":
			return "prism v0.69.2", nil
		case "same", "running":
			return "prism v0.72.4", nil
		default:
			return "", nil
		}
	}

	got := findPrismInstallationConflicts(
		running, "v0.72.4", olderDir+string(os.PathListSeparator)+sameDir,
		"", runtime.GOOS, probe,
	)
	if len(got) != 1 || got[0].path != older || got[0].version != "v0.69.2" {
		t.Fatalf("unexpected conflicts: %#v", got)
	}
}

func TestNormalizePrismVersion(t *testing.T) {
	for input, want := range map[string]string{
		"prism v0.72.4\n": "0.72.4",
		"0.72.4":          "0.72.4",
		"dev":             "",
		"":                "",
	} {
		if got := normalizePrismVersion(input); got != want {
			t.Errorf("normalizePrismVersion(%q) = %q, want %q", input, got, want)
		}
	}
}
