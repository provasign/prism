package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/workspace"
)

func TestLinkedGraphQueriesRequireExplicitIndex(t *testing.T) {
	for _, emptyStore := range []bool{false, true} {
		name := "missing store"
		if emptyStore {
			name = "existing empty store"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("PRISM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			parent, app := workspaceFixture(t)
			lib := filepath.Join(parent, "lib")
			workspace.LinkReverse(app, lib)
			if emptyStore {
				c := grove.NewClient().WithTokenFromDir(lib).OneShot()
				if err := c.EnsureRunning(t.Context()); err != nil {
					t.Fatal(err)
				}
				c.Shutdown()
			}
			t.Chdir(app)
			old := openRepoGraphClient
			opens := 0
			openRepoGraphClient = func(dir string) (*config.Config, *grove.Client, error) { opens++; return old(dir) }
			t.Cleanup(func() { openRepoGraphClient = old })
			for _, args := range [][]string{
				{"query", "--terms", "Lib", "--repo", "lib", "--format", "text"},
				{"change-impact", "Lib", "--repo", "lib", "--format", "text"},
				{"lookup", "Lib", "--repo", "lib", "--path", "lib.go", "--format", "text"},
			} {
				var rc int
				stderr := captureStderr(func() { _ = captureStdout(func() { rc = Run(args) }) })
				if rc == 0 || !strings.Contains(stderr, "prism index '"+lib+"'") || strings.Contains(stderr, "building the index") {
					t.Fatalf("cold linked request must return an explicit index command: %v rc=%d\n%s", args, rc, stderr)
				}
			}
			if !emptyStore {
				if opens != 0 {
					t.Fatalf("opened cold linked graph %d times", opens)
				}
				if _, err := os.Stat(filepath.Join(lib, ".grove")); !os.IsNotExist(err) {
					t.Fatalf("cold query created a store: %v", err)
				}
			} else {
				c := grove.NewClient().WithTokenFromDir(lib).OneShot()
				if err := c.EnsureRunning(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer c.Shutdown()
				status, err := c.Status(t.Context())
				if err != nil || status.SymbolCount != 0 {
					t.Fatalf("request indexed the empty linked store: %+v, %v", status, err)
				}
			}
		})
	}
}

func TestSelectedCacheGraphIsRefusedBeforeOpen(t *testing.T) {
	for _, storeExists := range []bool{false, true} {
		name := "cold cache"
		if storeExists {
			name = "existing cache index"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("PRISM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			parent, app := workspaceFixture(t)
			cache := filepath.Join(parent, "cached")
			writeCLIFile(t, parent, "cached/lib.go", "package cached\n// CachedReadMarker\nfunc Lib() {}\n")
			if storeExists {
				writeCLIFile(t, parent, "cached/.grove/grove.db", "must not be opened or changed")
			}
			if err := workspace.LinkClone(app, workspace.CacheLink{ID: "cached", Path: cache, URL: "file://fixture", SHA: "fixture"}); err != nil {
				t.Fatal(err)
			}
			t.Chdir(app)
			old := openRepoGraphClient
			openRepoGraphClient = func(dir string) (*config.Config, *grove.Client, error) {
				t.Fatalf("opened a read-only cache graph: %s", dir)
				return nil, nil, nil
			}
			t.Cleanup(func() { openRepoGraphClient = old })
			for _, args := range [][]string{
				{"query", "--terms", "Lib", "--repo", "cached", "--format", "text"},
				{"change-impact", "Lib", "--repo", "cached", "--format", "text"},
				{"lookup", "Lib", "--repo", "cached", "--path", "lib.go", "--format", "text"},
			} {
				var rc int
				stderr := captureStderr(func() { _ = captureStdout(func() { rc = Run(args) }) })
				if rc == 0 || !strings.Contains(stderr, "read-only cache clone") || strings.Contains(stderr, "prism index") {
					t.Fatalf("bad cache refusal for %v: rc=%d\n%s", args, rc, stderr)
				}
			}
			out := captureStdout(func() {
				if rc := cmdRead([]string{"lib.go", "--repo", "cached", "--format", "text"}); rc != 0 {
					t.Fatalf("cached source read rc=%d", rc)
				}
			})
			if !strings.Contains(out, "CachedReadMarker") {
				t.Fatalf("cached source was not readable: %s", out)
			}
			if !storeExists {
				if _, err := os.Stat(filepath.Join(cache, ".grove")); !os.IsNotExist(err) {
					t.Fatalf("created cache store: %v", err)
				}
			} else {
				data, err := os.ReadFile(filepath.Join(cache, ".grove/grove.db"))
				if err != nil || string(data) != "must not be opened or changed" {
					t.Fatalf("cache index changed: %q %v", data, err)
				}
			}
		})
	}
}

func TestImpactBatchOpensOneClientAndKeepsPerNameErrors(t *testing.T) {
	t.Setenv("PRISM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	parent, app := workspaceFixture(t)
	for _, repo := range []string{"app", "lib"} {
		if err := os.Remove(filepath.Join(parent, repo, ".git")); err != nil {
			t.Fatal(err)
		}
	}
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.21\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\nfunc Target() int { return 7 }\nfunc Caller() int { return Target() }\n")
	writeCLIFile(t, parent, "app/go.mod", "module example.com/app\n\ngo 1.21\n")
	writeCLIFile(t, parent, "app/main.go", "package main\nfunc Target() int { return 99 }\nfunc Caller() int { return Target() }\nfunc config() int { return 1 }\n")
	writeCLIFile(t, parent, "app/config/README.md", "A directory that shares a symbol name.\n")
	for _, repo := range []string{app, filepath.Join(parent, "lib")} {
		if rc := cmdIndex([]string{repo}); rc != 0 {
			t.Fatalf("index %s: rc=%d", repo, rc)
		}
	}
	t.Chdir(app)
	old := openRepoGraphClient
	defer func() { openRepoGraphClient = old }()
	for _, selected := range []bool{false, true} {
		opens := 0
		var opened *grove.Client
		openRepoGraphClient = func(dir string) (*config.Config, *grove.Client, error) {
			opens++
			cfg, client, err := old(dir)
			opened = client
			return cfg, client, err
		}
		args := []string{"NoSuchBatchSymbol", "Target", "Caller", "--format", "json"}
		if selected {
			args = append(args, "--repo", "lib")
		}
		var rc int
		out := captureStdout(func() { rc = cmdChangeImpact(args) })
		if rc != exitNotFound || opens != 1 {
			t.Fatalf("selected=%v: rc=%d graph opens=%d, want 1/1\n%s", selected, rc, opens, out)
		}
		var decoded struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("batch JSON: %v\n%s", err, out)
		}
		if len(decoded.Results) != 3 || decoded.Results[0]["error"] == nil || decoded.Results[1]["result"] == nil || decoded.Results[2]["result"] == nil {
			t.Fatalf("one name's error lost later answers: %s", out)
		}
		if opened == nil || opened.Health(t.Context()) == nil {
			t.Fatal("batch did not close its one client")
		}
	}
	// The second bare name is a symbol even when a directory has that name.
	out := captureStdout(func() {
		if rc := cmdChangeImpact([]string{"Target", "config", "--format", "json"}); rc != 0 {
			t.Fatalf("directory-shaped symbol batch rc=%d", rc)
		}
	})
	var result struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Results) != 2 || result.Results[1]["query"] != "config" {
		t.Fatalf("directory swallowed a batch name: %v\n%s", err, out)
	}
	// Directory selection remains explicit and also works with a bare folder.
	for _, args := range [][]string{
		{"Target", "./", "--format", "text"},
		{"Target", "--dir", app, "--format", "text"},
	} {
		_ = captureStdout(func() {
			if rc := cmdChangeImpact(args); rc != 0 {
				t.Fatalf("explicit directory %v: rc=%d", args, rc)
			}
		})
	}

}

