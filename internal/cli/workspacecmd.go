package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/workspace"
)

// cmdWorkspace shows, adds, or removes the repositories linked to this one.
func cmdWorkspace(args []string) int {
	// Any workspace command is the user reviewing this repository's links:
	// the ones its prism.yaml declares come into force on this machine.
	if here, _ := cliRoot(""); here != "" {
		if own, err := config.LoadFromDir(here); err == nil {
			workspace.ConfirmLinks(here, own)
		}
	}
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return workspaceAdd(args[1:])
		case "remove", "rm":
			return workspaceRemove(args[1:])
		case "discover":
			return workspaceDiscover(args[1:])
		case "fetch":
			return workspaceFetch(args[1:])
		case "export", "push":
			return workspaceExport(args[0] == "push", args[1:])
		case "prune":
			for _, dir := range workspace.PruneClones() {
				fmt.Println("removed", dir)
			}
			return 0
		default:
			fmt.Fprintln(os.Stderr, "usage: prism workspace [add <path|git-url[@ref]> [--id <name>] | remove <id> | discover [--link] | fetch | prune | export [-o <file>] | push [--server <url>]]")
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
		if why := r.Unusable(); why != "" {
			state, index = why, "-"
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
	var targets []string
	var id string
	index := true
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--no-index":
			index = false
		case args[i] == "--id" && i+1 < len(args):
			i++
			id = args[i]
		case strings.HasPrefix(args[i], "--id="):
			id = strings.TrimPrefix(args[i], "--id=")
		case strings.HasPrefix(args[i], "-"):
			return rejectUnknownFlag("workspace add", args[i])
		default:
			targets = append(targets, args[i])
		}
	}
	if len(targets) == 0 || (id != "" && len(targets) > 1) {
		fmt.Fprintln(os.Stderr, "usage: prism workspace add <path|git-url[@ref]>... [--id <name>] [--no-index]   (--id names a single repository)")
		return 2
	}
	rc := 0
	for _, target := range targets {
		if r := workspaceAddOne(target, id, index); r != 0 {
			rc = r
		}
	}
	return rc
}

// ensureLinkedIndex builds the first index of a repository that was just
// linked. A linked repository with no index answers nothing, and the user
// asked for the link, so the index is part of the request. An existing
// index is left for the ordinary staleness check.
func ensureLinkedIndex(dir string) {
	if _, err := os.Stat(filepath.Join(dir, ".grove", "grove.db")); err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "indexing %s (first time; --no-index skips this)\n", dir)
	if err := grove.RefreshIndex(context.Background(), dir, workspace.RootsOf(dir)...); err != nil {
		fmt.Fprintf(os.Stderr, "workspace add: %s was linked but not indexed: %v\n", dir, err)
	}
}

