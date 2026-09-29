package textsearch

import (
	"path"
	"path/filepath"
	"strings"
)

// MatchGlob reports whether a repository-relative file path matches glob
// with ripgrep --glob semantics, so every search surface (rg, the native
// scanner, and indexed-symbol filtering) agrees on what a glob selects:
//
//   - a glob with no "/" matches the file's base name at any depth
//     ("*.py", "types.py");
//   - a glob with a "/" matches the whole relative path, segment by segment,
//     where a "**" segment crosses any number of directories, including zero
//     ("**/types.py", "src/**/*.java", "tests/**").
//
// Before this matcher, the symbol filter used path.Match on the base name and
// the full path only, so "**/types.py" matched no file at all in symbol scope
// while text scope (rg) matched every types.py.
func MatchGlob(glob, rel string) bool {
	glob = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(glob)), "./")
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if glob == "" {
		return false
	}
	if !strings.Contains(glob, "/") {
		ok, _ := path.Match(glob, path.Base(rel))
		return ok
	}
	glob = strings.TrimPrefix(glob, "/")
	if strings.HasSuffix(glob, "/") {
		// "dir/" selects everything below dir.
		glob += "**"
	}
	return matchGlobSegments(strings.Split(glob, "/"), strings.Split(rel, "/"))
}

func matchGlobSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchGlobSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// MatchAnyGlob reports whether rel matches at least one glob.
func MatchAnyGlob(globs []string, rel string) bool {
	for _, g := range globs {
		if MatchGlob(g, rel) {
			return true
		}
	}
	return false
}
