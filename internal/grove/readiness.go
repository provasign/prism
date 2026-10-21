package grove

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ReadinessIssue is a language whose compiler-backed (native) analysis did
// not run or did not complete. Without it prism's call and reference
// resolution for that language is name-based: still useful, measurably less
// accurate (TypeScript field renames: 1 of 44 sites confirmed name-based,
// 44 of 44 with the compiler -- hono ConnInfo.remote, 2026-09-27).
type ReadinessIssue struct {
	Language string `json:"language"`
	Problem  string `json:"problem"`
	Fix      string `json:"fix"`
}

var goMissingModule = regexp.MustCompile(`could not import ([A-Za-z0-9._-]+\.[A-Za-z0-9._/-]+) \(`)

// goImportFailure captures the import path and go/types' reason from a
// "could not import <path> (<reason>)" diagnostic.
var goImportFailure = regexp.MustCompile(`could not import (\S+) \((.*)\)\s*$`)

// Readiness reads grove's native-analyzer diagnostics for one index run and
// names each language that fell back to name-based resolution, with the
// command that fixes it. Benign skips (nothing changed, previous compiler
// results carried forward) are not issues.
func Readiness(root string, native []string) []ReadinessIssue {
	var out []ReadinessIssue
	seen := map[string]bool{}
	addKey := func(key, lang, problem, fix string) {
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ReadinessIssue{Language: lang, Problem: problem, Fix: fix})
	}
	add := func(lang, problem, fix string) { addKey(lang, lang, problem, fix) }
	var goModules []string
	goModulesRead := false
	for _, d := range native {
		switch {
		case strings.Contains(d, "no changed files in its languages"), strings.Contains(d, "disabled by config"),
			// The analyzer does not apply (a plain JavaScript project that never
			// used typescript): tree-sitter is the whole analysis, nothing to fix.
			strings.Contains(d, "skipped: not applicable:"):
			continue
		case strings.HasPrefix(d, "js-ts: "):
			reason := strings.TrimPrefix(d, "js-ts: ")
			switch {
			case strings.Contains(reason, "typescript not resolvable"):
				add("typescript", "the project's own TypeScript compiler is not installed (dependencies missing)",
					"run `"+jsInstallCommand(root)+"` in "+root+", then `prism index`")
			case strings.Contains(reason, "node executable not found"):
				add("typescript", "Node.js is not on PATH, so the TypeScript compiler cannot run",
					"install Node.js and the project's dependencies (`"+jsInstallCommand(root)+"`), then `prism index`")
			case strings.Contains(reason, "timed out"), strings.Contains(reason, "bootstrap failed"),
				strings.Contains(reason, "no edges produced"), strings.Contains(reason, "decode failed"):
				add("typescript", "TypeScript compiler analysis did not complete: "+reason,
					"check that `npx tsc --noEmit -p .` runs in "+root+", then `prism index`")
			}
		case strings.HasPrefix(d, "java: "):
			reason := strings.TrimPrefix(d, "java: ")
			switch {
			case strings.Contains(reason, "no JDK"):
				add("java", "no JDK 11+ found (JAVA_HOME, PATH, macOS java_home, Homebrew openjdk), so javac cannot resolve calls",
					"install the JDK the project builds with (see its pom.xml/build.gradle) or set JAVA_HOME to it, then `prism index`")
			case strings.Contains(reason, "Maven classpath not resolvable"):
				add("java", "the project's Maven dependencies are not in the local repository, so types from libraries are unresolved",
					"build the project once (`mvn -q -DskipTests compile` in "+root+"), then `prism index`")
			case strings.Contains(reason, "Gradle classpath not resolvable"):
				add("java", "the project's Gradle dependencies could not be resolved, so types from libraries are unresolved",
					"build the project once (`./gradlew classes` in "+root+"), then `prism index`")
			case strings.Contains(reason, "mvn not found"):
				add("java", "Maven is not on PATH, so the project's library classpath is unavailable",
					"install Maven, build the project once (`mvn -q -DskipTests compile`), then `prism index`")
			case strings.Contains(reason, "no Gradle wrapper or gradle executable"):
				add("java", "neither a Gradle wrapper nor gradle is available, so the project's library classpath is unavailable",
					"install Gradle, build the project once, then `prism index`")
			case strings.Contains(reason, "javac resolver failed"), strings.Contains(reason, "javac resolver JSON decode failed"):
				add("java", "javac resolution did not complete: "+reason,
					"check that the project compiles (`mvn -q -o compile` or `gradle compileJava`), then `prism index`")
			}
		case strings.HasPrefix(d, "rust: rust-analyzer "):
			// The pass runs only where a macro invoked at item level can
			// generate items; without it those items and every call inside
			// them are missing (ripgrep: 663 rgtest! tests).
			reason := strings.TrimPrefix(d, "rust: ")
			switch {
			case strings.Contains(reason, "rust-analyzer not found"):
				add("rust", "rust-analyzer is not installed, so items generated by macros (and the calls inside them) are missing",
					"run `rustup component add rust-analyzer`, then `prism index`")
			case strings.Contains(reason, "scip failed"), strings.Contains(reason, "scip decode failed"):
				add("rust", "rust-analyzer analysis did not complete: "+reason,
					"check that `cargo check` succeeds in "+root+", then `prism index`")
			}
		case strings.HasPrefix(d, "go: "):
			reason := strings.TrimPrefix(d, "go: ")
			if !goModulesRead {
				goModules, goModulesRead = goMainModules(root), true
			}
			// A failed import inside the project's own module(s) is not a
			// missing download: the package is local and `go mod download`
			// cannot change it. go/types reports `invalid package name: ""`
			// for a package it found but could not load any Go file of (a
			// cgo-only local package, astkit thirdparty/tsobjc), which is the
			// same case even when the module path is unknown.
			if m := goImportFailure.FindStringSubmatch(reason); m != nil &&
				(goPathInModules(m[1], goModules) || strings.Contains(m[2], `invalid package name: ""`)) {
				addKey("go-local", "go", "local package "+m[1]+" could not be type-checked ("+m[2]+"); packages importing it are type-checked partially",
					"no download is needed: the package is part of this project. If `go build ./...` succeeds in "+root+", this is a Go-pass limit (for example a cgo-only package) and those results are partly name-based")
				continue
			}
			if m := goMissingModule.FindStringSubmatch(reason); m != nil && !strings.HasPrefix(m[1], "golang.org/toolchain") {
				add("go", "Go module dependencies are not downloaded (e.g. "+m[1]+"); packages importing them are type-checked partially",
					"run `go mod download` in "+root+", then `prism index`")
			} else if strings.Contains(reason, "go executable not found") || strings.Contains(reason, "skipped: ") && strings.Contains(reason, "not found") {
				add("go", "the Go toolchain is not on PATH, so Go type-checking cannot run",
					"install Go (matching go.mod), then `prism index`")
			} else if strings.Contains(reason, "timed out") {
				add("go", "Go type-checking did not complete: "+reason, "rerun `prism index`; report the timeout if it persists")
			}
		}
	}
	return out
}

