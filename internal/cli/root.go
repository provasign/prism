package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// cliRoot picks the project root for an information command. An explicit
// directory is used as given. With none (dir "" or "."), the CLI walks up from
// the working directory: an agent that ran `cd middleware` and then
// `prism read content_charset_test.go` got a second index built in
// middleware/.grove, an answer scoped to that one directory, and a binary
// grove.db in its diff that failed the task (chi pr1158, 2026-10-18). MCP never
// hit this because its server starts at the repository root.
//
// Markers: the nearest prism.yaml (written by init), then the git work tree
// root, then an existing .grove. A stray .grove in a subdirectory therefore
// cannot capture the root. One exception: a prism.yaml in a plain folder
// above the git work tree (a parent folder holding several checkouts) does
// not capture the repositories below it. It used to, which answered a
// question asked inside one checkout from a single index over all of them;
// that file now only declares the workspace (workspace.Load reads it). sub
// is the working directory relative to the root ("" when they are the same).
func cliRoot(dir string) (root, sub string) {
	abs := mustAbs(dir)
	if dir != "" && dir != "." {
		return abs, ""
	}
	root = abs
	cfgDir, hasCfg := nearestAncestorWith(abs, "prism.yaml")
	gitDir, hasGit := nearestAncestorWith(abs, ".git")
	switch {
	case hasCfg && hasGit && cfgDir != gitDir && isWithin(cfgDir, gitDir) && !inGitWorkTree(cfgDir):
		root = gitDir
	case hasCfg:
		root = cfgDir
	case hasGit:
		root = gitDir
	default:
		if found, ok := nearestAncestorWith(abs, ".grove"); ok {
			root = found
		}
	}
	if rel, err := filepath.Rel(root, abs); err == nil && rel != "." {
		sub = filepath.ToSlash(rel)
	}
	return root, sub
}

// isWithin reports whether child is parent or a directory below it.
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func inGitWorkTree(dir string) bool {
	_, ok := nearestAncestorWith(dir, ".git")
	return ok
}

func nearestAncestorWith(start, marker string) (string, bool) {
	for d := start; ; {
		if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// rebaseArgs rewrites relative path arguments so they name files relative
// to the root. Output paths are root-relative, so a path copied from an
// answer and passed back from a subdirectory used to be joined onto the
// subdirectory and fail (BUG-20). A path is read relative to the working
// directory when it exists there, else relative to the root when it exists
// there, else relative to the working directory. When both exist and
// differ the working directory wins and stderr says so. Globs are
// patterns and always scope to the working directory.
func rebaseArgs(args map[string]any, root, sub string) {
	if sub == "" {
		return
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
	join := func(v string) string {
		if v == "" || filepath.IsAbs(v) {
			return v
		}
		cwdRel := path.Join(sub, filepath.ToSlash(v))
		rootRel := path.Clean(filepath.ToSlash(v))
		if rootRel == ".." || strings.HasPrefix(rootRel, "../") || rootRel == cwdRel || !exists(rootRel) {
			return cwdRel
		}
		if exists(cwdRel) {
			fmt.Fprintf(os.Stderr, "prism: %q read as %s (relative to the current directory); %s also exists\n", v, cwdRel, rootRel)
			return cwdRel
		}
		return rootRel
	}
	globJoin := func(v string) string {
		if v == "" || filepath.IsAbs(v) {
			return v
		}
		return path.Join(sub, filepath.ToSlash(v))
	}
	for _, key := range []string{"file", "symbol_file", "path", "paths", "glob"} {
		j := join
		if key == "glob" {
			j = globJoin
		}
		switch v := args[key].(type) {
		case string:
			args[key] = j(v)
		case []string:
			for i := range v {
				v[i] = j(v[i])
			}
		case []any:
			for i := range v {
				if s, ok := v[i].(string); ok {
					v[i] = j(s)
				}
			}
		}
	}
	if ranges, ok := args["ranges"].([]any); ok {
		for _, r := range ranges {
			if m, ok := r.(map[string]any); ok {
				if f, ok := m["file"].(string); ok {
					m["file"] = join(f)
				}
			}
		}
	}
}

// noteRoot tells the agent which root answered, because paths in the answer
// are relative to it, not to the directory it ran the command from.
func noteRoot(root, sub string) {
	if sub != "" {
		fmt.Fprintf(os.Stderr, "prism: answering from the project root %s (you are in %s/); paths below are relative to the root\n", root, sub)
	}
}
