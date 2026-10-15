package mcp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// sourceText is a symbol's source as delivered: its indexed text with the
// first line's indentation restored from the file. The index stores the
// declaration from its first token, so a method's first line arrived flush
// left while the rest kept their indentation, and an agent copying the body
// into an Edit got a first line that matches nothing in the file.
func (h *Handler) sourceText(s grove.SymbolRecord) string { return symbolSourceText(h.Root, s) }

// symbolSourceText is sourceText for a known repository root.
func symbolSourceText(root string, s grove.SymbolRecord) string {
	raw := s.RawText
	if raw == "" || s.Span.Start < 1 || root == "" {
		return raw
	}
	first, rest, multi := strings.Cut(raw, "\n")
	if strings.TrimSpace(first) == "" {
		return raw
	}
	data, err := os.ReadFile(filepath.Join(root, s.FilePath))
	if err != nil {
		return raw
	}
	lines := strings.Split(string(data), "\n")
	if s.Span.Start > len(lines) {
		return raw
	}
	line := strings.TrimRight(lines[s.Span.Start-1], "\r")
	if line == first {
		return raw
	}
	// The index stores a declaration from its own first token: the
	// indentation, and for a variable the "const "/"let " before its name,
	// are not part of it. Deliver the whole first source line.
	full := line
	if !strings.Contains(line, strings.TrimSpace(first)) {
		return raw // the file changed since indexing
	}
	if multi {
		return full + "\n" + rest
	}
	return full
}
