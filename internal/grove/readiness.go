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

// Readiness reads grove's native-analyzer diagnostics for one index run and
// names each language that fell back to name-based resolution, with the
// command that fixes it. Benign skips (nothing changed, previous compiler
// results carried forward) are not issues.
func Readiness(root string, native []string) []ReadinessIssue {
	var out []ReadinessIssue
	seen := map[string]bool{}
	add := func(lang, problem, fix string) {
		if seen[lang] {
			return
		}
		seen[lang] = true
		out = append(out, ReadinessIssue{Language: lang, Problem: problem, Fix: fix})
	}
	for _, d := range native {
		switch {
		case strings.Contains(d, "no changed files in its languages"), strings.Contains(d, "disabled by config"):
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
		case strings.HasPrefix(d, "go: "):
			reason := strings.TrimPrefix(d, "go: ")
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
