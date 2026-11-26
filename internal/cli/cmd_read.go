package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdRead(args []string) int {
	args, rc := positionalsFirst("read", "a file", "prism read <file> [file...] [--from N --to M | --offset N --limit N] [--repo <id>] [--dir <path>]", 1, args, "--from", "--to", "--repo", "--dir", "--offset", "--limit", "--format")
	if rc != 0 {
		return rc
	}
	file := args[0]
	dir := "."
	includeSensitive := false
	repo := ""
	dirSet := false
	var extra []string
	format := formatText
	offset, limit := 0, 0
	from, to := 0, 0
	for i := 1; i < len(args); i++ {
		a := args[i]
		// --flag=value spellings, as other commands accept.
		if name, value, ok := strings.Cut(a, "="); ok && strings.HasPrefix(name, "--") {
			args = append(args[:i+1], append([]string{value}, args[i+1:]...)...)
			args[i] = name
			a = name
		}
		var err error
		switch a {
		case "--from", "--to":
			// The MCP read's own field names (an agent switching from the
			// tool to the CLI writes these): an inclusive line range.
			var n int
			n, err = positiveIntFlag("read", args, i)
			if a == "--from" {
				from = n
			} else {
				to = n
			}
			i++
		case "--repo":
			repo, err = flagValue("read", args, i)
			i++
		case "--dir":
			dir, err = flagValue("read", args, i)
			dirSet = true
			i++
		case "--offset", "--limit":
			// Line-window parity with `sed -n A,Bp`, which is a quarter of
			// every file read agents make.
			var n int
			n, err = positiveIntFlag("read", args, i)
			if a == "--offset" {
				offset = n
			} else {
				limit = n
			}
			i++
		case "--format":
			format, err = formatFlag("read", args, i)
			i++
		case "--include-sensitive":
			includeSensitive = true
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("read", a)
			}
			extra = append(extra, a)
		}
		if err != nil {
			return usageError(err)
		}
	}
	if (from > 0 || to > 0) && (offset > 0 || limit > 0) {
		return usageError(fmt.Errorf("read: use --from/--to or --offset/--limit, not both"))
	}
	if from > 0 && to > 0 && to < from {
		return usageError(fmt.Errorf("read: --to %d is before --from %d; the range is inclusive, so --to must be >= --from", to, from))
	}
	// `prism read <file> <dir>` is the legacy form; any other extra argument
	// is another file. The last path used to be taken as the directory, so
	// `prism read a.go b.go c.go` failed with "not a directory" (Codex, jackson).
	if len(extra) == 1 && !dirSet {
		if fi, err := os.Stat(extra[0]); err == nil && fi.IsDir() {
			dir, extra = extra[0], nil
		}
	}
	if repo != "" {
		root, files, err := readRepoPaths(dir, repo, append([]string{file}, extra...))
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			return 1
		}
		dir, file, extra = root, files[0], files[1:]
	}
	if len(extra) > 0 {
		return cmdReadFiles(dir, append([]string{file}, extra...), from, to, offset, limit, format)
	}
	if from > 0 {
		offset = from
		if to >= from {
			limit = to - from + 1
		}
	} else if to > 0 {
		offset, limit = 1, to
	}
	readReq := core.ReadRequest{File: file, IncludeSensitive: includeSensitive, Offset: max(offset, 0), Limit: max(limit, 0)}
	var out any
	var text string
	var rendered bool
	var err error
	if format == formatText && !includeSensitive {
		compactReq := core.CompactReadRequest{File: file, IncludeSensitive: includeSensitive}
		start := 1
		if offset > 0 {
			start = offset
			compactReq.From = start
		}
		if limit > 0 {
			compactReq.To = start + limit - 1
		}
		out, text, rendered, err = invokeCompactReadText(dir, compactReq)
	} else {
		out, text, rendered, err = invokeToolText(dir, readReq)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return exitForError(err)
	}
	printToolResult("read", out, text, rendered, format)
	return exitForAnswer("read", out)
}

// maxBatchTerms is the compact MCP gateway's limit for terms and names in one
// call (internal/mcp/compactargs.go). query and multi-file read apply it, so
// a request the gateway would reject is not answered here: a 12-term CLI
// query returned 30 KB of source on jackson (Codex, 2026-10-18). search stays
// uncapped on purpose (a82bd08); its answer has its own response budget.
const maxBatchTerms = 10

// cmdReadFiles reads several files in one answer through the gateway's ranges
// form, the CLI counterpart of op=read with ranges. Each file is read from its
// first line under the same per-range window as MCP.
func cmdReadFiles(dir string, files []string, from, to, offset, limit int, format outputFormat) int {
	if from > 0 || to > 0 || offset > 0 || limit > 0 {
		fmt.Fprintln(os.Stderr, "read: --from/--to/--offset/--limit apply to one file; read the files one at a time for a line range")
		return 2
	}
	if len(files) > maxBatchTerms {
		fmt.Fprintf(os.Stderr, "read: at most %d files per call (the MCP gateway rejects more)\n", maxBatchTerms)
		return 2
	}
	out, text, rendered, err := invokeCompactReadText(dir, core.CompactReadFilesRequest{Files: files})
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		return exitForError(err)
	}
	printToolResult("read", out, text, rendered, format)
	return exitForAnswer("read", out)
}
