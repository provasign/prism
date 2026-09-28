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
