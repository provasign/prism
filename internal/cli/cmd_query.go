package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdQuery(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism query --terms a,b,c [dir]  (--terms is REQUIRED; use prism search first when no anchor is known)")
		return 2
	}
	dir := "."
	dirSet := false
	profile := ""
	limit := 50
	maxFiles := 0
	delivery := ""
	format := formatText
	repo := ""
	var terms []string
	var include []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--repo":
			repo, err = flagValue("query", args, i)
			i++
		case "--profile":
			profile, err = flagValue("query", args, i)
			i++
		case "--limit":
			limit, err = positiveIntFlag("query", args, i)
			i++
		case "--terms":
			var more []string
			more, err = listFlag("query", args, i)
			terms = append(terms, more...)
			i++
		case "--include":
			var more []string
			more, err = listFlag("query", args, i)
			include = append(include, more...)
			i++
		case "--depth", "--graph-depth":
			// graph_depth has never been read by any handler; sending it was
			// a silent no-op. Say so instead of pretending it tunes anything.
			fmt.Fprintln(os.Stderr, "query: --depth/--graph-depth has no effect and was removed; expansion is a fixed one-hop call neighborhood")
			return 2
		case "--delivery":
			delivery, err = choiceFlag("query", args, i, "source", "symbols")
			i++
		case "--max-files":
			maxFiles, err = positiveIntFlag("query", args, i)
			i++
		case "--format":
			format, err = formatFlag("query", args, i)
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("query", a)
			}
			if dirSet {
				return usageError(fmt.Errorf("query: takes one directory, got %q and %q; pass terms with --terms a,b", dir, a))
			}
			dir, dirSet = a, true
		}
		if err != nil {
			return usageError(err)
		}
	}
	if len(terms) == 0 {
		fmt.Fprintln(os.Stderr, "query: --terms is required; use prism search first when no anchor is known")
		return 2
	}
	if len(terms) > maxBatchTerms {
		fmt.Fprintf(os.Stderr, "query: at most %d terms per call (the MCP gateway rejects more); ask about the terms the current question needs\n", maxBatchTerms)
		return 2
	}
	req := core.QueryRequest{Terms: terms, Limit: limit, Delivery: delivery, MaxFiles: maxFiles, Profile: profile, Include: include}
	out, text, rendered, err := invokeRepoToolText(dir, repo, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		return 1
	}
	printToolResult("query", out, text, rendered, format)
	return 0
}
