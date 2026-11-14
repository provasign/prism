package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	for _, r := range h.Workspace.Roots {
		if why := r.Unusable(); why != "" && r.ID == repo {
			return nil, fmt.Errorf("linked repository %q (%s) NOT SEARCHED: %s", repo, h.Workspace.DisplayPath(r), why)
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
	how := "--repo <id> lists them"
	if h.toolSurface {
		how = "repo=<id> lists them"
	}
	if len(opts.Paths) > 0 {
		// A path filter names files of this repository. Applied to another
		// one it matches nothing, and "0" would read as "not there".
		return "// linked repositories not counted: the path filter applies to this repository (" + how + ", with paths of that repository)\n"
	}
	parts := make([]string, 0, len(linked)+len(absent))
	counts := make([]string, len(linked))
	// The first few matching lines of each linked repository, so an answer
	// that is there does not take a second call: in the 22-task multi-repo
	// bench (2026-11-14) 38 of 153 prism calls were --repo reruns.
	previews := make([][]string, len(linked))
	var wg sync.WaitGroup
	for i, r := range linked {
		wg.Add(1)
		go func(i int, r workspace.Root) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, linkedSearchBudget)
			defer cancel()
			hits, files, timedOut := 0, 0, false
			for _, t := range terms {
				c := textsearch.Count(cctx, r.Path, t, opts)
				hits += c.TotalHits
				files += c.FilesMatched
				timedOut = timedOut || c.TimedOut || cctx.Err() != nil
			}
			switch {
			case timedOut:
				counts[i] = r.ID + " not counted (timed out)"
			case hits == 0:
				counts[i] = r.ID + " 0"
			default:
				counts[i] = fmt.Sprintf("%s %d in %d file(s)", r.ID, hits, files)
				previews[i] = h.linkedPreview(cctx, r, terms, opts, hits)
			}
		}(i, r)
	}
	wg.Wait()
	parts = append(parts, counts...)
	parts = append(parts, absent...)
	out := "// linked repositories, text matches not listed above (" + how + "): " + strings.Join(parts, "; ") + "\n"
	for _, p := range previews {
		out += strings.Join(p, "")
	}
	return out
}

// linkedPreviewLines caps the lines LinkedSearchTail shows per linked repository.
const linkedPreviewLines = 5

// linkedPreview lists up to linkedPreviewLines matching lines of one linked
// repository (paths relative to the primary root), and how many it left out.
func (h *Handler) linkedPreview(ctx context.Context, r workspace.Root, terms []string, opts textsearch.Options, hits int) []string {
	rel := h.Workspace.RelPath(r.Path)
	opts.MaxHits, opts.Exhaustive = linkedPreviewLines, false
	var lines []string
	for _, t := range terms {
		if len(lines) >= linkedPreviewLines || ctx.Err() != nil {
			break
		}
		for _, hit := range textsearch.Search(ctx, r.Path, t, opts).Hits {
			if len(lines) >= linkedPreviewLines {
				break
			}
			lines = append(lines, fmt.Sprintf("%s/%s:%d: %s\n", rel, hit.File, hit.Line, strings.TrimSpace(hit.Text)))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if more := hits - len(lines); more > 0 {
		how := "--repo " + r.ID
		if h.toolSurface {
			how = "repo=" + r.ID
		}
		lines = append(lines, fmt.Sprintf("// %s: %d more match(es) not shown (%s lists them)\n", r.ID, more, how))
	}
	return lines
}

// linkedPathRepo reports whether every path argument of a search with no
// repo argument resolves inside one linked repository, and those paths
// relative to it.
func (h *Handler) linkedPathRepo(args map[string]any) (id string, rels []string, ok bool) {
	if h.Workspace == nil || stringArg(args, "repo", "") != "" {
		return "", nil, false
	}
	paths := stringsArg(args, "path")
	if len(paths) == 0 {
		return "", nil, false
	}
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(h.Root, p)
		}
		abs = filepath.Clean(abs)
		if rel, err := filepath.Rel(h.Root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", nil, false // inside this repository: an ordinary search
		}
		found := ""
		for _, r := range h.Workspace.Linked() {
			rel, err := filepath.Rel(r.Path, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			found = r.ID
			rels = append(rels, filepath.ToSlash(rel))
			break
		}
		if found == "" || (id != "" && found != id) {
			return "", nil, false
		}
		id = found
	}
	return id, rels, true
}

// outsideRootHint says where a path outside the root can be searched.
func (h *Handler) outsideRootHint() string {
	if h.Workspace != nil && len(h.Workspace.Linked()) > 0 {
		var ids []string
		for _, r := range h.Workspace.Linked() {
			ids = append(ids, r.ID)
		}
		flag := "--repo <id>"
		if h.toolSurface {
			flag = "repo=<id>"
		}
		return "a linked repository is searched with " + flag + " and paths of that repository; linked: " + strings.Join(ids, ", ")
	}
	return "for another repository run the prism CLI there or use its own prism MCP server"
}
