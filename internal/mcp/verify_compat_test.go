package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestGoAddedTrailingVariadic(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		want          bool
	}{
		{"func f(x int) int", "func f(x int, opts ...string) int", true},
		{"func (h *Handler) f(x int)", "func (h *Handler) f(x int, opts ...string)", true},
		{"func f(x int)", "func f(x string, opts ...string)", false},
		{"func f(x int) int", "func f(x int, opts ...string) string", false},
		{"func f(x int)", "func f(x int, opts string)", false},
		{"func f(x int)", "func g(x int, opts ...string)", false},
		{"nonsense", "func f(x int, opts ...string)", false},
	} {
		if got := goAddedTrailingVariadic(tc.before, tc.after); got != tc.want {
			t.Errorf("goAddedTrailingVariadic(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestVerifyOptionalGoVariadicDoesNotFlagValidCallers(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	path := filepath.Join(dir, "core", "core.go")
	if err := os.WriteFile(path, []byte("package core\n\ntype S struct{}\n\nfunc (S) Do(x string, n ...int) string { return x }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "complete" || len(anySlice(m["missedSites"])) != 0 {
		t.Fatalf("optional variadic parameter flagged existing callers: %v", m)
	}
}

func TestSyntheticAnonymousJavaSymbol(t *testing.T) {
	for _, tc := range []struct {
		file, name string
		want       bool
	}{
		{"src/Builder.java", "Builder.<anonymous@286:43>.match", true},
		{"src/Builder.java", "Builder.NamedMatcher.match", false},
		{"src/builder.go", "Builder.<anonymous@286:43>.match", false},
	} {
		got := isSyntheticAnonymousJavaSymbol(grove.SymbolRecord{
			FilePath: tc.file, QualifiedName: tc.name,
		})
		if got != tc.want {
			t.Errorf("isSyntheticAnonymousJavaSymbol(%q, %q) = %v, want %v", tc.file, tc.name, got, tc.want)
		}
	}
}

// variadicRepo commits base files, applies edits on top, and runs verify.
func variadicVerify(t *testing.T, base, edits map[string]string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/v\n\ngo 1.26\n")
	for rel, content := range base {
		write(rel, content)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "base"}} {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	for rel, content := range edits {
		write(rel, content)
	}
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

func missedFiles(m map[string]any) map[string]bool {
	files := map[string]bool{}
	for _, s := range anySlice(m["missedSites"]) {
		switch site := s.(type) {
		case missedSite:
			files[site.File] = true
		case map[string]any:
			files[stringArg(site, "file", "")] = true
		}
	}
	return files
}

func TestVerifyGoVariadicStillFlagsInterfaceImplementation(t *testing.T) {
	m := variadicVerify(t, map[string]string{
		"core/core.go": "package core\n\ntype Doer interface {\n\tDo(x string) string\n}\n\ntype S struct{}\n\nfunc (S) Do(x string) string { return x }\n\nvar _ Doer = S{}\n",
		"use/use.go":   "package use\n\nimport \"example.com/v/core\"\n\nfunc Run(d core.Doer) string { return d.Do(\"a\") }\n",
	}, map[string]string{
		"core/core.go": "package core\n\ntype Doer interface {\n\tDo(x string) string\n}\n\ntype S struct{}\n\nfunc (S) Do(x string, n ...int) string { return x }\n\nvar _ Doer = S{}\n",
	})
	if m["verdict"] == "complete" {
		t.Fatalf("S no longer implements Doer, but verify said complete: %v", m)
	}
}

func TestVerifyGoVariadicStillFlagsFunctionValue(t *testing.T) {
	m := variadicVerify(t, map[string]string{
		"core/core.go": "package core\n\nfunc Do(x string) string { return x }\n",
		"use/use.go":   "package use\n\nimport \"example.com/v/core\"\n\nvar Hook func(string) string = core.Do\n\nfunc Run() string { return core.Do(\"a\") }\n",
	}, map[string]string{
		"core/core.go": "package core\n\nfunc Do(x string, n ...int) string { return x }\n",
	})
	if m["verdict"] == "complete" {
		t.Fatalf("Hook's type no longer matches core.Do, but verify said complete: %v", m)
	}
}

func TestVerifyGoVariadicIgnoresUntypedBinding(t *testing.T) {
	m := variadicVerify(t, map[string]string{
		"core/core.go": "package core\n\nfunc Do(x string) string { return x }\n\nvar local = Do\n\nfunc Self() string { f := Do; return f(\"b\") }\n",
	}, map[string]string{
		"core/core.go": "package core\n\nfunc Do(x string, n ...int) string { return x }\n\nvar local = Do\n\nfunc Self() string { f := Do; return f(\"b\") }\n",
	})
	if m["verdict"] != "complete" || len(anySlice(m["missedSites"])) != 0 {
		t.Fatalf("untyped bindings still compile, but verify flagged them: %v", m)
	}
}
