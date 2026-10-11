package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSedFlavor(t *testing.T, bsd bool) {
	t.Helper()
	orig := systemSedIsBSD
	systemSedIsBSD = func() bool { return bsd }
	t.Cleanup(func() { systemSedIsBSD = orig })
}

func TestSteeringBlock_BSDSedNoteOnlyOnBSD(t *testing.T) {
	withSedFlavor(t, true)
	got := steeringBlock()
	for _, want := range []string{"BSD (macOS) sed", "`sed -i ''`", `\b \w \s`, "[[:<:]] and [[:>:]]"} {
		if !strings.Contains(got, want) {
			t.Errorf("BSD steering block missing %q", want)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "<!-- prism:end -->") {
		t.Errorf("note must sit inside the section, before the end marker:\n%s", got)
	}

	withSedFlavor(t, false)
	if got := steeringBlock(); strings.Contains(got, "BSD") || got != steeringInstructions {
		t.Errorf("GNU machine must get the unchanged block")
	}
}

func TestSteeringBlock_BSDSedNoteSurvivesReinit(t *testing.T) {
	withSedFlavor(t, true)
	dir := t.TempDir()
	writeSteeringInstructions(dir, []string{"claude"}, false)
	writeSteeringInstructions(dir, []string{"claude"}, false)
	raw := readClaudeMD(t, dir)
	if n := strings.Count(raw, "BSD (macOS) sed"); n != 1 {
		t.Fatalf("want the note exactly once after re-init, got %d:\n%s", n, raw)
	}
	// Moving to a GNU-sed machine and re-running init drops it.
	withSedFlavor(t, false)
	writeSteeringInstructions(dir, []string{"claude"}, false)
	if strings.Contains(readClaudeMD(t, dir), "BSD") {
		t.Fatal("re-init on a GNU machine must remove the note")
	}
}

func readClaudeMD(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
