package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/workspace"
)

// cmdWorkspace shows, adds, or removes the repositories linked to this one.
func cmdWorkspace(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return workspaceAdd(args[1:])
		case "remove", "rm":
			return workspaceRemove(args[1:])
		default:
			fmt.Fprintln(os.Stderr, "usage: prism workspace [add <path> [--id <name>] | remove <id>]")
			return 2
		}
	}
	root, _ := cliRoot("")
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace:", err)
		return 1
	}
	self := workspace.Inspect(workspace.Root{ID: filepath.Base(root), Path: root})
	fmt.Printf("// workspace of %s  %s\n", root, refHead(self))
	ws := workspace.Load(root, cfg)
	if ws == nil {
		fmt.Println("// no linked repositories")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPATH\tBRANCH@COMMIT\tINDEX\tLINKED BY")
	for _, r := range ws.Roots {
		s := workspace.Inspect(r)
		index := "not indexed"
		switch {
		case s.Missing:
			index = "-"
		case s.Indexed:
			index = s.IndexedAt.Format("2006-01-02 15:04")
		}
		state := refHead(s)
		if s.Missing {
			state = "not checked out"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, ws.RelPath(r.Path), state, index, r.Evidence)
	}
	tw.Flush()
	return 0
}

func refHead(s workspace.Status) string {
	if s.Head == "" {
		return "(not a git work tree)"
	}
	ref := s.Ref
	if ref == "" {
		ref = "detached"
	}
	out := ref + "@" + s.Head
	if s.Dirty {
		out += " (uncommitted changes)"
	}
	return out
}

func workspaceAdd(args []string) int {
	var target, id string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--id" && i+1 < len(args):
			i++
			id = args[i]
		case strings.HasPrefix(args[i], "--id="):
			id = strings.TrimPrefix(args[i], "--id=")
		case strings.HasPrefix(args[i], "-"):
			return rejectUnknownFlag("workspace add", args[i])
		case target == "":
			target = args[i]
		default:
			fmt.Fprintln(os.Stderr, "usage: prism workspace add <path> [--id <name>]")
			return 2
		}
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: prism workspace add <path> [--id <name>]")
		return 2
	}
	root, _ := cliRoot("")
	abs := mustAbs(target)
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "workspace add: %s is not a directory\n", target)
		return 1
	}
	if isWithin(root, abs) || isWithin(abs, root) {
		fmt.Fprintf(os.Stderr, "workspace add: %s overlaps this repository (%s); link a separate checkout\n", target, root)
		return 1
	}
	if id == "" {
		id = filepath.Base(abs)
	}
	if strings.ContainsAny(id, "=# \t") {
		fmt.Fprintf(os.Stderr, "workspace add: id %q may not contain spaces, '=' or '#'\n", id)
		return 1
	}
	yaml := filepath.Join(root, "prism.yaml")
	data, err := os.ReadFile(yaml)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace add: no prism.yaml in %s; run prism init there first\n", root)
		return 1
	}
	cfg, _ := config.LoadFromDir(root)
	for _, r := range cfg.WorkspaceRepos {
		if r.ID == id {
			fmt.Fprintf(os.Stderr, "workspace add: id %q is already linked (%s)\n", id, r.Path)
			return 1
		}
		p := r.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, filepath.FromSlash(p))
		}
		if mustAbs(p) == abs {
			fmt.Fprintf(os.Stderr, "workspace add: %s is already linked as %q\n", target, r.ID)
			return 1
		}
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		rel = abs
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += fmt.Sprintf("workspace_repo: %s=%s\n", id, filepath.ToSlash(rel))
	if err := os.WriteFile(yaml, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "workspace add:", err)
		return 1
	}
	fmt.Printf("linked %s -> %s\n", id, filepath.ToSlash(rel))
	return 0
}

func workspaceRemove(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: prism workspace remove <id>")
		return 2
	}
	id := args[0]
	root, _ := cliRoot("")
	yaml := filepath.Join(root, "prism.yaml")
	data, err := os.ReadFile(yaml)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace remove:", err)
		return 1
	}
	var kept []string
	removed := false
	for _, line := range strings.Split(string(data), "\n") {
		if workspaceLineID(line) == id {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "workspace remove: %q is not linked in %s\n", id, yaml)
		return 1
	}
	if err := os.WriteFile(yaml, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "workspace remove:", err)
		return 1
	}
	fmt.Printf("unlinked %s\n", id)
	return 0
}

// workspaceLineID returns the id a `workspace_repo:` line declares, or "".
func workspaceLineID(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "workspace_repo:") {
		return ""
	}
	v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "workspace_repo:")), `"'`)
	if j := strings.Index(v, " #"); j >= 0 {
		v = strings.TrimSpace(v[:j])
	}
	if j := strings.IndexByte(v, '='); j >= 0 {
		return strings.TrimSpace(v[:j])
	}
	return filepath.Base(filepath.Clean(v))
}
