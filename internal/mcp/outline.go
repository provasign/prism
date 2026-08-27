package mcp

// Large-file outline: the map of a file instead of its territory.
//
// A 2,000-line Java class costs ~21k tokens delivered whole, is re-read on
// every subsequent turn, and is usually wanted for one method. The outline
// costs ~600 tokens and tells the agent exactly which line range to fetch.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// outlineMinLines is where a whole-file read stops being the cheap option.
// Below it a file is a few KB and a second round trip costs more than the
// body; above it the body dominates the session's context mass.
const outlineMinLines = 800

// outlineSymbolCap bounds the map itself — a 5,000-line file with 400
// members must not reproduce the problem it exists to solve.
const outlineSymbolCap = 60

// fileOutline renders the symbol map for a large file. ok=false means the
// caller should deliver the file normally (small file, or nothing indexed).
func (h *Handler) fileOutline(path, content string, syms []grove.SymbolRecord) (map[string]any, bool) {
	if len(syms) == 0 {
		return nil, false // nothing to map: deliver the file normally
	}
	// A trailing newline terminates the last line, it does not start a new
	// one — counting it made an 800-line file report 801 and tripped the
	// threshold one line early.
	total := strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
	if total < outlineMinLines {
		return nil, false
	}
	ordered := make([]grove.SymbolRecord, len(syms))
	copy(ordered, syms)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Span.Start < ordered[j].Span.Start })

	var b strings.Builder
	fmt.Fprintf(&b, "// %s — OUTLINE (%d lines, %d indexed symbols)\n", path, total, len(ordered))
	fmt.Fprintf(&b, "// The body was NOT delivered: at %d lines it would occupy your context for the\n", total)
	b.WriteString("// rest of this task. Fetch only what you need:\n")
	b.WriteString("//   prism_lookup name=\"<Type.member>\"      one symbol, with its body\n")
	b.WriteString("//   prism_read offset=<n> limit=<n>        an exact line range\n")
	b.WriteString("//   prism_read full=true                   the entire file, if you truly need it\n")
	shown := ordered
	if len(shown) > outlineSymbolCap {
		shown = shown[:outlineSymbolCap]
	}
	for _, s := range shown {
		name := s.QualifiedName
		if name == "" {
			name = s.Name
		}
		sig := strings.TrimSpace(firstLine(s.Signature))
		if sig == "" {
			sig = s.Kind
		}
		fmt.Fprintf(&b, "%5d-%-5d %s\n", s.Span.Start, s.Span.End, truncateOutline(sig, 110))
		_ = name
	}
	if len(ordered) > outlineSymbolCap {
		fmt.Fprintf(&b, "// +%d more symbols — prism_search to locate one by name\n",
			len(ordered)-outlineSymbolCap)
	}
	return map[string]any{
		"file":       path,
		"strategy":   "outline",
		"totalLines": total,
		"content":    b.String(),
	}, true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateOutline(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " …"
}
