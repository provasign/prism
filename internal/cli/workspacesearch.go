package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/workspace"
)

// linkedSearchBudget bounds the count pass over one linked repository that
// follows an ordinary search. The primary answer is already printed; a slow
// sibling must not hold the command, and a timeout is reported, not hidden.
const linkedSearchBudget = 1500 * time.Millisecond

func loadWorkspace(dir string) (*workspace.Workspace, error) {
	root, _ := cliRoot(dir)
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, err
	}
	return workspace.Load(root, cfg), nil
}

// linkedSearchCounts is the line an ordinary text search ends with when
// repositories are linked: how many matches each holds. It keeps the answer
// the size it was while saying the other repositories were not listed.
func linkedSearchCounts(ws *workspace.Workspace, terms []string, opts textsearch.Options) string {
	linked := ws.Linked()
	if len(linked) == 0 {
		return ""
	}
	parts := make([]string, 0, len(linked))
	for _, r := range linked {
		ctx, cancel := context.WithTimeout(context.Background(), linkedSearchBudget)
		hits, files, timedOut := 0, 0, false
		for _, t := range terms {
			c := textsearch.Count(ctx, r.Path, t, opts)
			hits += c.TotalHits
			files += c.FilesMatched
			timedOut = timedOut || c.TimedOut || ctx.Err() != nil
		}
		cancel()
		switch {
		case timedOut:
			parts = append(parts, fmt.Sprintf("%s not counted (timed out)", r.ID))
		case hits == 0:
			parts = append(parts, r.ID+" 0")
		default:
			parts = append(parts, fmt.Sprintf("%s %d in %d file(s)", r.ID, hits, files))
		}
	}
	return "// linked repositories, text matches not listed above (--repo <id> lists them): " + strings.Join(parts, "; ")
}

// printLinkedSearch lists text matches in the named linked repository, or in
// every one for "all". Paths are relative to the primary root.
func printLinkedSearch(ws *workspace.Workspace, repo string, terms []string, opts textsearch.Options, filesOnly bool) int {
	var roots []workspace.Root
	for _, r := range ws.Linked() {
		if repo == "all" || r.ID == repo {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		fmt.Fprintf(os.Stderr, "search: no linked repository %q; linked: %s\n", repo, ws.Describe())
		return 1
	}
	for _, r := range roots {
		prefix := ws.RelPath(r.Path) + "/"
		for _, t := range terms {
			res := textsearch.Search(context.Background(), r.Path, t, opts)
			total := res.TotalHits
			if total < len(res.Hits) {
				total = len(res.Hits)
			}
			state := fmt.Sprintf("%d match(es)", total)
			switch {
			case res.TimedOut:
				state += ", search timed out: INCOMPLETE"
			case res.Truncated:
				state = fmt.Sprintf("at least %d match(es), first %d shown (--exhaustive lists all)", total, len(res.Hits))
			}
			fmt.Printf("// repo %s  %s  %q: %s\n", r.ID, strings.TrimSuffix(prefix, "/"), t, state)
			for _, p := range res.RejectedPaths {
				fmt.Printf("// path %q is not inside %s; ignored\n", p, r.ID)
			}
			seen := map[string]bool{}
			for _, h := range res.Hits {
				if filesOnly {
					if !seen[h.File] {
						seen[h.File] = true
						fmt.Println(prefix + h.File)
					}
					continue
				}
				fmt.Printf("%s%s:%d: %s\n", prefix, h.File, h.Line, h.Text)
			}
		}
	}
	return 0
}
