package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/mcp"
	"github.com/provasign/prism/internal/workspace"
)

// openRepoGraphClient is the graph-open boundary for a CLI session. Tests
// count calls here to guard against reopening the graph once per batch name.
var openRepoGraphClient = newClient

// repoToolSession owns one client and handler for one CLI invocation.
// A batch shares this session, including the handler's index and graph caches.
type repoToolSession struct {
	root, selected, sub, repo string
	client                    *grove.Client
	handler                   *mcp.Handler
}

func newRepoToolSession(dir, repo string) (*repoToolSession, error) {
	root, sub := cliRoot(dir)
	selected := root
	if repo != "" {
		if repo == "all" {
			return nil, fmt.Errorf("--repo needs one repository id; graph queries do not merge unrelated graphs")
		}
		cfg, err := config.LoadFromDir(root)
		if err != nil {
			return nil, err
		}
		ws := workspace.Load(root, cfg)
		selected, err = selectRepoRoot(root, repo, ws)
		if err != nil {
			return nil, err
		}
		if ws != nil {
			for _, r := range ws.Roots {
				if r.ID == repo && r.Cache {
					return nil, fmt.Errorf("repository %q is a read-only cache clone; graph queries require a writable checkout. Use prism read <file> --repo %s for cached source", repo, repoShellQuote(repo))
				}
			}
		}
		// Check before opening Grove: opening a missing store creates .grove.
		info, err := os.Stat(filepath.Join(selected, ".grove", "grove.db"))
		if os.IsNotExist(err) || err == nil && (!info.Mode().IsRegular() || info.Size() == 0) {
			return nil, repoIndexRequired(repo, selected)
		}
		if err != nil {
			return nil, fmt.Errorf("repository %q index: %w", repo, err)
		}
		sub = ""
	}
	cfg, client, err := openRepoGraphClient(selected)
	if err != nil {
		return nil, err
	}
	if repo != "" {
		needsIndex, checkErr := client.IndexNeedsRefresh(context.Background())
		if checkErr != nil {
			client.Shutdown()
			return nil, fmt.Errorf("repository %q index: %w", repo, checkErr)
		}
		if needsIndex {
			client.Shutdown()
			return nil, repoIndexRequired(repo, selected)
		}
	} else if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		client.Shutdown()
		return nil, err
	}
	noteRoot(root, sub)
	return &repoToolSession{root: root, selected: selected, sub: sub, repo: repo,
		client: client, handler: mcp.NewHandler(cfg, selected, client)}, nil
}

func repoIndexRequired(repo, root string) error {
	return fmt.Errorf("repository %q has no current, complete index; run `prism index %s` explicitly, then retry", repo, repoShellQuote(root))
}

func repoShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (s *repoToolSession) close() { s.client.Shutdown() }

func (s *repoToolSession) invoke(tool string, args map[string]any) (out any, text string, rendered bool, err error) {
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	if s.repo == "" {
		rebaseArgs(call, s.sub)
	} else if file, ok := call["file"].(string); ok && file != "" {
		absolute := file
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(s.root, file)
		}
		if rel, e := filepath.Rel(s.selected, absolute); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			call["file"] = filepath.ToSlash(rel)
		}
	}
	out, err = s.handler.Invoke(tool, call)
	if s.repo == "" {
		if err != nil {
			if hint := graphRepoHint(tool, call, s.handler.Workspace); hint != "" {
				err = fmt.Errorf("%w; %s", err, hint)
			}
		}
	}
	if err != nil {
		return nil, "", false, err
	}
	text, rendered = s.handler.RenderToolText(true, tool, call, out)
	if s.repo == "" && tool == "prism_query" && rendered && strings.Contains(text, "no symbols matched") {
		// On stdout with the miss, not stderr: an agent that pipes or
		// discards stderr otherwise never learns the symbol may live in a
		// linked repository (it did on the 11-15 ice-turn-context cell).
		if hint := graphRepoHint(tool, call, s.handler.Workspace); hint != "" {
			text += "// " + hint + "\n"
		}
	}
	if s.repo != "" {
		out = map[string]any{"repository": s.repo, "root": s.selected, "result": out}
		if rendered {
			text = fmt.Sprintf("// repository %s (%s); paths below are relative to this root\n", s.repo, s.selected) + text
		}
	}
	return
}

