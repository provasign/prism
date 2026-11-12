package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceFixture lays out a parent folder holding three checkouts: app
// (links lib), lib, and other (linked by nobody).
func workspaceFixture(t *testing.T) (parent, app string) {
	t.Helper()
	parent, _ = filepath.EvalSymlinks(t.TempDir())
	app = filepath.Join(parent, "app")
	for _, repo := range []string{"app", "lib", "other"} {
		if err := os.MkdirAll(filepath.Join(parent, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCLIFile(t, parent, "app/prism.yaml", "version: 2\nworkspace_repo: lib=../lib\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nfunc Main() {}\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\n// LibMarker lives in the linked repository\nfunc Lib() {}\n")
	writeCLIFile(t, parent, "other/secret.go", "package other\n\n// OtherMarker is not linked\n")
	return parent, app
}

func TestWorkspaceReadLinkedRepository(t *testing.T) {
	parent, app := workspaceFixture(t)
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(app)

	out := captureStdout(func() {
		if rc := cmdRead([]string{"../lib/lib.go"}); rc != 0 {
			t.Fatalf("read of a linked repository rc=%d", rc)
		}
	})
	if !strings.Contains(out, "LibMarker") || !strings.Contains(out, "../lib/lib.go") {
		t.Fatalf("linked file not delivered under its ../ path:\n%s", out)
	}

	// A sibling that is not linked stays out of reach.
	out = captureStdout(func() {
		if rc := cmdRead([]string{"../other/secret.go"}); rc == 0 {
			t.Fatal("read of an unlinked repository succeeded")
		}
	})
	if strings.Contains(out, "OtherMarker") {
		t.Fatalf("unlinked file content leaked:\n%s", out)
	}

	// A symlink inside the linked repository cannot reach outside it.
	if err := os.Symlink(filepath.Join(parent, "other", "secret.go"), filepath.Join(parent, "lib", "leak.go")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	out = captureStdout(func() {
		if rc := cmdRead([]string{"../lib/leak.go"}); rc == 0 {
			t.Fatal("symlink out of the linked repository was followed")
		}
	})
	if strings.Contains(out, "OtherMarker") {
		t.Fatalf("symlink escape leaked content:\n%s", out)
	}
}

// A prism.yaml in a plain parent folder used to capture every checkout below
// it that had none of its own, answering from one index over all of them.
func TestCLIRootStopsAtGitBoundaryBelowParentConfig(t *testing.T) {
	parent, _ := workspaceFixture(t)
	writeCLIFile(t, parent, "prism.yaml", "version: 2\n")
	writeCLIFile(t, parent, "lib/pkg/x.go", "package pkg\n")
	t.Chdir(filepath.Join(parent, "lib", "pkg"))
	if got, sub := cliRoot(""); got != filepath.Join(parent, "lib") || sub != "pkg" {
		t.Fatalf("cliRoot = %q, %q; want the lib checkout, pkg", got, sub)
	}

	// A prism.yaml that is itself inside a git work tree keeps capturing
	// nested repositories (submodules, vendored clones), as before.
	outer, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outer, "vendor", "dep", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, outer, "prism.yaml", "version: 2\n")
	t.Chdir(filepath.Join(outer, "vendor", "dep"))
	if got, _ := cliRoot(""); got != outer {
		t.Fatalf("cliRoot = %q; want the outer repository %q", got, outer)
	}
}

func TestWorkspaceParentFileLinksSiblings(t *testing.T) {
	parent, _ := workspaceFixture(t)
	writeCLIFile(t, parent, "prism.yaml", "version: 2\nworkspace_repo: lib=lib\nworkspace_repo: other=other\n")
	t.Chdir(filepath.Join(parent, "lib"))
	out := captureStdout(func() {
		if rc := cmdWorkspace(nil); rc != 0 {
			t.Fatalf("workspace rc=%d", rc)
		}
	})
	if !strings.Contains(out, "../other") || strings.Contains(out, "../lib") {
		t.Fatalf("lib should see other, and not itself, through the parent file:\n%s", out)
	}
}

func TestWorkspaceAddRemove(t *testing.T) {
	parent, app := workspaceFixture(t)
	t.Chdir(app)
	if rc := workspaceAdd([]string{"../other", "--id", "oth"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	data, _ := os.ReadFile(filepath.Join(app, "prism.yaml"))
	if !strings.Contains(string(data), "workspace_repo: oth=../other\n") {
		t.Fatalf("link not written:\n%s", data)
	}
	if rc := workspaceAdd([]string{"../other"}); rc == 0 {
		t.Fatal("linking the same checkout twice succeeded")
	}
	if rc := workspaceAdd([]string{"."}); rc == 0 {
		t.Fatal("linking the repository to itself succeeded")
	}
	out := captureStdout(func() { cmdWorkspace(nil) })
	for _, want := range []string{"lib", "../lib", "oth", "../other", "not indexed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	if rc := workspaceRemove([]string{"oth"}); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	data, _ = os.ReadFile(filepath.Join(app, "prism.yaml"))
	if strings.Contains(string(data), "oth=") || !strings.Contains(string(data), "lib=../lib") {
		t.Fatalf("remove changed the wrong lines:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(parent, "other", "secret.go")); err != nil {
		t.Fatal("remove touched the checkout")
	}
}

func TestWorkspaceLookupFallsBackToLinkedRepository(t *testing.T) {
	parent, app := workspaceFixture(t)
	writeCLIFile(t, parent, "lib/box.go", "package lib\n\ntype Box struct{}\n\n// Unwrap is only defined in lib.\nfunc (b *Box) Unwrap() int { return 42 }\n")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(app)

	// lib is linked but has no index yet: the miss must say it was not searched.
	out := captureStdout(func() { cmdLookup([]string{"Box.Unwrap"}) })
	if !strings.Contains(out, "not searched") || !strings.Contains(out, "lib (../lib)") {
		t.Fatalf("miss does not name the unindexed linked repository:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(parent, "lib", ".grove")); err == nil {
		t.Fatal("a lookup from app created an index in lib")
	}

	if rc := cmdIndex([]string{filepath.Join(parent, "lib")}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	out = captureStdout(func() {
		if rc := cmdLookup([]string{"Box.Unwrap"}); rc != 0 {
			t.Fatalf("lookup rc=%d", rc)
		}
	})
	for _, want := range []string{"../lib/box.go", "return 42", "linked repository lib"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lookup output missing %q:\n%s", want, out)
		}
	}

	// A name this repository defines is answered here, with no linked note.
	out = captureStdout(func() { cmdLookup([]string{"Main"}) })
	if !strings.Contains(out, "main.go") || strings.Contains(out, "linked repository") {
		t.Fatalf("local symbol not answered locally:\n%s", out)
	}
}

func TestWorkspaceSearchLinkedRepositories(t *testing.T) {
	parent, app := workspaceFixture(t)
	writeCLIFile(t, parent, "app/use.go", "package main\n\n// calls LibMarker helpers\n")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(app)

	out := captureStdout(func() {
		if rc := cmdSearch([]string{"LibMarker", "--scope", "text"}); rc != 0 {
			t.Fatalf("search rc=%d", rc)
		}
	})
	if !strings.Contains(out, "use.go") || strings.Contains(out, "../lib/lib.go") {
		t.Fatalf("default search must list this repository only:\n%s", out)
	}
	if !strings.Contains(out, "lib 1 in 1 file(s)") {
		t.Fatalf("default search does not count matches in the linked repository:\n%s", out)
	}

	out = captureStdout(func() {
		if rc := cmdSearch([]string{"LibMarker", "--repo", "lib"}); rc != 0 {
			t.Fatalf("search --repo rc=%d", rc)
		}
	})
	if !strings.Contains(out, "../lib/lib.go:3:") || strings.Contains(out, "use.go") {
		t.Fatalf("--repo lib must list only lib, under ../lib paths:\n%s", out)
	}
	if strings.Contains(out, "OtherMarker") {
		t.Fatalf("unlinked repository searched:\n%s", out)
	}
	if rc := cmdSearch([]string{"LibMarker", "--repo", "other"}); rc == 0 {
		t.Fatal("--repo accepted a repository that is not linked")
	}
}

func TestWorkspaceDiscoverFromManifests(t *testing.T) {
	parent, app := workspaceFixture(t)
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\nrequire example.com/lib v1.2.0\n")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n")
	writeCLIFile(t, parent, "other/go.mod", "module example.com/other\n\nrequire example.com/app v0.9.0\n")
	writeCLIFile(t, parent, "unrelated/go.mod", "module example.com/unrelated\n")
	t.Chdir(app)
	out := captureStdout(func() {
		if rc := workspaceDiscover(nil); rc != 0 {
			t.Fatalf("discover rc=%d", rc)
		}
	})
	for _, want := range []string{"../lib", "depends on", "require example.com/lib v1.2.0", "../other", "used by", "other/go.mod requires example.com/app v0.9.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("discover output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unrelated") {
		t.Fatalf("a sibling with no shared package was reported:\n%s", out)
	}
	before, _ := os.ReadFile(filepath.Join(app, "prism.yaml"))
	if strings.Contains(string(before), "other") {
		t.Fatal("discover linked a repository without --link")
	}
	if rc := workspaceDiscover([]string{"--link"}); rc != 0 {
		t.Fatalf("discover --link rc=%d", rc)
	}
	after, _ := os.ReadFile(filepath.Join(app, "prism.yaml"))
	if !strings.Contains(string(after), "workspace_repo: other=../other") || strings.Count(string(after), "lib=") != 1 {
		t.Fatalf("--link should add other and leave lib linked once:\n%s", after)
	}
}

// The headline case: change a library function, see its callers in the
// application repository that has its own index.
func TestWorkspaceChangeImpactListsConsumersInLinkedRepository(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app, idle := filepath.Join(parent, "lib"), filepath.Join(parent, "app"), filepath.Join(parent, "idle")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n\nfunc inside() int { return Helper() }\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\nworkspace_repo: idle=../idle\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper())\n}\n")
	writeCLIFile(t, parent, "idle/go.mod", "module example.com/idle\n\ngo 1.22\n")
	writeCLIFile(t, parent, "idle/idle.go", "package idle\n\nfunc Idle() {}\n")
	// No fake .git directories here: `go list` stamps VCS state for a main
	// package and fails on a .git that is not a real repository.
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() {
		if rc := cmdChangeImpact([]string{"Helper", "--format", "text"}); rc != 0 {
			t.Fatalf("change-impact rc=%d", rc)
		}
	})
	for _, want := range []string{
		"consumers in linked repositories",
		"// repo app  ../app  requires example.com/lib v0.3.0",
		"../app/main.go:6:14: println(lib.Helper())  [in main]",
		"// repo idle  ../idle  NOT SEARCHED: no index there",
		"1 call site(s) in 1 of 2 linked repositories searched",
		"not searched (no index): idle",
		"repositories that are not linked are not covered",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("change-impact output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(idle, ".grove")); err == nil {
		t.Fatal("change-impact from lib created an index in a linked repository")
	}
}
