package mcp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/view"
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
			if len(opts.Paths) > 0 || len(opts.Glob) > 0 {
				// A scope that names no file of the repository tested no term.
				// "0 match(es)" there read as "not in boxo" (kubo-ipns, 11-15).
				if miss := scopeFilterCheck(r.Path, opts.Paths, opts.Glob, true); miss.noFiles {
					fmt.Fprintf(&b, "// repo %s  %s  %q: %s\n", r.ID, rel, t, miss.note)
					continue
				}
			}
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
			// Test-file lines are counted per file, not listed, as in a local
			// search: they were 40% of linked-repo lines in the 11-15/11-16
			// runs. exhaustive and files_only still list everything.
			testHits, testFiles := 0, map[string]bool{}
			for _, hit := range res.Hits {
				if !filesOnly && !opts.Exhaustive && view.IsTestPath(hit.File) {
					testHits++
					testFiles[hit.File] = true
					continue
				}
				if filesOnly {
					if !seen[hit.File] {
						seen[hit.File] = true
						fmt.Fprintf(&b, "%s/%s\n", rel, hit.File)
					}
					continue
				}
				fmt.Fprintf(&b, "%s/%s:%d: %s\n", rel, hit.File, hit.Line, hit.Text)
			}
			if testHits > 0 {
				fmt.Fprintf(&b, "// test files: %d hit(s) in %d file(s) of %s, lines omitted (--exhaustive lists them)\n", testHits, len(testFiles), r.ID)
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
			if view.IsTestPath(hit.File) {
				continue
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

// pathScopeListCap bounds a file listing answered for a path-only search.
const pathScopeListCap = 200

// pathScopeTerms turns a search term that ends in "/" and names a real
// directory into a path scope, in a project with linked repositories.
// `prism search sigstore-go/` searched the literal text (about 10k characters
// of unrelated hits in the multi-repo bench); the agent meant "look in that
// repository". A term must end in "/" and exist as a directory: the primary
// root's, or a linked repository named by its id or folder name. Dropping
// the slash searches the text.
//
// routed carries the term's scope (repo/path) with the remaining terms. When
// no term remains, listing is the scope's files and routed is nil. ok=false
// leaves the search untouched.
func (h *Handler) pathScopeTerms(args map[string]any) (routed map[string]any, note, listing string, ok bool) {
	if h.Workspace == nil || len(h.Workspace.Linked()) == 0 || boolArg(args, "regex") ||
		stringArg(args, "repo", "") != "" || len(stringsArg(args, "path")) > 0 {
		return nil, "", "", false
	}
	var rest, scoped, rels []string
	scopeRepo, first := "", true
	for _, t := range stringsArg(args, "query") {
		id, rel, hit := h.resolvePathTerm(t)
		if !hit || (!first && id != scopeRepo) {
			rest = append(rest, t)
			continue
		}
		first, scopeRepo = false, id
		scoped = append(scoped, t)
		if rel != "" {
			rels = append(rels, rel)
		}
	}
	if len(scoped) == 0 {
		return nil, "", "", false
	}
	where := "this repository"
	if scopeRepo != "" {
		where = "--repo " + scopeRepo
	}
	note = fmt.Sprintf("treated %q as a path scope (%s); drop the trailing slash to search it as text", strings.Join(scoped, `", "`), where)
	if len(rest) == 0 {
		base, display := h.Root, ""
		if scopeRepo != "" {
			roots, err := h.linkedSearchRoots(scopeRepo)
			if err != nil || len(roots) == 0 {
				return nil, "", "", false
			}
			base, display = roots[0].Path, h.Workspace.RelPath(roots[0].Path)
		}
		subs := rels
		if len(subs) == 0 {
			subs = []string{""}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "// %s\n", note)
		for _, sub := range subs {
			b.WriteString(scopeFiles(base, sub, display))
		}
		return nil, note, b.String(), true
	}
	routed = make(map[string]any, len(args)+2)
	for k, v := range args {
		routed[k] = v
	}
	routed["query"] = rest
	if scopeRepo != "" {
		routed["repo"] = scopeRepo
	}
	if len(rels) > 0 {
		routed["path"] = rels
	}
	return routed, note, "", true
}

// resolvePathTerm reports whether term is "<dir>/" naming an existing
// directory, and where: a linked repository's id ("" for this repository)
// with the path inside it ("" for its root).
func (h *Handler) resolvePathTerm(term string) (repo, rel string, ok bool) {
	if !strings.HasSuffix(term, "/") || strings.ContainsAny(term, " \t*?[]{}()|^$:\\") {
		return "", "", false
	}
	clean := strings.Trim(strings.TrimPrefix(term, "./"), "/")
	if clean == "" || filepath.IsAbs(term) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", false
	}
	isDir := func(p string) bool { info, err := os.Stat(p); return err == nil && info.IsDir() }
	if isDir(filepath.Join(h.Root, filepath.FromSlash(clean))) {
		return "", clean, true
	}
	segs := strings.SplitN(clean, "/", 2)
	for _, r := range h.Workspace.Linked() {
		if segs[0] != r.ID && segs[0] != filepath.Base(r.Path) {
			continue
		}
		sub := ""
		if len(segs) == 2 {
			sub = segs[1]
		}
		if isDir(filepath.Join(r.Path, filepath.FromSlash(sub))) {
			return r.ID, sub, true
		}
	}
	return "", "", false
}

// scopeFiles lists the files under base/sub (paths shown under display),
// skipping hidden and vendored folders, capped at pathScopeListCap.
func scopeFiles(base, sub, display string) string {
	start := filepath.Join(base, filepath.FromSlash(sub))
	var files []string
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != start && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(base, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	var b strings.Builder
	for i, f := range files {
		if i == pathScopeListCap {
			fmt.Fprintf(&b, "// %d more file(s) not shown; add a search term to narrow\n", len(files)-i)
			break
		}
		if display != "" {
			f = display + "/" + f
		}
		b.WriteString(f + "\n")
	}
	if len(files) == 0 {
		b.WriteString("// no files\n")
	}
	return b.String()
}
