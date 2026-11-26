package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// End-to-end snapshots (ENG-06): every command runs through Run on a copy of
// testdata/e2e/fixture, and its text output is compared with a golden file
// under testdata/e2e/golden. After an intended output change, rewrite the
// goldens with
//
//	go test ./internal/cli -run TestE2E -update
//
// and review the diff: it is the change users and agents will see.
//
// The fixture encodes the cases behind BUG-01 to BUG-04: two Go types named
// Cache in different packages, a Python Cache, an interface (store.Store)
// with three implementers, a package variable (config.Default) used across
// packages, a test file, and an arch_deny rule the code breaks.

var update = flag.Bool("update", false, "rewrite the e2e golden files")

type e2eCase struct {
	name string
	args []string
	exit int
	// json: also run with --format json and check the result parses and
	// exits the same.
	json bool
	// sites: the output has a "N edit site(s)" header and an
	// "editSites (N; ...)" list whose counts must agree (BUG-19).
	sites bool
	// edit: runs after e2eEdit changed the working tree (cases are ordered).
	edit bool
}

var e2eCases = []e2eCase{
	{name: "status", args: []string{"status"}, json: true},
	{name: "lookup", args: []string{"lookup", "Lookup", "mem.Cache.Get"}, json: true},
	{name: "lookup-unknown", args: []string{"lookup", "NoSuchThing"}, exit: 4, json: true},
	{name: "read-range", args: []string{"read", "app/app.go", "--from", "12", "--to", "17"}, json: true},
	{name: "search-text", args: []string{"search", "Default", "--scope", "text"}, json: true},
	{name: "search-symbols", args: []string{"search", "Cache", "--scope", "symbols"}, json: true},
	{name: "search-bad-regex", args: []string{"search", "[unclosed", "--regex"}, exit: 2, json: true},
	{name: "query", args: []string{"query", "--terms", "Lookup,Store"}, json: true},
	{name: "query-unknown", args: []string{"query", "--terms", "NoSuchThing"}},
	{name: "query-missing-terms", args: []string{"query"}, exit: 2},
	{name: "change-impact-interface", args: []string{"change-impact", "Store.Get"}, json: true, sites: true},
	{name: "change-impact-ambiguous", args: []string{"change-impact", "Cache.Get"}, exit: 5, json: true},
	{name: "change-impact-scoped", args: []string{"change-impact", "Cache.Get", "--file", "mem/cache.go"}, json: true, sites: true},
	{name: "change-impact-variable", args: []string{"change-impact", "Default"}, json: true, sites: true},
	{name: "change-impact-unknown", args: []string{"change-impact", "NoSuchThing"}, exit: 4, json: true},
	{name: "change-impact-bad-flag", args: []string{"change-impact", "Store.Get", "--no-such-flag"}, exit: 2},
	{name: "missing-implementations", args: []string{"missing-implementations", "Store.Close"}, json: true},
	{name: "rename-plan", args: []string{"rename-plan", "Lookup", "Find"}, json: true},
	{name: "references", args: []string{"references", "Default"}, json: true},
	{name: "callers", args: []string{"callers", "Lookup"}, json: true},
	{name: "path", args: []string{"path", "All", "Store.Get"}, json: true},
	{name: "edges", args: []string{"edges", "Lookup"}, json: true},
	{name: "node", args: []string{"node", "Lookup"}, json: true},
	{name: "resolve", args: []string{"resolve", "Cache"}, json: true},
	{name: "dead-code", args: []string{"dead-code"}, json: true},
	{name: "map", args: []string{"map"}, json: true},
	{name: "cycles", args: []string{"cycles"}, json: true},
	{name: "arch", args: []string{"arch"}, exit: 1, json: true},
	{name: "verify-clean", args: []string{"verify"}, json: true},
	{name: "unknown-command", args: []string{"no-such-command"}, exit: 2},
	// From here on the working tree has an edit: mem.Cache.Get changes body.
	{name: "test-plan", args: []string{"test-plan"}, json: true, edit: true},
	{name: "verify-edited", args: []string{"verify"}, json: true, edit: true},
}

