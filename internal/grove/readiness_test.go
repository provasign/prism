package grove

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessNamesDegradedLanguagesWithFixes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := Readiness(root, []string{
		"js-ts: skipped: typescript not resolvable in the project (previous native edges carried forward)",
		"java: skipped: no JDK 11+ found and no Maven or Gradle project config",
		"go: project import gin/binding type-checked partially: could not import go.mongodb.org/mongo-driver/bson (no required module)",
		"python: skipped: no changed files in its languages (previous edges carried forward)",
		"rust: skipped: disabled by config",
	})
	byLang := map[string]ReadinessIssue{}
	for _, r := range got {
		byLang[r.Language] = r
	}
	if len(got) != 3 {
		t.Fatalf("want typescript, java, go issues only; got %+v", got)
	}
	if fix := byLang["typescript"].Fix; !strings.Contains(fix, "pnpm install") {
		t.Errorf("typescript fix should use the lockfile's package manager: %q", fix)
	}
	if fix := byLang["java"].Fix; !strings.Contains(fix, "JAVA_HOME") {
		t.Errorf("java fix: %q", fix)
	}
	if fix := byLang["go"].Fix; !strings.Contains(fix, "go mod download") {
		t.Errorf("go fix: %q", fix)
	}
}

func TestReadinessIgnoresCompletedAndCarriedPasses(t *testing.T) {
	if got := Readiness(t.TempDir(), []string{
		"java: javac attributed 12 file(s) (0 compile error(s) tolerated)",
		"go: resolved 10 native call edge(s)",
		"go: project import golang.org/toolchain/x type-checked partially: could not import golang.org/toolchain/x (x)",
	}); len(got) != 0 {
		t.Fatalf("healthy run reported issues: %+v", got)
	}
}

func TestReadinessJavaClasspath(t *testing.T) {
	root := t.TempDir()
	cases := map[string]string{
		"java: Maven classpath not resolvable; external types unresolved":                                                 "mvn -q -DskipTests compile",
		"java: Gradle classpath not resolvable; external types unresolved":                                                "./gradlew classes",
		"java: mvn not found, so the Maven classpath is unavailable; external types unresolved":                           "install Maven",
		"java: no Gradle wrapper or gradle executable, so the Gradle classpath is unavailable; external types unresolved": "install Gradle",
	}
	for diag, fix := range cases {
		got := Readiness(root, []string{diag})
		if len(got) != 1 || got[0].Language != "java" || !strings.Contains(got[0].Fix, fix) {
			t.Errorf("%q: got %+v, want java fix containing %q", diag, got, fix)
		}
	}
	// Partial multi-module resolution and plain-javac projects are not
	// something the user can fix by building once.
	for _, diag := range []string{
		"java: Maven classpath resolved for some modules only; types from the rest are unresolved",
		"java: no Maven or Gradle build found; external types unresolved",
		"java: build-tool classpath skipped in untrusted mode; external types unresolved",
	} {
		if got := Readiness(root, []string{diag}); len(got) != 0 {
			t.Errorf("%q reported %+v", diag, got)
		}
	}
}

