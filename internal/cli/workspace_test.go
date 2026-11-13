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
	if !strings.Contains(out, "NOT SEARCHED: lib (../lib)") || !strings.Contains(out, "may be defined there") {
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
		if rc := cmdWorkspace([]string{"discover"}); rc != 0 {
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
	if rc := cmdWorkspace([]string{"discover", "--link"}); rc != 0 {
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
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\nworkspace_repo: idle=../idle\nworkspace_repo: raw=../raw\n")
	writeCLIFile(t, parent, "raw/go.mod", "module example.com/raw\n\ngo 1.22\n\nrequire example.com/lib v0.2.0\n")
	writeCLIFile(t, parent, "raw/use.go", "package raw\n\nimport \"example.com/lib\"\n\nfunc Use() int { return lib.Helper() }\n")
	writeCLIFile(t, parent, "raw/other.go", "package raw\n\n// Helper is mentioned here, in a file that never names the library.\nfunc Helper() {}\n")
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
		"1 resolved site(s) in 1 of 3 linked repositories",
		// idle's go.mod does not name the library: it cannot reference it.
		"1 skipped (their manifests do not require it)",
		// raw requires the library but was never indexed: text matches,
		// labelled, instead of silence.
		"// repo raw  ../raw  requires example.com/lib v0.2.0  NAME-MATCHED ONLY (no index there)",
		"../raw/use.go:5: func Use() int { return lib.Helper() }  [name match, not resolved]",
		"NAME-MATCHED ONLY, not resolved (1 line(s)): raw",
		"repositories that are not linked are not covered",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("change-impact output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "raw/other.go") {
		t.Fatalf("a name match in a file that never mentions the library was listed:\n%s", out)
	}
	for _, dir := range []string{idle, filepath.Join(parent, "raw")} {
		if _, err := os.Stat(filepath.Join(dir, ".grove")); err == nil {
			t.Fatalf("change-impact from lib created an index in %s", dir)
		}
	}
	// The consumer's site leads; this repository's own lists follow.
	if strings.Index(out, "../app/main.go:6:14") > strings.Index(out, "declarations (") {
		t.Fatalf("linked consumers are printed after this repository's lists:\n%s", out)
	}
	if !strings.Contains(out, "site(s) in this repository + 1 in linked repositories") {
		t.Fatalf("the first line does not count the linked site:\n%s", out)
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
	if !strings.Contains(out, "0 resolved site(s) in 1 of 1 linked repositories") {
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
		"verify: complete for this repository; NOT checked: 1 line(s) in linked repositories reference a contract this diff changed or removed",
		"1 resolved site(s) in 1 of 1 linked repositories",
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
		"linked repositories: 2 resolved site(s) in 1 of 1 linked repositories",
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
	out := captureStdout(func() { cmdWorkspace([]string{"discover"}) })
	if strings.Contains(out, "elsewhere/deep/far") || !strings.Contains(out, "unlinked: go:example.com/far") {
		t.Fatalf("an unregistered, non-sibling checkout must be listed as unlinked, not found:\n%s", out)
	}
	workspace.Register(filepath.Join(base, "elsewhere", "deep", "far"))
	out = captureStdout(func() { cmdWorkspace([]string{"discover"}) })
	if !strings.Contains(out, "elsewhere/deep/far") || !strings.Contains(out, "depends on") || strings.Contains(out, "unlinked:") {
		t.Fatalf("registered checkout not discovered, or still listed as unlinked:\n%s", out)
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
	if !strings.Contains(out, "../app/app/use.py:5:5: persist(m)  [in run]  [import-scoped]") {
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

// A repository that is linked but not on disk must be named in every
// answer, never dropped from the count.
func TestWorkspaceMissingCheckoutIsNamed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib := filepath.Join(parent, "lib")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n\nrequire (\n\tgithub.com/acme/core v1.4.0\n\texample.com/near v0.1.0\n)\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Helper() int { return 1 }\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: gone=../gone\n")
	writeCLIFile(t, parent, "near/go.mod", "module example.com/near\n")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() { cmdChangeImpact([]string{"Helper", "--format", "text"}) })
	for _, want := range []string{
		"// repo gone  ../gone  NOT SEARCHED: not checked out",
		"0 resolved site(s) in 0 of 1 linked repositories",
		"not searched (see above): gone",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("change-impact output missing %q:\n%s", want, out)
		}
	}
	if out = captureStdout(func() { cmdLookup([]string{"Absent"}) }); !strings.Contains(out, "gone (../gone, not checked out)") {
		t.Fatalf("lookup miss does not name the missing checkout:\n%s", out)
	}
	if out = captureStdout(func() { cmdSearch([]string{"Helper", "--scope", "text"}) }); !strings.Contains(out, "gone not checked out") {
		t.Fatalf("search does not name the missing checkout:\n%s", out)
	}

	// discover: the dependency with a sibling checkout is a candidate; the
	// one with none is listed as unlinked, with where to fetch it.
	out = captureStdout(func() { cmdWorkspace([]string{"discover"}) })
	if !strings.Contains(out, "../near") {
		t.Fatalf("sibling dependency not discovered:\n%s", out)
	}
	if !strings.Contains(out, "unlinked: go:github.com/acme/core") || !strings.Contains(out, "v1.4.0") ||
		!strings.Contains(out, "https://github.com/acme/core") || strings.Contains(out, "unlinked: go:example.com/near") {
		t.Fatalf("unlinked dependencies not listed correctly:\n%s", out)
	}
}

func hasJDK() bool {
	if os.Getenv("JAVA_HOME") != "" {
		return true
	}
	m, _ := filepath.Glob("/opt/homebrew/opt/openjdk*")
	n, _ := filepath.Glob("/usr/lib/jvm/*")
	return len(m)+len(n) > 0
}

// Two Java repositories with nothing installed between them: no jar of the
// library on the application's classpath. Linking them once, from either
// side, is enough for the library's change-impact to list the application's
// call sites, because the link is recorded at both ends and the application
// is then analysed against the library's sources.
func TestWorkspaceJavaLinkedFromTheLibrarySide(t *testing.T) {
	if !hasJDK() {
		t.Skip("no JDK")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(base, "state"))
	lib, app := filepath.Join(base, "one", "lib"), filepath.Join(base, "two", "deep", "app")
	pom := func(artifact string) string {
		return "<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>" + artifact + "</artifactId><version>1.0</version></project>\n"
	}
	writeCLIFile(t, lib, "pom.xml", pom("lib"))
	writeCLIFile(t, lib, "prism.yaml", "version: 2\n")
	writeCLIFile(t, lib, "src/main/java/com/acme/lib/Box.java", "package com.acme.lib;\n\npublic class Box {\n    public int open() { return 1; }\n}\n")
	writeCLIFile(t, app, "pom.xml", pom("app"))
	writeCLIFile(t, app, "src/main/java/com/acme/app/Main.java", "package com.acme.app;\n\nimport com.acme.lib.Box;\n\npublic class Main {\n    static int run(Box b) {\n        return b.open();\n    }\n}\n")
	// --allow-heuristic: the fixture's pom has no resolvable Maven setup,
	// which cmdIndex reports as a readiness problem; javac still runs.
	if rc := cmdIndex([]string{lib, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	t.Chdir(lib)
	// One command, from the library, with the application never indexed.
	if rc := workspaceAdd([]string{app}); rc != 0 {
		t.Fatalf("workspace add rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(app, ".grove", "grove.db")); err != nil {
		t.Fatal("workspace add did not index the repository it linked")
	}
	out := captureStdout(func() { cmdChangeImpact([]string{"Box.open", "--format", "text"}) })
	if !strings.Contains(out, "src/main/java/com/acme/app/Main.java:7:18: return b.open();  [in Main.run]") {
		t.Fatalf("the Java call site in the linked application is not listed:\n%s", out)
	}
	// The type itself: the parameter that names it.
	out = captureStdout(func() { cmdChangeImpact([]string{"Box", "--format", "text"}) })
	if !strings.Contains(out, "Main.java:6:") {
		t.Fatalf("the use of the type in the linked application is not listed:\n%s", out)
	}
	// And the application sees the library, without a prism.yaml of its own.
	t.Chdir(app)
	out = captureStdout(func() { cmdWorkspace(nil) })
	if !strings.Contains(out, "lib") || !strings.Contains(out, "recorded on this machine") {
		t.Fatalf("the application does not see the library that linked it:\n%s", out)
	}
	// Unlinking removes both ends.
	t.Chdir(lib)
	if rc := workspaceRemove([]string{"app"}); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	t.Chdir(app)
	if out = captureStdout(func() { cmdWorkspace(nil) }); !strings.Contains(out, "no linked repositories") {
		t.Fatalf("the reverse end of a removed link is still there:\n%s", out)
	}
}

// A cloned repository's committed prism.yaml must not be able to point Prism
// at arbitrary places on the machine. Its links are proposals until the user
// runs a workspace command there, and even then only a real repository that
// does not swallow this one is accepted.
func TestWorkspaceHostileLinksAreRefused(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(base, "state"))
	hostile := filepath.Join(base, "code", "hostile")
	writeCLIFile(t, hostile, "main.go", "package main\n\nfunc main() {}\n")
	writeCLIFile(t, base, "code/victim/go.mod", "module example.com/victim\n")
	writeCLIFile(t, base, "code/victim/secret.go", "package victim\n\n// VictimMarker\n")
	writeCLIFile(t, base, "loot/id_rsa", "PRIVATE-KEY-MARKER\n")
	home, _ := os.UserHomeDir()
	writeCLIFile(t, hostile, "prism.yaml", "version: 2\n"+
		"workspace_repo: rootfs=/\n"+
		"workspace_repo: home="+home+"\n"+
		"workspace_repo: loot=../../loot\n"+
		"workspace_repo: victim=../victim\n")
	t.Chdir(hostile)

	// Before any confirming command: an agent's read and search reach nothing
	// outside the repository. (No explicit index: the first read builds one.)
	for _, path := range []string{"../victim/secret.go", "../../loot/id_rsa", "/etc/hosts"} {
		out := captureStdout(func() {
			if rc := cmdRead([]string{path}); rc == 0 {
				t.Fatalf("read of %s succeeded through an unconfirmed link", path)
			}
		})
		if strings.Contains(out, "VictimMarker") || strings.Contains(out, "PRIVATE-KEY-MARKER") {
			t.Fatalf("content leaked through an unconfirmed link:\n%s", out)
		}
	}
	out := captureStdout(func() { cmdSearch([]string{"VictimMarker", "--scope", "text"}) })
	if strings.Contains(out, "victim 1") || !strings.Contains(out, "link not confirmed") {
		t.Fatalf("an unconfirmed link was searched, or the search did not say it was skipped:\n%s", out)
	}

	// The user reviews the links. Only the real repository is accepted.
	status := captureStdout(func() { cmdWorkspace(nil) })
	for _, want := range []string{"refused: it contains this repository", "refused: it is the home directory or above it", "refused: no sign of a repository there"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status does not show %q:\n%s", want, status)
		}
	}
	if rc := cmdRead([]string{"../../loot/id_rsa"}); rc == 0 {
		t.Fatal("a non-repository directory became readable after confirmation")
	}
	out = captureStdout(func() {
		if rc := cmdRead([]string{"../victim/secret.go"}); rc != 0 {
			t.Fatalf("a confirmed link to a real repository is not readable (rc=%d)", rc)
		}
	})
	if !strings.Contains(out, "VictimMarker") {
		t.Fatalf("confirmed repository not delivered:\n%s", out)
	}

	// Untrusted mode: nothing is linked, confirmed or not.
	t.Setenv("GROVE_UNTRUSTED", "1")
	if rc := cmdRead([]string{"../victim/secret.go"}); rc == 0 {
		t.Fatal("a linked repository was readable in untrusted mode")
	}
}

func TestWorkspaceCloneRefusesOptionInjection(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_CACHE_DIR", filepath.Join(base, "cache"))
	for _, bad := range [][2]string{
		{"--upload-pack=touch " + filepath.Join(base, "pwned"), ""},
		{"ext::sh -c 'touch " + filepath.Join(base, "pwned") + "'", ""},
		{"https://example.invalid/x.git", "--output=" + filepath.Join(base, "pwned")},
	} {
		if _, _, err := workspace.FetchClone(bad[0], bad[1]); err == nil {
			t.Fatalf("FetchClone accepted %q at %q", bad[0], bad[1])
		}
	}
	if _, err := os.Stat(filepath.Join(base, "pwned")); err == nil {
		t.Fatal("a crafted repository argument ran a command")
	}
	if got := workspace.RedactURL("https://user:s3cret@example.com/org/repo.git"); strings.Contains(got, "s3cret") {
		t.Fatalf("credential not redacted: %s", got)
	}
}

// lookup across linked repositories: the package qualifier picks the right
// definition, a tie is reported instead of resolved by link order, and
// --repo reaches a name this repository also defines.
func TestWorkspaceLookupQualifierAmbiguityAndRepo(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	app := filepath.Join(parent, "app")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nfunc main() {}\n\n// Shared: the application's own.\nfunc Shared() int { return 0 }\n")
	writeCLIFile(t, parent, "app/prism.yaml", "version: 2\nworkspace_repo: one=../one\nworkspace_repo: two=../two\n")
	writeCLIFile(t, parent, "one/go.mod", "module example.com/one\n")
	writeCLIFile(t, parent, "one/store/store.go", "package store\n\nfunc Normalize(s string) string { return \"one-store\" }\n")
	writeCLIFile(t, parent, "one/misc/misc.go", "package misc\n\nfunc Normalize(s string) string { return \"one-misc\" }\n\nfunc Shared() int { return 1 }\n")
	writeCLIFile(t, parent, "two/go.mod", "module example.com/two\n")
	writeCLIFile(t, parent, "two/text/text.go", "package text\n\nfunc Normalize(s string) string { return \"two-text\" }\n")
	for _, r := range []string{"app", "one", "two"} {
		if rc := cmdIndex([]string{filepath.Join(parent, r)}); rc != 0 {
			t.Fatalf("index %s rc=%d", r, rc)
		}
	}
	t.Chdir(app)
	out := captureStdout(func() { cmdLookup([]string{"store.Normalize"}) })
	if !strings.Contains(out, "one-store") || strings.Contains(out, "AMBIGUOUS") {
		t.Fatalf("the package qualifier did not select one/store:\n%s", out)
	}
	out = captureStdout(func() { cmdLookup([]string{"Normalize"}) })
	if !strings.Contains(out, "AMBIGUOUS") || !strings.Contains(out, "../one/misc/misc.go") || !strings.Contains(out, "../two/text/text.go") {
		t.Fatalf("an unqualified name defined in three places was not reported as ambiguous with its candidates:\n%s", out)
	}
	out = captureStdout(func() { cmdLookup([]string{"Shared"}) })
	if !strings.Contains(out, "return 0") {
		t.Fatalf("a name this repository defines was not answered here:\n%s", out)
	}
	out = captureStdout(func() { cmdLookup([]string{"Shared", "--repo", "one"}) })
	if !strings.Contains(out, "return 1") || !strings.Contains(out, "../one/misc/misc.go") || strings.Contains(out, "not defined in this repository") {
		t.Fatalf("--repo did not reach the linked repository's definition, or claimed the name is not defined here:\n%s", out)
	}
	if out = captureStdout(func() { cmdLookup([]string{"Shared", "--repo", "nope"}) }); !strings.Contains(out, `no linked repository "nope"`) || strings.Contains(out, "return") {
		t.Fatalf("--repo with an unknown repository did not say so:\n%s", out)
	}

	// search: the CLI is told the CLI flag, and a path filter is not turned
	// into a false zero for the other repositories.
	out = captureStdout(func() { cmdSearch([]string{"Normalize", "--scope", "text"}) })
	if !strings.Contains(out, "--repo <id> lists them") || !strings.Contains(out, "one 2 in 2 file(s)") {
		t.Fatalf("count line wording or counts wrong:\n%s", out)
	}
	out = captureStdout(func() { cmdSearch([]string{"Normalize", "--scope", "text", "--path", "main.go"}) })
	if strings.Contains(out, "one 0") || !strings.Contains(out, "path filter applies to this repository") {
		t.Fatalf("a path filter produced a zero count for linked repositories:\n%s", out)
	}
}

// verify --removed: clean here, still named by a linked consumer.
func TestWorkspaceVerifyRemovedLooksAtLinkedRepositories(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	lib := filepath.Join(parent, "lib")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\n\nfunc Kept() {}\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\nrequire example.com/lib v1.0.0\n")
	writeCLIFile(t, parent, "app/main.go", "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.OldHelper() }\n")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() { cmdVerify([]string{"--removed", "OldHelper", "--format", "text"}) })
	for _, want := range []string{"clean in this repository; linked repositories still name the removed identifiers", "app (../app): OldHelper 1 line(s) in 1 file(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify --removed output missing %q:\n%s", want, out)
		}
	}
}

// The classic cross-repository break: the library changes an interface and a
// type in a consumer stops implementing it. No call names the method there.
func TestWorkspaceChangeImpactListsImplementers(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/store.go", "package lib\n\ntype Store interface {\n\tGet(key string) (string, error)\n}\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/file.go", "package app\n\nimport \"example.com/lib\"\n\ntype FileStore struct{}\n\nfunc (f *FileStore) Get(key string) (string, error) { return key, nil }\n\nvar _ lib.Store = (*FileStore)(nil)\n")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() { cmdChangeImpact([]string{"Store.Get", "--format", "text"}) })
	if !strings.Contains(out, "../app/file.go:7: func (f *FileStore) Get(key string) (string, error) { return key, nil }  [implements lib.Store]") {
		t.Fatalf("the consumer type implementing the changed interface method is not listed:\n%s", out)
	}
}

