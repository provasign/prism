package cli

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/workspace"
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
		"consumers in other repositories",
		"// repo app  ../app  requires example.com/lib v0.3.0, built against the local checkout",
		"../app/main.go:6:14: println(lib.Helper())  [in main]",
		"// repo idle  ../idle  NOT SEARCHED: no index there",
		"1 site(s) in 1 of 2 linked repositories searched",
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

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The consumer repository switches to a branch that no longer calls the
// library. The library's change-impact must not answer from the old tree:
// it refreshes the consumer's index, or, when it may not, says it is stale.
func TestWorkspaceChangeImpactFollowsBranchSwitch(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/.gitignore", ".grove/\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper())\n}\n")
	gitIn(t, app, "init", "-q", "-b", "main")
	gitIn(t, app, "add", "-A")
	gitIn(t, app, "commit", "-q", "-m", "calls the library")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(lib)
	impact := func() string {
		return captureStdout(func() {
			if rc := cmdChangeImpact([]string{"Helper", "--format", "text"}); rc != 0 {
				t.Fatalf("change-impact rc=%d", rc)
			}
		})
	}
	if out := impact(); !strings.Contains(out, "../app/main.go:6:14") || strings.Contains(out, "STALE") {
		t.Fatalf("baseline: site missing or index reported stale:\n%s", out)
	}

	gitIn(t, app, "checkout", "-q", "-b", "no-lib")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nfunc main() {\n\tprintln(1)\n}\n")
	gitIn(t, app, "commit", "-q", "-am", "stop calling the library")

	// Refresh forbidden: the old site may be shown, but only under a label
	// naming both trees.
	t.Setenv("PRISM_WORKSPACE_REFRESH", "off")
	out := impact()
	if !strings.Contains(out, "STALE: indexed at main@") || !strings.Contains(out, "now at no-lib@") ||
		!strings.Contains(out, "answered from a STALE index: app") {
		t.Fatalf("stale index not labelled with both trees:\n%s", out)
	}

	// Refresh allowed (the default): the answer describes the tree on disk.
	t.Setenv("PRISM_WORKSPACE_REFRESH", "")
	out = impact()
	if strings.Contains(out, "main.go:6:14") || strings.Contains(out, "STALE") {
		t.Fatalf("answer still describes the old branch:\n%s", out)
	}
	if !strings.Contains(out, "0 site(s) in 1 of 1 linked repositories searched") {
		t.Fatalf("scope line wrong after refresh:\n%s", out)
	}

	// An uncommitted edit that restores the call is picked up too.
	writeCLIFile(t, parent, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper())\n}\n")
	if out = impact(); !strings.Contains(out, "../app/main.go:6:14") {
		t.Fatalf("uncommitted edit in the linked repository not seen:\n%s", out)
	}
}

// verify judges this repository's diff. A changed contract that a linked
// repository calls is listed, and the verdict says it was not covered.
func TestWorkspaceVerifyListsLinkedConsumersOfAChangedContract(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n")
	writeCLIFile(t, parent, "lib/.gitignore", ".grove/\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper())\n}\n")
	gitIn(t, lib, "init", "-q", "-b", "main")
	gitIn(t, lib, "add", "-A")
	gitIn(t, lib, "commit", "-q", "-m", "library")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper(n int) int { return n }\n")
	t.Chdir(lib)
	out := captureStdout(func() { cmdVerify([]string{"--format", "text"}) })
	for _, want := range []string{
		"../app/main.go:6:14: println(lib.Helper())  [in main]",
		"NOT covered by this verdict",
		"1 site(s) in 1 of 1 linked repositories searched",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the diff covers its own blast radius") {
		t.Fatalf("verify claims the diff covers its blast radius while a linked repository still calls the old contract:\n%s", out)
	}
}

