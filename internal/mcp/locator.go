package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

// pathLineForm is "path:line" with a file extension, as an editor or a
// compiler error prints it.
var pathLineForm = regexp.MustCompile(`^(.+\.[A-Za-z0-9]+):(\d+)$`)

// resolvePathLine rewrites args["name"] given as path:line to the innermost
// symbol declared over that line, with its file as the scope (CON-10).
// change-impact, references, rename-plan and missing-implementations
// already accepted this form through the engine; lookup and node did not.
// It reports false when name is not of that form.
func (h *Handler) resolvePathLine(ctx context.Context, args map[string]any, key string) (bool, error) {
	name, _ := args[key].(string)
	m := pathLineForm.FindStringSubmatch(name)
	if m == nil {
		return false, nil
	}
	file := normalizePath(m[1])
	line, _ := strconv.Atoi(m[2])
	syms, err := h.Grove.FileSymbols(ctx, file)
	if err != nil || len(syms) == 0 {
		if h.fileExists(file) {
			return true, fmt.Errorf("%s:%d: no symbol named at that line: %s has no indexed symbols", file, line, file)
		}
		return true, fmt.Errorf("%s:%d: file not found in the index", file, line)
	}
	best := -1
	for i, s := range syms {
		if s.Span.Start <= line && line <= s.Span.End {
			if best < 0 || s.Span.End-s.Span.Start < syms[best].Span.End-syms[best].Span.Start {
				best = i
			}
		}
	}
	if best < 0 {
		return true, fmt.Errorf("%s:%d: no symbol named at that line (no declaration spans line %d)", file, line, line)
	}
	s := syms[best]
	qn := s.QualifiedName
	if qn == "" {
		qn = s.Name
	}
	args[key] = qn
	args["file"] = file
	return true, nil
}

// fileNotSymbolError answers a bare file path given to a symbol command.
func (h *Handler) fileNotSymbolError(name string) error {
	if pathLineForm.MatchString(name) || !h.fileExists(name) {
		return nil
	}
	return fmt.Errorf("%s is a file, not a symbol: name a symbol, or a line in it (%s:<line>); prism read or prism node show the file", name, name)
}

// copyArgs is a shallow copy, so a rewrite does not leak into the caller's map.
func copyArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	return out
}