func workspaceAddOne(target, id string, index bool) int {
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: prism workspace add <path> [--id <name>]")
		return 2
	}
	root, _ := cliRoot("")
	if workspace.IsGitURL(target) {
		return workspaceAddClone(root, target, id)
	}
	abs := mustAbs(target)
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "workspace add: %s is not a directory\n", target)
		return 1
	}
	// A plain folder that holds several checkouts declares them once for
	// all of them (workspace.Load reads an ancestor prism.yaml). Inside a
	// git work tree a path below the root is part of this repository.
	folder := !inGitWorkTree(root)
	if abs == root || isWithin(abs, root) || (isWithin(root, abs) && !folder) {
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
	// The other end, on this machine: abs now sees this repository too.
	workspace.LinkReverse(root, abs)
	if index {
		ensureLinkedIndex(abs)
	}
	refreshSteering(root)
	refreshSteering(abs)
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
	var other string
	if cfg, err := config.LoadFromDir(root); err == nil {
		for _, r := range cfg.WorkspaceRepos {
			if r.ID == id {
				p := r.Path
				if !filepath.IsAbs(p) {
					p = filepath.Join(root, filepath.FromSlash(p))
				}
				workspace.UnlinkReverse(root, p)
				other = p
			}
		}
	}
	defer func() {
		refreshSteering(root)
		if other != "" {
			refreshSteering(other)
		}
	}()
	for _, line := range strings.Split(string(data), "\n") {
		if workspaceLineID(line) == id {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		if workspace.UnlinkClone(root, id) {
			fmt.Printf("unlinked %s (its cache clone stays until `prism workspace prune`)\n", id)
			return 0
		}
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

// workspaceDiscover lists repositories on disk that the manifests relate to
// this one. With --link it adds the ones not yet linked.
func workspaceDiscover(args []string) int {
	link := false
	for _, a := range args {
		if a != "--link" {
			return rejectUnknownFlag("workspace discover", a)
		}
		link = true
	}
	root, _ := cliRoot("")
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace discover:", err)
		return 1
	}
	ws := workspace.Load(root, cfg)
	cands := workspace.Discover(root, ws)
	defer printUnlinked(root, workspace.Missing(root, ws, cands))
	if len(cands) == 0 {
		fmt.Println("// no related repositories found: no manifest here points at a directory, and no sibling directory declares or requires a matching package")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "REPOSITORY\tRELATION\tPACKAGE\tSTATE\tEVIDENCE")
	for _, c := range cands {
		state := "not linked"
		if c.Linked {
			state = "linked"
		}
		rel, _ := filepath.Rel(root, c.Path)
		kind := c.Relation
		if c.Explicit {
			kind += " (build points here)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", filepath.ToSlash(rel), kind, c.Package, state, c.Evidence)
	}
	tw.Flush()
	if !link {
		return 0
	}
	rc := 0
	for _, c := range cands {
		if c.Linked {
			continue
		}
		if r := workspaceAdd([]string{c.Path}); r != 0 {
			rc = r
		}
	}
	return rc
}

// offerWorkspaceLinks runs at the end of `prism init`: it records the
// repository in the per-user registry, looks for related checkouts, and
// links them only on a yes. mode is "auto" (link all found), "none" (do not
// look), or "" (ask when interactive, otherwise only report). Linking
// changes what every later answer covers, so it is never done silently.
func offerWorkspaceLinks(root, mode string) {
	workspace.Register(root)
	if cfg, err := config.LoadFromDir(root); err == nil {
		workspace.ConfirmLinks(root, cfg)
	}
	if mode == "none" {
		return
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return
	}
	var pending []workspace.Candidate
	for _, c := range workspace.Discover(root, workspace.Load(root, cfg)) {
		if !c.Linked {
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nrelated checkouts found (%d):\n", len(pending))
	for _, c := range pending {
		rel, _ := filepath.Rel(root, c.Path)
		fmt.Fprintf(os.Stderr, "  %s  %s  — %s\n", filepath.ToSlash(rel), c.Relation, c.Evidence)
	}
	link := mode == "auto"
	if mode == "" && isInteractive() {
		fmt.Fprint(os.Stderr, "Link them, so lookup, read and change-impact cover them? [y/N]: ")
		line, _ := initStdin().ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			link = true
		}
	}
	if !link {
		fmt.Fprintln(os.Stderr, "not linked; `prism workspace discover --link` links them later")
		return
	}
	wd, _ := os.Getwd()
	if os.Chdir(root) == nil {
		for _, c := range pending {
			workspaceAdd([]string{c.Path})
		}
		_ = os.Chdir(wd)
	}
}

// steeringFor is the steering block for a repository: the interface's
// instructions, plus one line naming its linked repositories on the CLI.
// Over MCP the server says the same at initialize, from the links as they
// are when the session starts, so the file carries nothing that can go out
// of date.
func steeringFor(root, iface string) string {
	if iface != interfaceCLI {
		return steeringBlock()
	}
	steering := cliSteeringInstructionsForOS(runtime.GOOS)
	if line := workspaceSteeringLine(root); line != "" {
		steering = strings.Replace(steering, "<!-- prism:end -->", line+"\n\n<!-- prism:end -->", 1)
	}
	return steering
}

// workspaceSteeringLine is the one line the CLI steering block carries when
// repositories are linked. Steering is paid for at every session start, so
// it is a single sentence, absent without a workspace.
func workspaceSteeringLine(root string) string {
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return ""
	}
	ws := workspace.Load(root, cfg)
	if ws == nil || len(ws.Linked()) == 0 {
		return ""
	}
	return "Linked repositories: " + ws.Describe() + ". change-impact, verify and test-plan also list their " +
		"consumers; lookup and read reach their source; search takes --repo <id>."
}

// refreshSteering rewrites the steering block of a repository Prism was set
// up in, after its links changed: an agent told about yesterday's links
// searches the wrong places. A repository never initialised (no recorded
// harnesses) is left alone.
func refreshSteering(root string) {
	yaml := filepath.Join(root, "prism.yaml")
	harnesses := readRecordedHarnesses(yaml)
	if len(harnesses) == 0 {
		return
	}
	iface := readRecordedInterface(yaml)
	if iface == "" {
		iface = interfaceMCP
	}
	if iface != interfaceCLI {
		return // MCP steering carries no links
	}
	writeSteeringBlock(root, harnesses, true, steeringFor(root, iface))
}

// workspaceAddClone fetches a remote repository at a ref into the cache,
// indexes it, and links it to this repository on this machine only.
func workspaceAddClone(root, target, id string) int {
	repo, ref := workspace.SplitURLRef(target)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(strings.TrimRight(repo, "/")), ".git")
	}
	link, err := fetchAndIndexClone(repo, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace add:", err)
		return 1
	}
	link.ID = id
	if err := workspace.LinkClone(root, link); err != nil {
		fmt.Fprintln(os.Stderr, "workspace add:", err)
		return 1
	}
	fmt.Printf("linked %s -> %s (cache clone at %s, read-only reference)\n", id, link.Path, shortSHA(link.SHA))
	return 0
}