func TestWorkspaceTestPlanListsLinkedTests(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n")
	writeCLIFile(t, parent, "lib/.gitignore", ".grove/\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/use.go", "package app\n\nimport \"example.com/lib\"\n\nfunc Use() int { return lib.Helper() }\n")
	writeCLIFile(t, parent, "app/use_test.go", "package app\n\nimport (\n\t\"testing\"\n\n\t\"example.com/lib\"\n)\n\nfunc TestHelper(t *testing.T) {\n\tif lib.Helper() != 1 {\n\t\tt.Fatal()\n\t}\n}\n")
	gitIn(t, lib, "init", "-q", "-b", "main")
	gitIn(t, lib, "add", "-A")
	gitIn(t, lib, "commit", "-q", "-m", "library")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 2 }\n")
	t.Chdir(lib)
	out := captureStdout(func() { cmdTestPlan([]string{"--format", "text"}) })
	for _, want := range []string{
		"Tests in linked repositories that reference a changed symbol",
		"../app/use_test.go:10:9",
		"1 non-test site(s) in linked repositories reference a changed symbol",
		"linked repositories: 2 site(s) in 1 of 1 linked repositories searched",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("test-plan output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "../app/use.go:5") {
		t.Fatalf("a non-test site was listed as a linked test:\n%s", out)
	}
}

func TestInitOffersLinksAndWritesSteeringLine(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	app := filepath.Join(parent, "app")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\nrequire example.com/lib v1.2.0\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nfunc main() {}\n")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n")

	// Non-interactive, no flag: reported, not linked, no steering line.
	if rc := cmdInit([]string{app, "--harness", "claude", "--interface", "cli"}); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	yaml, _ := os.ReadFile(filepath.Join(app, "prism.yaml"))
	steering, _ := os.ReadFile(filepath.Join(app, "CLAUDE.md"))
	if strings.Contains(string(yaml), "workspace_repo") || strings.Contains(string(steering), "Linked repositories") {
		t.Fatalf("init linked a repository without being asked:\n%s\n%s", yaml, steering)
	}

	if rc := cmdInit([]string{app, "--harness", "claude", "--interface", "cli", "--workspace", "auto"}); rc != 0 {
		t.Fatalf("init --workspace auto rc=%d", rc)
	}
	yaml, _ = os.ReadFile(filepath.Join(app, "prism.yaml"))
	steering, _ = os.ReadFile(filepath.Join(app, "CLAUDE.md"))
	if !strings.Contains(string(yaml), "workspace_repo: lib=../lib") {
		t.Fatalf("--workspace auto did not link the dependency:\n%s", yaml)
	}
	if !strings.Contains(string(steering), "Linked repositories: lib (../lib).") || strings.Count(string(steering), "<!-- prism:end -->") != 1 {
		t.Fatalf("steering line missing or the block is malformed:\n%s", steering)
	}
	// Re-running keeps one link and one line.
	if rc := cmdInit([]string{app, "--harness", "claude", "--interface", "cli", "--workspace", "auto"}); rc != 0 {
		t.Fatalf("re-init rc=%d", rc)
	}
	yaml, _ = os.ReadFile(filepath.Join(app, "prism.yaml"))
	steering, _ = os.ReadFile(filepath.Join(app, "CLAUDE.md"))
	if strings.Count(string(yaml), "workspace_repo") != 1 || strings.Count(string(steering), "Linked repositories") != 1 {
		t.Fatalf("re-init duplicated the link or the line:\n%s\n%s", yaml, steering)
	}
}

// A checkout that is not a sibling is found through the registry once
// Prism has been set up in it.
func TestDiscoverUsesTheRegistry(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	writeCLIFile(t, base, "work/app/go.mod", "module example.com/app\n\nrequire example.com/far v1.0.0\n")
	writeCLIFile(t, base, "work/app/prism.yaml", "version: 2\n")
	writeCLIFile(t, base, "elsewhere/deep/far/go.mod", "module example.com/far\n")
	t.Chdir(filepath.Join(base, "work", "app"))
	out := captureStdout(func() { workspaceDiscover(nil) })
	if strings.Contains(out, "far") {
		t.Fatalf("an unregistered, non-sibling checkout was found:\n%s", out)
	}
	workspace.Register(filepath.Join(base, "elsewhere", "deep", "far"))
	out = captureStdout(func() { workspaceDiscover(nil) })
	if !strings.Contains(out, "elsewhere/deep/far") || !strings.Contains(out, "depends on") {
		t.Fatalf("registered checkout not discovered:\n%s", out)
	}
}

