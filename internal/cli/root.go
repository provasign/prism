package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// cliRoot picks the project root for an information command. An explicit
// directory is used as given. With none (dir "" or "."), the CLI walks up from
// the working directory: an agent that ran `cd middleware` and then
// `prism read content_charset_test.go` got a second index built in
// middleware/.grove, an answer scoped to that one directory, and a binary
// grove.db in its diff that failed the task (chi pr1158, 2026-10-18). MCP never
// hit this because its server starts at the repository root.
//
// Markers, nearest first within each kind: prism.yaml (written by init), then
// the git work tree root, then an existing .grove. A stray .grove in a
// subdirectory therefore cannot capture the root. sub is the working
// directory relative to the root ("" when they are the same).
func cliRoot(dir string) (root, sub string) {
	abs := mustAbs(dir)
	if dir != "" && dir != "." {
		return abs, ""
	}
	root = abs
	for _, marker := range []string{"prism.yaml", ".git", ".grove"} {
		if found, ok := nearestAncestorWith(abs, marker); ok {
			root = found
			break
		}
	}
	if rel, err := filepath.Rel(root, abs); err == nil && rel != "." {
		sub = filepath.ToSlash(rel)
	}
	return root, sub
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

// rebaseArgs rewrites relative path arguments, written relative to the
// working directory, so they name the same files relative to the root.
func rebaseArgs(args map[string]any, sub string) {
	if sub == "" {
		return
	}
	join := func(v string) string {
		if v == "" || filepath.IsAbs(v) {
			return v
		}
		return path.Join(sub, filepath.ToSlash(v))
	}
	for _, key := range []string{"file", "symbol_file", "path", "paths", "glob"} {
		switch v := args[key].(type) {
		case string:
			args[key] = join(v)
		case []string:
			for i := range v {
				v[i] = join(v[i])
			}
		case []any:
			for i := range v {
				if s, ok := v[i].(string); ok {
					v[i] = join(s)
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