// e2eEdit is the working-tree change the edit cases run against.
func e2eEdit(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, "mem", "cache.go")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(data), "\treturn config.Default\n}", "\treturn \"\" + config.Default\n}", 1)
	if out == string(data) {
		t.Fatal("edit did not apply")
	}
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestE2E(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the fixture's compiler-backed pass needs the go toolchain")
	}
	t.Setenv("PRISM_UNTRUSTED", "")
	goldenDir, err := filepath.Abs(filepath.Join("testdata", "e2e", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	root := e2eFixture(t)
	t.Chdir(root)

	edited := false
	for _, c := range e2eCases {
		if c.edit && !edited {
			e2eEdit(t, root)
			edited = true
		}
		t.Run(c.name, func(t *testing.T) {
			rc, out, errOut := runE2E(append(append([]string{}, c.args...), "--format", "text"))
			if rc != c.exit {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", rc, c.exit, out, errOut)
			}
			text, errText := normalizeE2E(out, root), normalizeE2E(errOut, root)
			checkE2EText(t, text+errText, c)
			compareGolden(t, goldenDir, c, rc, text, errText)

			if c.json {
				jrc, jout, jerr := runE2E(append(append([]string{}, c.args...), "--format", "json"))
				if jrc != rc {
					t.Errorf("--format json exit %d, text exit %d\nstderr:\n%s", jrc, rc, jerr)
				}
				var v any
				if err := json.Unmarshal([]byte(jout), &v); err != nil {
					t.Errorf("--format json is not one JSON document: %v\n%s", err, jout)
				}
			}
		})
	}
}

// e2eFixture copies the fixture into a git repository and indexes it.
func e2eFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fx")
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "e2e", "fixture"))); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "-c", "user.email=e2e@example.com", "-c", "user.name=e2e", "commit", "-qm", "fixture")
	if rc, _, errOut := runE2E([]string{"index", root, "--format", "text"}); rc != 0 {
		t.Fatalf("index exit %d\n%s", rc, errOut)
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real
	}
	return root
}

func runE2E(args []string) (rc int, stdout, stderr string) {
	stderr = captureStderr(func() {
		stdout = captureStdout(func() { rc = Run(args) })
	})
	return rc, stdout, stderr
}

var (
	e2eDuration = regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s)\b`)
	e2eGoVer    = regexp.MustCompile(`\bgo1\.\d+(\.\d+)?\b`)
)

// normalizeE2E removes what differs between machines and runs: the fixture's
// absolute path, timings and the Go version.
func normalizeE2E(s, root string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range []string{root, filepath.ToSlash(root)} {
		s = strings.ReplaceAll(s, r, "$ROOT")
	}
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	s = e2eDuration.ReplaceAllString(s, "<dur>")
	s = e2eGoVer.ReplaceAllString(s, "<go>")
	return s
}

var (
	e2eMCPWords   = regexp.MustCompile(`prism_[a-z_]+|\bop=|\b[a-z_]+=(true|false)\b`)
	e2eSiteHeader = regexp.MustCompile(`(\d+) (?:edit|confirmed) site\(s\)`)
	e2eSiteList   = regexp.MustCompile(`editSites \((\d+);`)
)

// checkE2EText asserts what every text answer must hold, whatever its golden
// says: no JSON document (CON-02), no MCP vocabulary (CON-03) and, where
// there are edit sites, one count (BUG-19).
func checkE2EText(t *testing.T, text string, c e2eCase) {
	t.Helper()
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v any
		if json.Unmarshal([]byte(trimmed), &v) == nil {
			t.Errorf("--format text printed JSON:\n%s", text)
		}
	}
	if m := e2eMCPWords.FindAllString(text, -1); m != nil {
		t.Errorf("MCP vocabulary in CLI output: %q", m)
	}
	if c.sites {
		h, l := e2eSiteHeader.FindStringSubmatch(text), e2eSiteList.FindStringSubmatch(text)
		if h == nil || l == nil {
			t.Errorf("no site header or editSites list:\n%s", text)
		} else if h[1] != l[1] {
			t.Errorf("header says %s site(s), editSites lists %s", h[1], l[1])
		}
	}
}

func compareGolden(t *testing.T, dir string, c e2eCase, rc int, text, errText string) {
	t.Helper()
	got := "# prism " + strings.Join(c.args, " ") + " --format text\n# exit " + strconv.Itoa(rc) + "\n" + text
	if errText != "" {
		got += "# stderr\n" + errText
	}
	path := filepath.Join(dir, c.name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (run with -update to create it): %v", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if string(want) != got {
		t.Errorf("output differs from %s (rerun with -update after an intended change):\n%s", path, lineDiff(string(want), got))
	}
}

// lineDiff shows the first lines where want and got differ.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	shown := 0
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl == gl {
			continue
		}
		b.WriteString("line " + strconv.Itoa(i+1) + ":\n  - " + wl + "\n  + " + gl + "\n")
		if shown++; shown == 10 {
			b.WriteString("  ...\n")
			break
		}
	}
	return b.String()
}
