package core

import (
	"context"
	"fmt"
	"github.com/provasign/prism/internal/grove"
	"regexp"
	"strconv"
	"strings"
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

// scopePathLineQuery turns a path:line query into the symbol declared there
// with its file as the scope (BUG-02). The engine resolved path:line to a
// bare name and then reported that name's same-named twins in other
// packages as ambiguous, though the line names one declaration.
func (h *Handler) scopePathLineQuery(ctx context.Context, args map[string]any, query string) (map[string]any, string, error) {
	if !pathLineForm.MatchString(query) || stringArg(args, "file", "") != "" {
		return args, query, nil
	}
	probe := map[string]any{"name": query}
	if _, err := h.resolvePathLine(ctx, probe, "name"); err != nil {
		return args, query, err
	}
	args = copyArgs(args)
	query = probe["name"].(string)
	args["query"], args["file"] = query, probe["file"]
	return args, query, nil
}

// qualifierMismatch rejects a package qualifier that names none of the
// declarations the engine resolved (BUG-01): change-impact
// fictitious.Client.Status answered for the real Client.Status, closed and
// safe to claim complete, so a mistyped package silently selected another
// symbol. The qualifier is what the query has in front of the
// declaration's own qualified name; lookup's locator decides whether it
// names the declaration's package, namespace or path.
func (h *Handler) qualifierMismatch(query string, r *grove.ChangeImpactResult) error {
	if r == nil || len(r.Declarations) == 0 || strings.ContainsAny(query, ":(") {
		return nil
	}
	loc := newLookupLocator(h.Root)
	var qualifier string
	for _, d := range r.Declarations {
		qn := d.QualifiedName
		if qn == "" {
			qn = d.Name
		}
		pre, ok := strings.CutSuffix(query, "."+qn)
		if !ok {
			if query == qn {
				return nil // no qualifier
			}
			continue
		}
		qualifier = pre
		file := normalizePath(d.FilePath)
		if strings.Contains(pre, "/") {
			// An import path or directory: github.com/org/repo/pkg, internal/grove.
			if loc.pathMatches(pre, file, false) {
				return nil
			}
		} else if loc.qualifierMatches(strings.Split(pre, "."), file, false) {
			return nil
		}
	}
	if qualifier == "" {
		return nil // the engine resolved a form this check does not parse
	}
	d := r.Declarations[0]
	return fmt.Errorf("no symbol named %q: %s exists in %s, but %q is not its package, namespace or path; use its qualified name or --file",
		query, d.QualifiedName, normalizePath(d.FilePath), qualifier)
}
