package mcp

// Per-file index failures at the answer boundary (audit 2026-10-20 #1).
//
// Grove's Index returns a nil Go error when single files fail (oversized,
// unreadable, unparsable) and lists them in IndexResult.Errors. Prism used to
// check only the Go error, so an answer computed while a.go could not be
// re-indexed — its OLD symbols still served — read as freshly refreshed.
//
// Grove re-reports a failing file on every refresh until it indexes (the stat
// cache is only updated on success), so the per-refresh error list is the
// set of still-unresolved files. Two cases, two policies:
//
//   - the file still has indexed facts: those are last-good facts that no
//     longer match the bytes on disk. Every answer says so while the failure
//     lasts — this is the stale result the warning exists for.
//   - the file has no indexed facts (never indexed, or pruned after a parse
//     failure): the answer is simply missing it. Said once while the failure
//     persists; a symlinked doc the indexer always skips must not tag every
//     answer of the session. A failure that clears and later recurs is new
//     and is said again.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// indexErrorFilesShown bounds the warning: first N failures, then a count.
	indexErrorFilesShown = 3
	// indexErrorFactChecks bounds the FileSymbols probes per refresh; a
	// refresh with thousands of failures must not cost thousands of queries.
	indexErrorFactChecks = 10
	indexErrorMsgLimit   = 120
)

// indexErrorTracker remembers which failures the previous refresh reported,
// so a persistent failure on a file without indexed facts is said once.
type indexErrorTracker struct {
	mu   sync.Mutex
	prev map[string]bool
}

// indexErrorWarning turns one refresh's IndexResult.Errors into a concise
// warning ("" when there is nothing new to say).
func (h *Handler) indexErrorWarning(ctx context.Context, errs []string) string {
	t := &h.indexErrs
	t.mu.Lock()
	defer t.mu.Unlock()
	current := make(map[string]bool, len(errs))
	type failure struct{ file, msg string }
	var show []failure
	staleShown := false
	checks := 0
	for _, e := range errs {
		if current[e] {
			continue
		}
		current[e] = true
		file, msg := h.indexErrorFile(e)
		retained := false
		if file != "" && h.Grove != nil && checks < indexErrorFactChecks {
			checks++
			if syms, err := h.Grove.FileSymbols(ctx, file); err == nil && len(syms) > 0 {
				retained = true
			}
		}
		if !retained && t.prev[e] {
			continue // already said, and no stale facts are being served
		}
		staleShown = staleShown || retained
		if file == "" {
			file, msg = e, ""
		}
		show = append(show, failure{file, msg})
	}
	t.prev = current
	if len(show) == 0 {
		return ""
	}
	parts := make([]string, 0, indexErrorFilesShown+1)
	for i, f := range show {
		if i == indexErrorFilesShown {
			parts = append(parts, fmt.Sprintf("+%d more", len(show)-i))
			break
		}
		if f.msg == "" {
			parts = append(parts, truncateIndexError(f.file))
		} else {
			parts = append(parts, f.file+" ("+truncateIndexError(f.msg)+")")
		}
	}
	consequence := "they are missing from this result"
	if staleShown {
		consequence = "answers involving them use facts indexed before their current contents and may be stale"
	}
	return fmt.Sprintf("index refresh incomplete: %d file(s) could not be indexed: %s; %s",
		len(show), strings.Join(parts, ", "), consequence)
}

// indexErrorFile splits a Grove "path: message" error into a root-relative
// path and the message. Errors that do not name a file return file "".
func (h *Handler) indexErrorFile(e string) (string, string) {
	file, msg, ok := strings.Cut(e, ": ")
	if !ok || file == "" || strings.ContainsAny(file, " \t") {
		return "", e
	}
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(h.Root, file)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", e
		}
		file = rel
	}
	return filepath.ToSlash(file), msg
}

func truncateIndexError(s string) string {
	if len(s) <= indexErrorMsgLimit {
		return s
	}
	return strings.ToValidUTF8(s[:indexErrorMsgLimit], "") + "…"
}