func TestJSInstallCommandFollowsLockfile(t *testing.T) {
	for lock, want := range map[string]string{
		"pnpm-lock.yaml": "pnpm install", "bun.lock": "bun install", "yarn.lock": "yarn install",
		"package-lock.json": "npm ci", "": "npm install",
	} {
		root := t.TempDir()
		if lock != "" {
			if err := os.WriteFile(filepath.Join(root, lock), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := jsInstallCommand(root); got != want {
			t.Errorf("%q: got %q, want %q", lock, got, want)
		}
	}
}

// A failed import of the project's own package is not a missing download
// (stack audit 2026-10-20, finding 4): classify by where the import path
// lives, and by go/types' `invalid package name: ""` (a package found but with
// no loadable Go file, such as a cgo-only local package).
func TestReadinessGoImportFailureCause(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := t.TempDir()
	write(t, mod, "go.mod", "module example.com/app\n\ngo 1.26\n")
	work := t.TempDir()
	write(t, work, "go.work", "go 1.26\n\nuse (\n\t./svc // service\n\t\"./lib\"\n)\n")
	write(t, work, "svc/go.mod", "module example.com/svc\n")
	write(t, work, "lib/go.mod", "module \"example.com/lib\"\n")

	local := func(t *testing.T, root, diag string) {
		t.Helper()
		got := Readiness(root, []string{diag})
		if len(got) != 1 || got[0].Language != "go" {
			t.Fatalf("want one go issue, got %+v", got)
		}
		if strings.Contains(got[0].Problem, "not downloaded") || strings.Contains(got[0].Fix, "go mod download") {
			t.Fatalf("local package failure prescribes a download: %+v", got[0])
		}
		if !strings.Contains(got[0].Problem, "local package") || !strings.Contains(got[0].Fix, "no download is needed") {
			t.Fatalf("local package failure not named as local: %+v", got[0])
		}
	}
	t.Run("cgo-only local package without go.mod", func(t *testing.T) {
		local(t, t.TempDir(), `go: project import github.com/provasign/astkit type-checked partially: could not import github.com/provasign/astkit/thirdparty/tsobjc (invalid package name: "")`)
	})
	t.Run("package of the main module", func(t *testing.T) {
		local(t, mod, "go: project import example.com/app/api type-checked partially: a.go:3:2: could not import example.com/app/internal/x (some type error)")
	})
	t.Run("package of a go.work module", func(t *testing.T) {
		local(t, work, "go: project import example.com/svc/api type-checked partially: could not import example.com/lib/util (x)")
	})
	t.Run("dependency outside the main modules still prescribes download", func(t *testing.T) {
		got := Readiness(mod, []string{"go: project import example.com/app/api type-checked partially: could not import example.com/application/dep (no required module provides package)"})
		if len(got) != 1 || !strings.Contains(got[0].Fix, "go mod download") {
			t.Fatalf("missing dependency: %+v", got)
		}
	})
	t.Run("local and dependency failures are both reported", func(t *testing.T) {
		got := Readiness(mod, []string{
			"go: project import example.com/app/a type-checked partially: could not import example.com/app/b (x)",
			"go: project import example.com/app/c type-checked partially: could not import github.com/dep/z (no required module)",
		})
		if len(got) != 2 {
			t.Fatalf("want local + download issues, got %+v", got)
		}
	})
}

// A plain JavaScript project never used typescript: nothing is missing and
// prism index must not exit 3 or tell the user to install dependencies.
func TestReadinessIgnoresNotApplicableTypeScript(t *testing.T) {
	if got := Readiness(t.TempDir(), []string{
		"js-ts: skipped: not applicable: the project does not use the TypeScript compiler (no typescript dependency, no tsconfig.json or jsconfig.json); tree-sitter analysis only (previous native edges carried forward)",
	}); len(got) != 0 {
		t.Fatalf("plain JavaScript reported as degraded: %+v", got)
	}
	if got := Readiness(t.TempDir(), []string{
		"js-ts: skipped: typescript not resolvable in the project (previous native edges carried forward)",
	}); len(got) != 1 {
		t.Fatalf("declared-but-uninstalled typescript must still be reported, got %+v", got)
	}
}

func TestReadinessRustAnalyzer(t *testing.T) {
	got := Readiness(t.TempDir(), []string{"rust: rust-analyzer pass skipped: rust-analyzer not found (PATH, ~/.cargo/bin, editor extensions)"})
	if len(got) != 1 || got[0].Language != "rust" || !strings.Contains(got[0].Fix, "rustup component add rust-analyzer") {
		t.Fatalf("missing rust-analyzer: %+v", got)
	}
	got = Readiness(t.TempDir(), []string{"rust: rust-analyzer scip failed: exit status 1: error: failed to load workspace"})
	if len(got) != 1 || !strings.Contains(got[0].Fix, "cargo check") {
		t.Fatalf("failed rust-analyzer: %+v", got)
	}
	for _, d := range []string{
		"rust: rust-analyzer pass skipped: disabled by GROVE_RUST_ANALYZER",
		"rust: rust-analyzer declared 734 symbol(s) the parser did not index (macro-generated items)",
		"rust: rust-analyzer edges reused from the last run (no edited file needs it): 1892",
	} {
		if got := Readiness(t.TempDir(), []string{d}); len(got) != 0 {
			t.Errorf("%q is not an issue: %+v", d, got)
		}
	}
}