// invokeRepoToolText is the single-request form of the shared CLI session.
func invokeRepoToolText(dir, repo, tool string, args map[string]any) (any, string, bool, error) {
	s, err := newRepoToolSession(dir, repo)
	if err != nil {
		return nil, "", false, err
	}
	defer s.close()
	return s.invoke(tool, args)
}

func graphRepoHint(tool string, args map[string]any, ws *workspace.Workspace) string {
	if ws == nil || len(ws.Linked()) == 0 {
		return ""
	}
	var commands []string
	for _, r := range ws.Linked() {
		if len(commands) == 3 {
			break
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		if tool == "prism_query" {
			var terms []string
			switch v := args["terms"].(type) {
			case []string:
				terms = v
			case []any:
				for _, x := range v {
					if s, ok := x.(string); ok {
						terms = append(terms, s)
					}
				}
			}
			commands = append(commands, "prism query --terms "+quote(strings.Join(terms, ","))+" --repo "+quote(r.ID)+" --format text")
		} else if tool == "prism_change_impact" {
			q, _ := args["query"].(string)
			commands = append(commands, "prism change-impact "+quote(q)+" --repo "+quote(r.ID)+" --format text")
		}
	}
	if len(commands) == 0 {
		return ""
	}
	return "this call uses the current repository's graph; for a linked definition run " + strings.Join(commands, " or ")
}

func selectRepoRoot(root, repo string, ws *workspace.Workspace) (string, error) {
	selected := ""
	if repo == filepath.Base(root) {
		selected = root
	}
	if ws != nil {
		for _, r := range ws.Roots {
			if r.ID != repo {
				continue
			}
			if why := r.Unusable(); why != "" {
				return "", fmt.Errorf("repository %q NOT SEARCHED: %s", repo, why)
			}
			selected = r.Path
			break
		}
	}
	if selected == "" {
		linked := "none"
		if ws != nil {
			linked = ws.Describe()
		}
		return "", fmt.Errorf("no linked repository %q; linked: %s", repo, linked)
	}
	return selected, nil
}

// readRepoPaths keeps reads on the existing graph-free, checked file path.
// Output paths stay relative to the original project root, including batches.
func readRepoPaths(dir, repo string, files []string) (string, []string, error) {
	root, _ := cliRoot(dir)
	if repo == "all" {
		return "", nil, fmt.Errorf("--repo needs one repository id for read")
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return "", nil, err
	}
	selected, err := selectRepoRoot(root, repo, workspace.Load(root, cfg))
	if err != nil {
		return "", nil, err
	}
	paths := make([]string, len(files))
	for i, file := range files {
		absolute := file
		if !filepath.IsAbs(absolute) {
			// A copied linked path is relative to the original project root.
			absolute = filepath.Join(root, file)
			if rel, e := filepath.Rel(selected, absolute); e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				absolute = filepath.Join(selected, file)
			}
		}
		if rel, e := filepath.Rel(selected, absolute); e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", nil, fmt.Errorf("file %q is outside selected repository %q", file, repo)
		}
		path, e := filepath.Rel(root, absolute)
		if e != nil {
			path = absolute
		}
		paths[i] = filepath.ToSlash(path)
	}
	return root, paths, nil
}

// A copied linked path already identifies the repository, even when the caller
// omits --repo. Do not let a same-named local symbol win over that explicit path.
func repoForFileHint(dir, file string) (string, error) {
	root, _ := cliRoot(dir)
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return "", err
	}
	ws := workspace.Load(root, cfg)
	if ws == nil {
		return "", nil
	}
	absolute := file
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, file)
	}
	var selected workspace.Root
	for _, r := range ws.Roots {
		rel, e := filepath.Rel(r.Path, absolute)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(r.Path) > len(selected.Path) {
			selected = r
		}
	}
	if selected.ID == "" {
		return "", nil
	}
	if why := selected.Unusable(); why != "" {
		return "", fmt.Errorf("repository %q NOT SEARCHED: %s", selected.ID, why)
	}
	return selected.ID, nil
}
