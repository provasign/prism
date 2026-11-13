package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/workspace"
)

// linkedSearchBudget bounds the count pass over one linked repository that
// follows an ordinary search. The primary answer is already assembled; a
// slow sibling must not hold it, and a timeout is reported, not hidden.
const linkedSearchBudget = 1500 * time.Millisecond

// linkedSearchArgs reads the search arguments that apply to a text pass in
// another repository.
func linkedSearchArgs(args map[string]any) (terms []string, opts textsearch.Options, filesOnly bool) {
	switch q := args["query"].(type) {
	case string:
		terms = []string{q}
	case []string:
		terms = q
	case []any:
		for _, v := range q {
			if s, ok := v.(string); ok {
				terms = append(terms, s)
			}
		}
	}
	opts = textsearch.Options{
		Regex:      boolArg(args, "regex"),
		Paths:      stringsArg(args, "path"),
		Glob:       stringsArg(args, "glob"),
		Exhaustive: boolArg(args, "exhaustive"),
	}
	if n := intArg(args, "limit", 0); n > 0 {
		opts.MaxHits = n
	}
	return terms, opts, boolArg(args, "files_only")
}

// linkedSearchRoots resolves the repo argument: a linked repository's id,
// or "all".
func (h *Handler) linkedSearchRoots(repo string) ([]workspace.Root, error) {
	if h.Workspace == nil {
		return nil, fmt.Errorf("repo=%q needs linked repositories; none are declared (workspace_repo in prism.yaml)", repo)
	}
	if len(h.Workspace.Linked()) == 0 {
		var why []string
		for _, r := range h.Workspace.Roots {
			why = append(why, r.ID+": "+r.Unusable())
		}
		return nil, fmt.Errorf("repo=%q: no linked repository can be searched (%s)", repo, strings.Join(why, "; "))
	}
	var roots []workspace.Root
	for _, r := range h.Workspace.Linked() {
		if repo == "all" || r.ID == repo {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no linked repository %q; linked: %s", repo, h.Workspace.Describe())
	}
	return roots, nil
}

// linkedSearchListing lists text matches in the given linked repositories,
// with paths relative to the primary root.
func (h *Handler) linkedSearchListing(ctx context.Context, roots []workspace.Root, args map[string]any) string {
	terms, opts, filesOnly := linkedSearchArgs(args)
	var b strings.Builder
	for _, r := range roots {
		rel := h.Workspace.RelPath(r.Path)
		for _, t := range terms {
			res := textsearch.Search(ctx, r.Path, t, opts)
			total := res.TotalHits
			if total < len(res.Hits) {
				total = len(res.Hits)
			}
			state := fmt.Sprintf("%d match(es)", total)
			switch {
			case res.TimedOut:
				state += ", search timed out: INCOMPLETE"
			case res.Truncated:
				state = fmt.Sprintf("at least %d match(es), first %d shown (exhaustive lists all)", total, len(res.Hits))
			}
			fmt.Fprintf(&b, "// repo %s  %s  %q: %s\n", r.ID, rel, t, state)
			for _, p := range res.RejectedPaths {
				fmt.Fprintf(&b, "// path %q is not inside %s; ignored\n", p, r.ID)
			}
			seen := map[string]bool{}
			for _, hit := range res.Hits {
				if filesOnly {
					if !seen[hit.File] {
						seen[hit.File] = true
						fmt.Fprintf(&b, "%s/%s\n", rel, hit.File)
					}
					continue
				}
				fmt.Fprintf(&b, "%s/%s:%d: %s\n", rel, hit.File, hit.Line, hit.Text)
			}
		}
	}
	return b.String()
}

// LinkedSearchTail is what an ordinary text-format search ends with when
// repositories are linked: with repo=all the matches in each, otherwise one
// line with how many each holds. It keeps the answer the size it was while
// saying the other repositories were not listed. "" without a workspace.
func (h *Handler) LinkedSearchTail(ctx context.Context, args map[string]any) string {
	if h.Workspace == nil {
		return ""
	}
	linked := h.Workspace.Linked()
	var absent []string
	for _, r := range h.Workspace.Roots {
		if why := r.Unusable(); why != "" {
			absent = append(absent, r.ID+" "+shortReason(why))
		}
	}
	if len(linked) == 0 && len(absent) == 0 {
		return ""
	}
	if stringArg(args, "repo", "") == "all" {
		out := h.linkedSearchListing(ctx, linked, args)
		for _, a := range absent {
			out += "// repo " + a + ": NOT SEARCHED\n"
		}
		return out
	}
	terms, opts, _ := linkedSearchArgs(args)
	parts := make([]string, 0, len(linked)+len(absent))
	for _, r := range linked {
		cctx, cancel := context.WithTimeout(ctx, linkedSearchBudget)
		hits, files, timedOut := 0, 0, false
		for _, t := range terms {
			c := textsearch.Count(cctx, r.Path, t, opts)
			hits += c.TotalHits
			files += c.FilesMatched
			timedOut = timedOut || c.TimedOut || cctx.Err() != nil
		}
		cancel()
		switch {
		case timedOut:
			parts = append(parts, r.ID+" not counted (timed out)")
		case hits == 0:
			parts = append(parts, r.ID+" 0")
		default:
			parts = append(parts, fmt.Sprintf("%s %d in %d file(s)", r.ID, hits, files))
		}
	}
	parts = append(parts, absent...)
	return "// linked repositories, text matches not listed above (repo=<id> lists them): " + strings.Join(parts, "; ") + "\n"
}
