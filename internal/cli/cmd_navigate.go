package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

// cmdNode is the one-shot orientation view — a symbol's source + neighbours,
// or a file's source + defined symbols + dependents.
func cmdNode(args []string) int {
	args, rc := positionalsFirst("node", "a symbol or file", "prism node <symbol-or-file> [dir] [--file <path>] [--format text|json]", 1, args, "--file", "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	fileHint := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--file":
			v, err := flagValue("node", args, i)
			if err != nil {
				return usageError(err)
			}
			fileHint = v
			i++
		case "--format":
			f, err := formatFlag("node", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("node", a)
			}
			if err := positionalDir("node", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.NodeRequest{Name: name, File: fileHint})
	if err != nil {
		fmt.Fprintln(os.Stderr, "node:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("node", out)
}

func cmdResolve(args []string) int {
	args, rc := positionalsFirst("resolve", "a name", "prism resolve <name> [dir]", 1, args, "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--format" {
			f, err := formatFlag("resolve", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		} else if strings.HasPrefix(a, "-") {
			return rejectUnknownFlag("resolve", a)
		} else if err := positionalDir("resolve", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, err := invokeTool(dir, core.ResolveRequest{Name: name})
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("resolve", out)
}

func cmdEdges(args []string) int {
	args, rc := positionalsFirst("edges", "a symbol", "prism edges <name> [--direction out|in|both] [--kinds calls,uses-type,implements,...] [dir]", 1, args, "--dir", "--direction", "--kinds", "--format")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	direction := "both"
	var kinds []string
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir", "--direction":
			d, err := choiceFlag("edges", args, i, "out", "in", "both")
			if err != nil {
				return usageError(err)
			}
			direction = d
			i++
		case "--kinds":
			items, err := listFlag("edges", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, item := range items {
				kinds = append(kinds, item)
			}
			i++
		case "--format":
			f, err := formatFlag("edges", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("edges", a)
			}
			if err := positionalDir("edges", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.EdgesRequest{Name: name, Direction: direction, Kinds: kinds})
	if err != nil {
		fmt.Fprintln(os.Stderr, "edges:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("edges", out)
}

// cmdCallers prints the transitive callers of one symbol (GAP-03).
func cmdCallers(args []string) int {
	args, rc := positionalsFirst("callers", "a symbol", "prism callers <symbol> [dir] [--depth N]", 1, args, "--format", "--file", "--depth")
	if rc != 0 {
		return rc
	}
	callersReq := core.CallersRequest{Name: args[0]}
	dir, dirSet := ".", false
	format := formatText
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--depth":
			n, err := positiveIntFlag("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			callersReq.Depth = n
			i++
		case "--file":
			v, err := flagValue("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			callersReq.File = v
			i++
		case "--format":
			f, err := formatFlag("callers", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("callers", a)
			}
			if err := positionalDir("callers", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, callersReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "callers:", err)
		return exitForError(err)
	}
	printToolResult("callers", out, text, rendered, format)
	return exitOK
}

// cmdPath prints the shortest resolved call path from one symbol to
// another (GAP-03).
func cmdPath(args []string) int {
	args, rc := positionalsFirst("path", "two symbols", "prism path <from> <to> [dir]", 2, args, "--format")
	if rc != 0 {
		return rc
	}
	pathReq := core.CallPathRequest{From: args[0], To: args[1]}
	dir, dirSet := ".", false
	format := formatText
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			f, err := formatFlag("path", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("path", a)
			}
			if err := positionalDir("path", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, pathReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "path:", err)
		return exitForError(err)
	}
	printToolResult("path", out, text, rendered, format)
	if m, ok := out.(map[string]any); ok {
		if p, _ := m["path"].([]map[string]any); len(p) == 0 {
			return exitNotFound
		}
	}
	return exitOK
}

func cmdReferences(args []string) int {
	args, rc := positionalsFirst("references", "a name", "prism references <name> [dir]", 1, args, "--format", "--file", "--path", "--kind")
	if rc != 0 {
		return rc
	}
	name := args[0]
	dir, dirSet := ".", false
	format := formatText
	file, kind := "", ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--kind":
			v, err := flagValue("references", args, i)
			if err != nil {
				return usageError(err)
			}
			for _, k := range strings.Split(v, ",") {
				switch strings.TrimSpace(k) {
				case "call", "read", "write", "init", "decl", "type", "type-use":
				default:
					return usageError(fmt.Errorf("references: --kind %q: use call, read, write, decl or type", k))
				}
			}
			kind = v
			i++
		case "--format":
			f, err := formatFlag("references", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			v, err := flagValue("references", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("references", a)
			}
			if err := positionalDir("references", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.ReferencesRequest{Name: name, File: file, Kind: kind})
	if err != nil {
		fmt.Fprintln(os.Stderr, "references:", err)
		return exitForError(err)
	}
	printOutput(out, format)
	return exitForAnswer("references", out)
}