// A dependency that is not checked out: fetched into the cache at a commit,
// indexed once, and answered from like a linked checkout.
func TestWorkspaceCacheClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_CACHE_DIR", filepath.Join(base, "cache"))
	t.Setenv("PRISM_STATE_DIR", filepath.Join(base, "state"))
	upstream := filepath.Join(base, "upstream", "lib")
	writeCLIFile(t, base, "upstream/lib/lib.go", "package lib\n\n// CloneMarker one\nfunc FromClone() int { return 1 }\n")
	gitIn(t, upstream, "init", "-q", "-b", "main")
	gitIn(t, upstream, "add", "-A")
	gitIn(t, upstream, "commit", "-q", "-m", "one")
	app := filepath.Join(base, "app")
	writeCLIFile(t, base, "app/prism.yaml", "version: 2\n")
	writeCLIFile(t, base, "app/main.go", "package main\n\nfunc main() {}\n")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(app)
	if rc := workspaceAdd([]string{"file://" + upstream + "@main"}); rc != 0 {
		t.Fatalf("workspace add <url> rc=%d", rc)
	}
	yaml, _ := os.ReadFile(filepath.Join(app, "prism.yaml"))
	if strings.Contains(string(yaml), "workspace_repo") {
		t.Fatalf("a machine-specific cache link was written into prism.yaml:\n%s", yaml)
	}
	out := captureStdout(func() { cmdLookup([]string{"FromClone"}) })
	if !strings.Contains(out, "return 1") || !strings.Contains(out, "linked repository lib") {
		t.Fatalf("lookup did not answer from the cache clone:\n%s", out)
	}
	status := captureStdout(func() { cmdWorkspace(nil) })
	if !strings.Contains(status, "cache clone of file://") || !strings.Contains(status, "at main") {
		t.Fatalf("status does not show the cache clone:\n%s", status)
	}

	// The branch moves upstream. The old clone is untouched until fetch;
	// fetch adds the new commit and repoints the link.
	writeCLIFile(t, base, "upstream/lib/lib.go", "package lib\n\n// CloneMarker two\nfunc FromClone() int { return 2 }\n")
	gitIn(t, upstream, "commit", "-q", "-am", "two")
	if out = captureStdout(func() { cmdLookup([]string{"FromClone"}) }); !strings.Contains(out, "return 1") {
		t.Fatalf("a cache clone changed without a fetch:\n%s", out)
	}
	if rc := workspaceFetch(nil); rc != 0 {
		t.Fatalf("fetch rc=%d", rc)
	}
	if out = captureStdout(func() { cmdLookup([]string{"FromClone"}) }); !strings.Contains(out, "return 2") {
		t.Fatalf("fetch did not move the link to the new commit:\n%s", out)
	}
	if removed := workspace.PruneClones(); len(removed) != 1 {
		t.Fatalf("prune removed %v; want the one superseded clone", removed)
	}
	if rc := workspaceRemove([]string{"lib"}); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	if out = captureStdout(func() { cmdLookup([]string{"FromClone"}) }); strings.Contains(out, "return 2") {
		t.Fatalf("an unlinked cache clone still answers:\n%s", out)
	}
}