// jsInstallCommand picks the project's package manager from its lockfile.
func jsInstallCommand(root string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case has("pnpm-lock.yaml"):
		return "pnpm install"
	case has("bun.lockb"), has("bun.lock"):
		return "bun install"
	case has("yarn.lock"):
		return "yarn install"
	case has("package-lock.json"):
		return "npm ci"
	default:
		return "npm install"
	}
}

// goMainModules returns the module paths of the project's main module(s):
// root/go.mod and, with a go.work, each used module's go.mod.
func goMainModules(root string) []string {
	var mods []string
	if m := goModulePath(filepath.Join(root, "go.mod")); m != "" {
		mods = append(mods, m)
	}
	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return mods
	}
	inUse := false
	for _, line := range strings.Split(string(work), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		var dir string
		switch {
		case inUse && line == ")":
			inUse = false
		case inUse:
			dir = line
		case line == "use (":
			inUse = true
		case strings.HasPrefix(line, "use "):
			dir = strings.TrimSpace(strings.TrimPrefix(line, "use "))
		}
		if dir = strings.Trim(dir, `"`); dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		if m := goModulePath(filepath.Join(dir, "go.mod")); m != "" {
			mods = append(mods, m)
		}
	}
	return mods
}

// goModulePath reads the module directive of a go.mod file.
func goModulePath(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

// goPathInModules reports whether an import path belongs to one of mods.
func goPathInModules(path string, mods []string) bool {
	for _, m := range mods {
		if path == m || strings.HasPrefix(path, m+"/") {
			return true
		}
	}
	return false
}
