package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdLookup(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name>... [--dir <path>]  (two arguments use the second as a directory if it exists)")
		return 2
	}
	explicitDir := ""
	format := formatText
	repo := ""
	fileHint := ""
	var fields []string
	var bare []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintln(os.Stderr, "lookup: --dir requires a path")
				return 2
			}
			explicitDir = args[i+1]
			i++
		case "--fields":
			items, err := listFlag("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			fields = append(fields, items...)
			i++
		case "--repo":
			v, err := flagValue("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			repo = v
			i++
		case "--file", "--path":
			v, err := flagValue("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			fileHint = v
			i++
		case "--format":
			f, err := formatFlag("lookup", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("lookup", a)
			}
			bare = append(bare, a)
		}
	}
	if len(bare) == 0 {
		fmt.Fprintln(os.Stderr, "usage: prism lookup <name>... [--dir <path>]  (two arguments use the second as a directory if it exists)")
		return 2
	}
	dir := "."
	if explicitDir != "" {
		dir = explicitDir
	} else if len(bare) == 2 {
		// Preserve the positional directory form, while allowing the natural
		// two-name batch when the second argument is not a directory. A
		// path-shaped argument is always the directory, so a mistyped path
		// still fails instead of becoming a lookup in the wrong tree; only a
		// bare word that is also a directory is ambiguous enough to note.
		if lookupArgIsPath(bare[1]) {
			dir = bare[1]
			bare = bare[:1]
		} else if info, err := os.Stat(bare[1]); err == nil && info.IsDir() {
			fmt.Fprintf(os.Stderr, "lookup: reading %q as the directory (legacy form); use --dir . to look up both names\n", bare[1])
			dir = bare[1]
			bare = bare[:1]
		}
	}
	if len(bare) > 10 {
		fmt.Fprintln(os.Stderr, "lookup: at most 10 names per call")
		return 2
	}
	// Text output goes through the batch form even for one name: that is the
	// shape (and the "// lookup <name>" header) an agent gets over MCP.
	lookupReq := core.LookupRequest{Names: bare, AsList: format == formatText}
	for _, n := range bare {
		warnShellExpandedName("lookup", n)
	}
	if repo == "" && fileHint != "" {
		selected, err := repoForFileHint(dir, fileHint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lookup:", err)
			return 1
		}
		repo = selected
	}
	lookupReq.Fields, lookupReq.Repo, lookupReq.File = fields, repo, fileHint
	var out any
	var text string
	var rendered bool
	var err error
	if repo != "" && repo != "all" && fileHint != "" {
		lookupReq.Repo = ""
		out, text, rendered, err = invokeRepoToolText(dir, repo, lookupReq)
	} else {
		out, text, rendered, err = invokeToolText(dir, lookupReq)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		return exitForError(err)
	}
	// A batch answer is {results:[{name,result}]}, which the generic text
	// printer does not know; render it the way MCP does.
	printToolResult("lookup", out, text, rendered, format)
	return exitForAnswer("lookup", out)
}

// lookupArgIsPath reports whether a positional lookup argument can only be a
// directory: no symbol name is ".", "..", or starts with "/", "./", "../" or "~".
func lookupArgIsPath(a string) bool {
	if a == "." || a == ".." || filepath.IsAbs(a) {
		return true
	}
	for _, p := range []string{"./", "../", "~", ".\\", "..\\"} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}