// A consumer that is not on this machine: it uploaded its link bundle to
// the server, and the library's change-impact lists its call site at the
// uploaded commit.
func TestWorkspaceServerAnswersForRepositoriesNotCheckedOut(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	store, err := linkserver.Open(filepath.Join(base, "store"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(store.Handler("s3cret"))
	defer srv.Close()
	t.Setenv("PRISM_LINK_TOKEN", "s3cret")

	lib, app := filepath.Join(base, "lib"), filepath.Join(base, "app")
	writeCLIFile(t, base, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, base, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n")
	writeCLIFile(t, base, "lib/prism.yaml", "version: 2\nworkspace_server: "+srv.URL+"\n")
	writeCLIFile(t, base, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, base, "app/.gitignore", ".grove/\n")
	writeCLIFile(t, base, "app/prism.yaml", "version: 2\n")
	writeCLIFile(t, base, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper())\n}\n")
	gitIn(t, app, "init", "-q", "-b", "main")
	gitIn(t, app, "add", "-A")
	gitIn(t, app, "commit", "-q", "-m", "app")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}

	// The consumer's CI: push its bundle.
	t.Chdir(app)
	if rc := workspaceExport(true, []string{"--server", srv.URL, "--repo", "example.org/team/app"}); rc != 0 {
		t.Fatalf("workspace push rc=%d", rc)
	}
	// A tree with uncommitted changes is refused: its commit would lie.
	writeCLIFile(t, base, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() {\n\tprintln(lib.Helper(), 2)\n}\n")
	if rc := workspaceExport(true, []string{"--server", srv.URL, "--repo", "example.org/team/app"}); rc == 0 {
		t.Fatal("a dirty tree was pushed")
	}

	// The library author, with the consumer nowhere on disk as far as the
	// workspace knows.
	t.Chdir(lib)
	impact := func() string {
		return captureStdout(func() { cmdChangeImpact([]string{"Helper", "--format", "text"}) })
	}
	out := impact()
	for _, want := range []string{
		"// repo example.org/team/app@",
		"(main)  from the server, not checked out  requires example.com/lib v0.3.0",
		"example.org/team/app:main.go:6:14: println(lib.Helper())  [in main]",
		"1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit",
		"repositories neither linked nor uploaded are not covered",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("change-impact output missing %q:\n%s", want, out)
		}
	}

	// A name declared only in an uploaded repository: lookup points at it.
	if look := captureStdout(func() { cmdLookup([]string{"main"}) }); !strings.Contains(look, "example.org/team/app@") ||
		!strings.Contains(look, "main.go:5") || !strings.Contains(look, "signature only") {
		t.Fatalf("lookup did not point at the server's declaration:\n%s", look)
	}

	// A wrong token is a server that was not asked, and is said so.
	t.Setenv("PRISM_LINK_TOKEN", "wrong")
	if out = impact(); !strings.Contains(out, "NOT REACHED") || !strings.Contains(out, "repositories only it knows are not covered") {
		t.Fatalf("an unauthorized server answer was not reported as not covered:\n%s", out)
	}
	t.Setenv("PRISM_LINK_TOKEN", "s3cret")

	// Once the consumer is linked locally, the local answer stands alone:
	// the server's copy of the same repository is not listed again.
	gitIn(t, app, "checkout", "-q", "--", "main.go")
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	if rc := workspaceAdd([]string{"../app"}); rc != 0 {
		t.Fatalf("link app rc=%d", rc)
	}
	out = impact()
	if !strings.Contains(out, "../app/main.go:6:14") || strings.Contains(out, "example.org/team/app:main.go") {
		t.Fatalf("a locally linked repository was also listed from the server:\n%s", out)
	}
}

func TestWorkspaceChangeImpactPython(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/extlib/__init__.py", "from .models import persist\n")
	writeCLIFile(t, parent, "lib/extlib/models.py", "def persist(m):\n    pass\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/app/use.py", "from extlib import persist\n\n\ndef run(m):\n    persist(m)\n")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() { cmdChangeImpact([]string{"persist", "--format", "text"}) })
	if !strings.Contains(out, "../app/app/use.py:5:5: persist(m)  [in run]  [import-scoped: matched by module and name]") {
		t.Fatalf("Python consumer in the linked repository not listed:\n%s", out)
	}
}

// A plain parent folder declares its checkouts once; each then sees the
// others, and a command run inside one still answers for that one.
func TestWorkspaceParentFolderDeclaresChildren(t *testing.T) {
	parent, _ := workspaceFixture(t)
	writeCLIFile(t, parent, "prism.yaml", "version: 2\n")
	t.Chdir(parent)
	if rc := workspaceAdd([]string{"app", "lib", "other"}); rc != 0 {
		t.Fatalf("add children rc=%d", rc)
	}
	yaml, _ := os.ReadFile(filepath.Join(parent, "prism.yaml"))
	for _, want := range []string{"workspace_repo: app=app", "workspace_repo: lib=lib", "workspace_repo: other=other"} {
		if !strings.Contains(string(yaml), want) {
			t.Fatalf("parent prism.yaml missing %q:\n%s", want, yaml)
		}
	}
	t.Chdir(filepath.Join(parent, "other"))
	if root, _ := cliRoot(""); root != filepath.Join(parent, "other") {
		t.Fatalf("a command inside a child answered from %s", root)
	}
	out := captureStdout(func() { cmdWorkspace(nil) })
	if !strings.Contains(out, "../app") || !strings.Contains(out, "../lib") || strings.Contains(out, "../other") {
		t.Fatalf("child does not see its siblings through the parent file:\n%s", out)
	}
	// Inside a repository, a path below it is still refused.
	t.Chdir(filepath.Join(parent, "app"))
	if err := os.MkdirAll(filepath.Join(parent, "app", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rc := workspaceAdd([]string{"sub"}); rc == 0 {
		t.Fatal("a directory inside the repository was linked to it")
	}
}
