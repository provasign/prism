package mcp

import (
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// Grove attributes a call made inside a nested function or closure to the
// ENCLOSING named declaration — the right identity for blast radius, and the
// convention its own eval oracle uses. The cost is locality: a caller reported
// as `table_names` may hold the actual call several lines deep inside a nested
// `def get_names(...)`, leaving the reader to hunt for it in a long method.
//
// nestedScopeFor recovers just that missing hint. It is presentation only —
// no symbol is created and no edge changes — so a caller with no nested scope
// renders exactly as before.
//
// Returns the innermost nested function name containing the call to
// targetLeaf, or "" when the call is directly in the caller's own body (the
// common case), when the data needed is absent, or when nothing can be
// determined confidently.
func nestedScopeFor(caller grove.SymbolRecord, targetLeaf string) string {
	if caller.RawText == "" || targetLeaf == "" || len(caller.CallSites) == 0 || caller.Span.Start <= 0 {
		return ""
	}
	lines := strings.Split(caller.RawText, "\n")
	if len(lines) < 2 {
		return ""
	}
	declIndent := indentWidth(lines[0])

	best := ""
	for _, cs := range caller.CallSites {
		if leafOf(cs.Callee) != targetLeaf || cs.Line <= 0 {
			continue
		}
		// CallSite lines are absolute; RawText starts at the symbol's span.
		idx := cs.Line - caller.Span.Start
		if idx <= 0 || idx >= len(lines) {
			continue
		}
		callIndent := indentWidth(lines[idx])
		// Walk upward for the nearest function header that both encloses this
		// line (shallower indent) and is itself nested inside the caller
		// (deeper indent than the caller's own declaration).
		for i := idx - 1; i > 0; i-- {
			line := lines[i]
			if strings.TrimSpace(line) == "" {
				continue
			}
			ind := indentWidth(line)
			if ind >= callIndent {
				continue // sibling or deeper: not an enclosing scope
			}
			if ind <= declIndent {
				break // reached the caller's own level: the call is not nested
			}
			if name := nestedFuncName(line); name != "" {
				best = name
				break
			}
		}
		if best != "" {
			return best
		}
	}
	return best
}

// nestedFuncName extracts the declared name from a nested function header,
// covering the languages whose closures grove folds into the enclosing symbol.
// Returns "" for anything that is not unambiguously a named function header.
func nestedFuncName(line string) string {
	s := strings.TrimSpace(line)
	for _, kw := range []string{"def ", "func ", "function "} {
		rest, ok := strings.CutPrefix(s, kw)
		if !ok {
			continue
		}
		// Go methods start with a receiver: "func (r T) Name(" — not nested.
		if strings.HasPrefix(rest, "(") {
			return ""
		}
		name := rest
		if i := strings.IndexAny(name, "(<[ \t"); i >= 0 {
			name = name[:i]
		}
		if isPlainIdent(name) {
			return name
		}
		return ""
	}
	// "name := func(" / "name = function(" — a closure bound to a name.
	for _, op := range []string{":=", "="} {
		lhs, rhs, ok := strings.Cut(s, op)
		if !ok {
			continue
		}
		rhs = strings.TrimSpace(rhs)
		if !strings.HasPrefix(rhs, "func") && !strings.HasPrefix(rhs, "function") &&
			!strings.HasPrefix(rhs, "lambda") {
			continue
		}
		name := strings.TrimSpace(lhs)
		if isPlainIdent(name) {
			return name
		}
	}
	return ""
}

func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func indentWidth(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// leafOf returns the final segment of a possibly-qualified callee
// ("self.get_table_list" -> "get_table_list").
func leafOf(callee string) string {
	if i := strings.LastIndexByte(callee, '.'); i >= 0 {
		return callee[i+1:]
	}
	return callee
}