func fetchAndIndexClone(repo, ref string) (workspace.CacheLink, error) {
	dir, sha, err := workspace.FetchClone(repo, ref)
	if err != nil {
		return workspace.CacheLink{}, err
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".grove", "grove.db")); statErr != nil {
		fmt.Fprintf(os.Stderr, "indexing %s at %s (once per commit)\n", repo, shortSHA(sha))
		if err := grove.RefreshIndex(context.Background(), dir); err != nil {
			return workspace.CacheLink{}, fmt.Errorf("index %s: %w", dir, err)
		}
	}
	return workspace.CacheLink{URL: repo, Ref: ref, SHA: sha, Path: dir}, nil
}

// workspaceFetch re-resolves every cache clone's ref. A branch that moved
// gets a new directory and index; the link is pointed at it.
func workspaceFetch(args []string) int {
	if len(args) > 0 {
		return rejectUnknownFlag("workspace fetch", args[0])
	}
	root, _ := cliRoot("")
	rc := 0
	for _, l := range workspace.CacheLinks(root) {
		next, err := fetchAndIndexClone(l.URL, l.Ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace fetch: %s: %v\n", l.ID, err)
			rc = 1
			continue
		}
		if next.SHA == l.SHA {
			fmt.Printf("%s: unchanged at %s\n", l.ID, shortSHA(l.SHA))
			continue
		}
		next.ID = l.ID
		if err := workspace.LinkClone(root, next); err != nil {
			fmt.Fprintf(os.Stderr, "workspace fetch: %s: %v\n", l.ID, err)
			rc = 1
			continue
		}
		fmt.Printf("%s: %s -> %s\n", l.ID, shortSHA(l.SHA), shortSHA(next.SHA))
	}
	return rc
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// workspaceExport writes this repository's link bundle, or with push
// uploads it to the link server. The bundle holds locations, one source
// line per reference and declaration signatures; no source bodies.
func workspaceExport(push bool, args []string) int {
	var outFile, server, repo, ref string
	for i := 0; i < len(args); i++ {
		var dst *string
		switch args[i] {
		case "-o", "--output":
			dst = &outFile
		case "--server":
			dst = &server
		case "--repo":
			dst = &repo
		case "--ref":
			dst = &ref
		default:
			return rejectUnknownFlag("workspace export", args[i])
		}
		if i+1 >= len(args) {
			fmt.Fprintf(os.Stderr, "workspace: %s needs a value\n", args[i])
			return 2
		}
		i++
		*dst = args[i]
	}
	root, _ := cliRoot("")
	// The bundle must describe the tree on disk: bring the index up to date.
	if err := grove.RefreshIndex(context.Background(), root); err != nil {
		fmt.Fprintln(os.Stderr, "workspace: index:", err)
		return 1
	}
	b, err := linkserver.Build(context.Background(), root, repo, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace:", err)
		return 1
	}
	if !push {
		data, _ := json.MarshalIndent(b, "", " ")
		if outFile == "" {
			fmt.Println(string(data))
			return 0
		}
		if err := os.WriteFile(outFile, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "workspace export:", err)
			return 1
		}
		fmt.Printf("wrote %s: %s@%s, %d reference(s), %d declaration(s)\n", outFile, b.Repo, shortSHA(b.SHA), len(b.Refs), len(b.Defs))
		return 0
	}
	if server == "" {
		if cfg, err := config.LoadFromDir(root); err == nil {
			server = cfg.WorkspaceServer
		}
	}
	if server == "" {
		fmt.Fprintln(os.Stderr, "workspace push: no server; pass --server <url> or set workspace_server in prism.yaml")
		return 2
	}
	if b.Dirty {
		fmt.Fprintln(os.Stderr, "workspace push: the tree has uncommitted changes, so its commit does not describe it; commit first")
		return 1
	}
	if err := linkserver.Push(context.Background(), server, b); err != nil {
		fmt.Fprintln(os.Stderr, "workspace push:", err)
		return 1
	}
	fmt.Printf("pushed %s@%s (%s) to %s: %d reference(s), %d declaration(s)\n", b.Repo, shortSHA(b.SHA), b.Ref, server, len(b.Refs), len(b.Defs))
	return 0
}

