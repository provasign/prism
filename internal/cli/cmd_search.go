package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdSearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: prism search <term> [term...] [--dir <path>]")
		return 2
	}
	limit, limitSet := 25, false
	dir := ""
	format := formatText
	scope := ""
	regex := false
	caseMode, word, noDefaultExcludes, includeSensitive := "", false, false, false
	var paths, globs []string
	filesOnly := false
	includeBodies := true
	includeBodiesSet := false
	exhaustive := false
	rollupOnly := false
	contextLines := 0
	contextSet := false
	repo := ""
	var bare []string
args:
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--":
			// End of flags: every later argument is a term, so a term that
			// starts with "-" can be searched (BUG-18).
			bare = append(bare, args[i+1:]...)
			break args
		case "--dir":
			// The unambiguous way to say where to search, now that bare
			// arguments are terms rather than "term then directory".
			dir, err = flagValue("search", args, i)
			i++
		case "--scope":
			// The steering has documented `prism search <t> --scope text` since
			// v0.37.0 while this parser knew only --limit and --format, so the
			// flag was dropped and every CLI text search silently returned the
			// merged symbol view instead. A Bash-only subagent following its own
			// instructions got the wrong answer shape and no indication why.
			scope, err = choiceFlag("search", args, i, "text", "symbols", "both")
			i++
		case "--repo":
			// A linked repository's id, or "all": text matches there.
			repo, err = flagValue("search", args, i)
			i++
		case "--regex":
			regex = true
		case "--ignore-case", "-i":
			caseMode = "insensitive"
		case "--case-sensitive", "-s":
			caseMode = "sensitive"
		case "--word", "-w":
			word = true
		case "--no-default-excludes":
			noDefaultExcludes = true
		case "--include-sensitive":
			includeSensitive = true
		case "--path", "--paths":
			// The grep operand, restored. `prism search alias --path
			// octodns/manager.py` is what an agent that already knows the
			// file wants; without it, it uses grep instead.
			var p string
			p, err = flagValue("search", args, i)
			paths = append(paths, p)
			i++
		case "--glob", "--include":
			var g string
			g, err = flagValue("search", args, i)
			globs = append(globs, g)
			i++
		case "--include-bodies":
			includeBodies = true
			includeBodiesSet = true
		case "--no-bodies":
			includeBodies = false
			includeBodiesSet = true
		case "--files-only", "-l":
			filesOnly = true
		case "--exhaustive", "--all":
			exhaustive = true
		case "--rollup-only":
			rollupOnly = true
		case "--context", "-C":
			if i+1 >= len(args) {
				return usageError(fmt.Errorf("search: --context requires a non-negative integer"))
			}
			n, convErr := strconv.Atoi(args[i+1])
			if convErr != nil || n < 0 {
				return usageError(fmt.Errorf("search: --context requires a non-negative integer, got %q", args[i+1]))
			}
			contextLines, contextSet = n, true
			i++
		case "--limit":
			limit, err = positiveIntFlag("search", args, i)
			limitSet = true
			i++
		case "--format":
			format, err = formatFlag("search", args, i)
			i++
		default:
			// An unknown flag used to be dropped in silence — the mechanism
			// behind this whole class of bug. Fail loudly instead.
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "search: unknown flag %q\n", a)
				return 2
			}
			bare = append(bare, a)
		}
		if err != nil {
			return usageError(err)
		}
	}
	if len(bare) == 0 {
		fmt.Fprintln(os.Stderr, "usage: prism search <term> [term...] [--dir <path>]")
		return 2
	}
	if len(bare) > maxBatchTerms {
		return usageError(fmt.Errorf("search takes up to %d terms; got %d. Split into two calls", maxBatchTerms, len(bare)))
	}
	if msg, ok := mcpSpelling("search", bare); ok {
		fmt.Fprintln(os.Stderr, msg)
		return 2
	}
	if !regex {
		for _, t := range bare {
			if looksLikeRegex(t) {
				fmt.Fprintf(os.Stderr, "search: %q is matched as literal text; add --regex to use it as a regular expression\n", t)
			}
		}
	}
	// `prism search <keyword> <dir>` was the documented form for many
	// releases, so the two-argument case still honours it when the second
	// argument really is a directory — but says so, because the same two
	// words are now also a legitimate two-term search.
	if dir == "" && len(bare) == 2 {
		if fi, err := os.Stat(bare[1]); err == nil && fi.IsDir() {
			fmt.Fprintf(os.Stderr,
				"search: reading %q as the directory, not a second term (legacy `prism search <term> <dir>` form); "+
					"use --dir %s to be explicit, or --dir . to search for both words\n", bare[1], bare[1])
			dir, bare = bare[1], bare[:1]
		}
	}
	if dir == "" {
		dir = "."
	}
	searchReq := core.SearchRequest{
		Terms: bare, Scope: scope, Regex: regex, Case: caseMode, Word: word,
		NoDefaultExcludes: noDefaultExcludes, IncludeSensitive: includeSensitive,
		Paths: paths, Globs: globs, FilesOnly: filesOnly, Exhaustive: exhaustive,
		RollupOnly: rollupOnly, Repo: repo,
	}
	// An explicit --limit is a caller-chosen shape; without it the default
	// response budget applies (internal/core/searchbudget.go).
	if limitSet {
		searchReq.Limit = &limit
	}
	if includeBodiesSet {
		searchReq.IncludeBodies = &includeBodies
	}
	if contextSet {
		searchReq.Context = &contextLines
	}
	out, compactText, rendered, err := invokeSearchWithCompactRendering(dir, searchReq, format == formatText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		return 1
	}
	printToolResult("search", out, compactText, rendered, format)
	if m := toMap(out); m != nil {
		if _, invalid := m["invalidPattern"]; invalid {
			return exitUsage // the warning above names the bad pattern
		}
	}
	return 0
}

func invokeSearchWithCompactRendering(dir string, r core.SearchRequest, renderText bool) (any, string, bool, error) {
	args := r.Args()
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, "", false, err
	}
	defer client.Shutdown()
	// A text search is a pure grep (searchOne): it reads no graph, so it
	// must not build one first. On a large repository the first build takes
	// minutes, and a documented-as-instant text search waited for it.
	if args["scope"] != "text" {
		if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
			return nil, "", false, err
		}
	}
	h := core.NewHandler(cfg, root, client)
	out, err := h.Invoke("prism_search", args)
	if err != nil || !renderText {
		return out, "", false, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return out, "", false, nil
	}
	text, rendered := h.RenderCompactSearchText(context.Background(), m, args)
	return out, text, rendered, nil
}