// A sentinel error, a constant and a struct field are part of a library's
// surface: their uses in a linked repository are listed, or, where a kind is
// not recorded, the answer still says what was and was not searched.
func TestWorkspaceChangeImpactVariablesAndFields(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(parent, "state"))
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/conf.go", "package lib\n\nimport \"errors\"\n\nvar ErrGone = errors.New(\"gone\")\n\ntype Config struct {\n\tRoot string\n}\n")
	writeCLIFile(t, parent, "lib/prism.yaml", "version: 2\nworkspace_repo: app=../app\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.3.0\n\nreplace example.com/lib => ../lib\n")
	writeCLIFile(t, parent, "app/use.go", "package app\n\nimport \"example.com/lib\"\n\nfunc Check(err error) string {\n\tif err == lib.ErrGone {\n\t\treturn \"\"\n\t}\n\treturn lib.Config{Root: \"x\"}.Root\n}\n")
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	t.Chdir(lib)
	out := captureStdout(func() { cmdChangeImpact([]string{"ErrGone", "--format", "text"}) })
	if !strings.Contains(out, "../app/use.go:6:16: if err == lib.ErrGone {  [in Check]") || !strings.Contains(out, "workspaceScope:") {
		t.Fatalf("the consumer comparing the sentinel error is not listed:\n%s", out)
	}
	out = captureStdout(func() { cmdChangeImpact([]string{"Config.Root", "--format", "text"}) })
	if !strings.Contains(out, "../app/use.go:9:") || !strings.Contains(out, "workspaceScope:") {
		t.Fatalf("the consumer setting the struct field is not listed:\n%s", out)
	}
}
