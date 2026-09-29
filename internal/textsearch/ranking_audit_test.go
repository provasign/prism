package textsearch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSearchDefinitionSurvivesCapAndFreshUnrelatedCommit(t *testing.T) {
	root := t.TempDir()
	uses := "package p\nfunc Use() {\n" + strings.Repeat("\tNeedle()\n", 12) + "}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(uses), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "z.go"), []byte("package p\nfunc Needle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	search := func() Result {
		return Search(context.Background(), root, "Needle", Options{MaxHits: 3, MaxPerFile: 20})
	}
	before := search()
	if len(before.Hits) != 3 || before.Hits[0].File != "z.go" {
		t.Fatalf("definition must lead the capped sample: %+v", before.Hits)
	}

	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.name", "Ranking Test")
	git("config", "user.email", "ranking@example.test")
	git("add", ".")
	git("commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("unrelated change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-qm", "fresh unrelated edit")
	after := search()
	if !reflect.DeepEqual(before.Hits, after.Hits) {
		t.Fatalf("fresh unrelated commit changed search ranking: before=%+v after=%+v", before.Hits, after.Hits)
	}
}