// cmdLinkServer runs the shared link server over a directory of bundles.
func cmdLinkServer(args []string) int {
	dir, port := "", 8890
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "link-server: --dir needs a value")
				return 2
			}
			i++
			dir = args[i]
		case "--port":
			p, err := positiveIntFlag("link-server", args, i)
			if err != nil {
				return usageError(err)
			}
			port = p
			i++
		default:
			return rejectUnknownFlag("link-server", args[i])
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "usage: prism link-server --dir <bundle-store> [--port N]   (PRISM_LINK_TOKEN sets the required bearer token)")
		return 2
	}
	store, err := linkserver.Open(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "link-server:", err)
		return 1
	}
	token := linkserver.Token()
	if token == "" {
		fmt.Fprintln(os.Stderr, "link-server: WARNING no PRISM_LINK_TOKEN set; anyone who can reach this port can read and upload bundles")
	}
	addr := fmt.Sprintf(":%d", port)
	fmt.Fprintf(os.Stderr, "prism link server on %s: %s\n", addr, store.Describe())
	if err := http.ListenAndServe(addr, store.Handler(token)); err != nil {
		fmt.Fprintln(os.Stderr, "link-server:", err)
		return 1
	}
	return 0
}

// printUnlinked lists declared dependencies with no checkout on this
// machine. Nothing here was searched; a repository that matters can be
// brought in with `prism workspace add <git-url>[@ref]`.
func printUnlinked(root string, missing []workspace.Unlinked) {
	if len(missing) == 0 {
		return
	}
	const show = 20
	fmt.Printf("\n// required, no checkout found on this machine (%d): not linked, not searched\n", len(missing))
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for i, m := range missing {
		if i == show {
			break
		}
		fmt.Fprintf(tw, "unlinked: %s:%s\t%s\t%s\n", m.Ecosystem, m.Name, m.Version, m.URL)
	}
	tw.Flush()
	if len(missing) > show {
		fmt.Printf("// … and %d more\n", len(missing)-show)
	}
}
