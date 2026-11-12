package cli

import (
	"os"
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
