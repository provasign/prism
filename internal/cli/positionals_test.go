package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPositionalsFirstSkipsFlagValues(t *testing.T) {
	got, rc := positionalsFirst("x", "a name", "u", 1, []string{"--format", "text", "Foo.bar", "--file", "a.go", "./dir"}, "--format", "--file")
	want := []string{"Foo.bar", "./dir", "--format", "text", "--file", "a.go"}
	if rc != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v rc=%d, want %v", got, rc, want)
	}
}

// BUG-17: a flag value was taken as the directory and an index was
// created there.
func TestMissingSymbolNamesItNotADirectory(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	cases := map[string]func([]string) int{
		"change-impact": cmdChangeImpact, "missing-implementations": cmdMissingImplementations,
		"references": cmdReferences, "resolve": cmdResolve, "node": cmdNode, "edges": cmdEdges,
	}
	for name, fn := range cases {
		var rc int
		stderr := captureStderr(func() { rc = fn([]string{"--format", "text"}) })
		if rc != 2 || !strings.Contains(stderr, name+" needs ") || strings.Contains(stderr, "directory") {
			t.Errorf("%s --format text: rc=%d stderr=%q", name, rc, stderr)
		}
	}
	var rc int
	stderr := captureStderr(func() { rc = cmdRenamePlan([]string{"--format", "text", "Foo.bar"}) })
	if rc != 2 || !strings.Contains(stderr, "rename-plan needs a symbol and a new name") {
		t.Errorf("rename-plan: rc=%d stderr=%q", rc, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "text")); err == nil {
		t.Fatal("a directory named after the flag value was created")
	}
}

// BUG-18: a term that starts with "-" is searchable after "--".
func TestSearchDoubleDashEndsFlags(t *testing.T) {
	dir := setupCLIProject(t)
	_ = writeCLIFile(t, dir, "notes.txt", "pass --scope text here\n")
	var rc int
	out := captureStdout(func() {
		stderr := captureStderr(func() { rc = cmdSearch([]string{"--dir", dir, "--", "--scope"}) })
		if strings.Contains(stderr, "unknown flag") {
			t.Errorf("stderr = %q", stderr)
		}
	})
	if rc != 0 || !strings.Contains(out, "notes.txt") {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
}

// BUG-09: more than 10 terms is a usage error, never a quiet truncation.
func TestSearchOverTenTermsIsUsageError(t *testing.T) {
	args := []string{"--dir", t.TempDir()}
	for i := 0; i < 11; i++ {
		args = append(args, "t"+string(rune('a'+i)))
	}
	var rc int
	stderr := captureStderr(func() { rc = cmdSearch(args) })
	if rc != 2 || !strings.Contains(stderr, "search takes up to 10 terms; got 11") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr)
	}
}
