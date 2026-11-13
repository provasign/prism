package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/workspace"
)

// linkedRefreshBudget bounds bringing a linked repository's index up to
// date before answering from it. PRISM_WORKSPACE_REFRESH is a Go duration
// that overrides it; "off" never refreshes (the answer then names the
// repository as stale instead).
func linkedRefreshBudget() time.Duration {
	switch v := strings.TrimSpace(os.Getenv("PRISM_WORKSPACE_REFRESH")); v {
	case "":
		return 20 * time.Second
	case "off", "0":
		return 0
	default:
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		return 20 * time.Second
	}
}

// freshenLinked makes sure an answer from a linked repository's index
// describes the tree that is checked out there now.
//
// An index describes one tree state. The repository may since have moved to
// another commit or branch, or have uncommitted edits. When it has, the
// ordinary incremental index run is tried within a time budget. If the index
// is still behind afterwards, stale says exactly how, and the caller must
// show it: an answer from an old tree that does not say so reads as current.
//
// indexed is false when the repository has no index at all; none is built
// here (a first index can take minutes and is the user's call).
func (h *Handler) freshenLinked(ctx context.Context, r workspace.Root) (stale string, indexed bool) {
	if h.toolSurface {
		h.freshMu.Lock()
		f, ok := h.fresh[r.Path]
		h.freshMu.Unlock()
		if ok && time.Since(f.at) < freshnessTTL {
			return f.stale, f.indexed
		}
		defer func() {
			h.freshMu.Lock()
			if h.fresh == nil {
				h.fresh = map[string]freshness{}
			}
			h.fresh[r.Path] = freshness{stale: stale, indexed: indexed, at: time.Now()}
			h.freshMu.Unlock()
		}()
	}
	return h.freshenLinkedNow(ctx, r)
}

// freshness is one linked root's last staleness verdict.
type freshness struct {
	stale   string
	indexed bool
	at      time.Time
}

// freshnessTTL: long enough to cover the burst of calls an agent makes
// while working on one change, short enough that an edit in the other
// repository is seen by the next burst.
const freshnessTTL = 5 * time.Second

func (h *Handler) freshenLinkedNow(ctx context.Context, r workspace.Root) (stale string, indexed bool) {
	state, err := grove.ReadIndexState(ctx, r.Path)
	if err != nil {
		return "", false
	}
	why := linkedStaleReason(r, state)
	if why == "" {
		return "", true
	}
	budget := linkedRefreshBudget()
	if budget > 0 {
		rctx, cancel := context.WithTimeout(ctx, budget)
		err := grove.RefreshIndex(rctx, r.Path, workspace.RootsOf(r.Path)...)
		cancel()
		if err == nil {
			return "", true
		}
		return fmt.Sprintf("STALE: %s; the index could not be brought up to date within %s (%v)", why, budget, err), true
	}
	return "STALE: " + why + "; not refreshed", true
}

// linkedStaleReason compares the tree an index was built from with the one
// on disk. "" means no difference was found.
func linkedStaleReason(r workspace.Root, state grove.IndexState) string {
	root := r.Path
	// Two things make an index wrong without any file changing. Written by
	// another version, it was resolved by other rules; one from before
	// cross-repository references existed holds none, and "no references"
	// from it would be false. Built with a different set of linked
	// repositories, a compiler pass may not have had a dependency's
	// declarations to resolve against.
	if state.Resolver != grove.ResolverVersion {
		return "its index was written by another Prism version, so cross-repository references may be missing from it"
	}
	if state.Linked != grove.LinkedDigest(workspace.RootsOf(root)) {
		return "the repositories linked to it changed since it was indexed"
	}
	if r.Cache {
		return "" // a cache clone is one commit and never changes
	}
	now := workspace.CurrentTree(root)
	if now.Head == "" {
		return "" // not a git work tree: nothing to compare against
	}
	if state.Head == "" {
		return "its index predates commit tracking, so whether it matches " + describeTree(now.Ref, now.Head) + " is unknown"
	}
	if state.Head != now.Head {
		return "indexed at " + describeTree(state.Ref, state.Head) + ", now at " + describeTree(now.Ref, now.Head)
	}
	finished, err := time.Parse(time.RFC3339, state.Finished)
	if err != nil {
		return ""
	}
	changed := 0
	for _, f := range now.Changed {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(f)))
		// RFC 3339 keeps whole seconds: a file written in the same second
		// as the run finished counts as newer.
		if err != nil || !info.ModTime().Before(finished) {
			changed++
		}
	}
	if changed > 0 {
		return fmt.Sprintf("%d uncommitted file(s) changed since it was indexed", changed)
	}
	return ""
}

func describeTree(ref, head string) string {
	if len(head) > 8 {
		head = head[:8]
	}
	if ref == "" {
		return head + " (detached)"
	}
	return ref + "@" + head
}
