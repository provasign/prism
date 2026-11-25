package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/grove/pkg/grove"
)

// takeBoolFlag removes every --name from args and reports whether it was there.
func takeBoolFlag(args []string, name string) ([]string, bool) {
	out := args[:0:0]
	found := false
	for _, a := range args {
		if a == "--"+name {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// integrityResult runs the index integrity check for doctor --integrity. It
// returns the report for output and the warnings it implies.
func integrityResult(root string) (map[string]any, []string) {
	r, err := grove.CheckIntegrity(context.Background(), root)
	if err != nil {
		return map[string]any{"status": "error", "error": err.Error()},
			[]string{"index integrity check failed: " + err.Error() + ". Fix: prism index --rebuild"}
	}
	problems := r.Problems
	if problems == nil {
		problems = []string{}
	}
	out := map[string]any{
		"status":           "ok",
		"problems":         problems,
		"extractorVersion": r.ExtractorVersion,
		"resolverVersion":  r.ResolverVersion,
	}
	var warnings []string
	if len(r.Problems) > 0 {
		out["status"] = "corrupt"
		shown := r.Problems
		if len(shown) > 3 {
			shown = shown[:3]
		}
		warnings = append(warnings, fmt.Sprintf("index is corrupt (%d problems: %s). Fix: prism index --rebuild",
			len(r.Problems), strings.Join(shown, "; ")))
	}
	if r.Stale() {
		if out["status"] == "ok" {
			out["status"] = "stale"
		}
		warnings = append(warnings, fmt.Sprintf("index was built by another engine version (extractor %s, resolver %s; this build writes %s, %s); the next prism index rebuilds it",
			orNone(r.ExtractorVersion), orNone(r.ResolverVersion), r.WantExtractorVersion, r.WantResolverVersion))
	}
	return out, warnings
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// largeUntrackedLimit is the size above which an untracked, unignored
// directory is reported: text search and the index both read it.
var largeUntrackedLimit int64 = 50 << 20

// largeUntrackedWarnings lists untracked directories whose files git does
// not ignore add up to more than largeUntrackedLimit bytes. Only those files
// count: they are what text search and the index read. Outside a git
// repository it reports nothing.
func largeUntrackedWarnings(root string) []string {
	git := func(extra ...string) []string {
		out, err := exec.Command("git", append([]string{"-C", root, "ls-files", "--others", "--exclude-standard", "-z"}, extra...)...).Output()
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	}
	sizes := map[string]int64{}
	for _, rel := range git("--directory") {
		if strings.HasSuffix(rel, "/") {
			sizes[rel] = 0
		}
	}
	if len(sizes) == 0 {
		return nil
	}
	for _, rel := range git() {
		for dir := filepath.ToSlash(filepath.Dir(rel)) + "/"; dir != "./" && dir != "/"; dir = filepath.ToSlash(filepath.Dir(strings.TrimSuffix(dir, "/"))) + "/" {
			if _, ok := sizes[dir]; ok {
				if info, err := os.Lstat(filepath.Join(root, rel)); err == nil && info.Mode().IsRegular() {
					sizes[dir] += info.Size()
				}
				break
			}
		}
	}
	var dirs []string
	for dir, size := range sizes {
		if size > largeUntrackedLimit {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	var warnings []string
	for _, dir := range dirs {
		warnings = append(warnings, fmt.Sprintf("%s holds %d MB of untracked files that .gitignore does not exclude: search and the index read them. Fix: ignore or move them",
			strings.TrimSuffix(dir, "/"), sizes[dir]>>20))
	}
	return warnings
}