func reviewLinkedFixture(t *testing.T, two bool) (string, string) {
	t.Helper()
	t.Setenv("PRISM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeCLIFile(t, parent, "lib/go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeCLIFile(t, parent, "lib/lib.go", "package lib\nfunc First() {}\nfunc Second() {}\n")
	mod := "module example.com/app\n\ngo 1.22\nrequire example.com/lib v0.3.0\nreplace example.com/lib => ../lib\n"
	cfg := "version: 2\nworkspace_repo: lib=../lib\n"
	if two {
		writeCLIFile(t, parent, "other/go.mod", "module example.com/other\n\ngo 1.22\n")
		writeCLIFile(t, parent, "other/other.go", "package other\nfunc First() {}\n")
		mod += "require example.com/other v0.3.0\nreplace example.com/other => ../other\n"
		cfg += "workspace_repo: other=../other\n"
	}
	writeCLIFile(t, parent, "app/go.mod", mod)
	writeCLIFile(t, parent, "app/prism.yaml", cfg)
	writeCLIFile(t, parent, "app/main.go", "package main\nfunc main() {}\n")
	dirs := []string{lib, app}
	if two {
		dirs = append(dirs, filepath.Join(parent, "other"))
	}
	for _, d := range dirs {
		_ = captureStdout(func() {
			if rc := cmdIndex([]string{d}); rc != 0 {
				t.Fatalf("index %s: %d", d, rc)
			}
		})
	}
	t.Chdir(app)
	return app, lib
}
func TestLinkedImpactBatchReusesSelectedClient(t *testing.T) {
	app, lib := reviewLinkedFixture(t, false)
	old := openRepoGraphClient
	defer func() { openRepoGraphClient = old }()
	for _, format := range []string{"text", "json"} {
		opens := map[string]int{}
		var clients []*grove.Client
		openRepoGraphClient = func(dir string) (*config.Config, *grove.Client, error) {
			opens[dir]++
			cfg, c, err := old(dir)
			if c != nil {
				clients = append(clients, c)
			}
			return cfg, c, err
		}
		var rc int
		out := captureStdout(func() { rc = cmdChangeImpact([]string{"First", "MissingBatchName", "Second", "--format", format}) })
		if rc != exitNotFound || !strings.Contains(out, "First") || !strings.Contains(out, "Second") || !strings.Contains(out, "MissingBatchName") {
			t.Fatalf("format=%s: batch lost per-name results: rc=%d %s", format, rc, out)
		}
		if opens[lib] != 1 || opens[app] != 1 {
			t.Fatalf("format=%s: graph opens=%v; want one per repository", format, opens)
		}
		for _, c := range clients {
			if c.Health(t.Context()) == nil {
				t.Fatal("batch left a graph client open")
			}
		}
	}
}

func TestLinkedImpactPreservesAmbiguity(t *testing.T) {
	reviewLinkedFixture(t, true)
	var rc int
	var stdout string
	stderr := captureStderr(func() { stdout = captureStdout(func() { rc = cmdChangeImpact([]string{"First", "--format", "text"}) }) })
	if rc == 0 || !strings.Contains(stderr, "ambiguous") || !strings.Contains(stderr, "../lib/lib.go") || !strings.Contains(stderr, "../other/other.go") || strings.Contains(stdout, "// repository lib") {
		t.Fatalf("ambiguous linked definition not reported: rc=%d %s %s", rc, stdout, stderr)
	}
	_ = captureStdout(func() {
		if rc := cmdChangeImpact([]string{"First", "--repo", "lib", "--format", "text"}); rc != 0 {
			t.Fatalf("explicit repository rc=%d", rc)
		}
	})
}
