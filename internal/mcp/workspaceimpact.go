package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/workspace"
)

// addWorkspaceConsumers extends a change-impact answer past this
// repository: the places in linked repositories whose compiler resolved
// them to a symbol of the change set.
func (h *Handler) addWorkspaceConsumers(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult) {
	if r == nil {
		return
	}
	var syms []grove.SymbolRecord
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Supers, r.Family} {
		syms = append(syms, group...)
	}
	h.attachLinkedConsumers(ctx, out, h.linkedKeys(syms))
}

// linkedKeys returns the cross-repository keys of syms, sorted and unique.
func (h *Handler) linkedKeys(syms []grove.SymbolRecord) []string {
	seen := map[string]bool{}
	var keys []string
	for _, s := range syms {
		for _, k := range grove.SymbolKeys(h.Root, s) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// attachLinkedConsumers sets workspaceConsumers and workspaceScope on out
// and returns how many sites it found.
func (h *Handler) attachLinkedConsumers(ctx context.Context, out map[string]any, keys []string) int {
	lines, scope, sites := h.linkedConsumers(ctx, keys)
	if len(lines) > 0 {
		out["workspaceConsumers"] = lines
	}
	if scope != "" {
		out["workspaceScope"] = scope
	}
	return sites
}

// linkedConsumers reads the linked repositories' indexes for references to
// keys. It returns the lines to show, a statement of what was searched, and
// the number of sites.
//
// The statement is never omitted when repositories are linked. A linked
// repository with no index, a stale one, a language with no cross-repository
// records, and repositories nobody linked are each named, because a consumer
// list that stops silently reads as "no other consumers".
func (h *Handler) linkedConsumers(ctx context.Context, keys []string) (lines []string, scope string, sites int) {
	linked := h.Workspace.Linked()
	// Declared but not on disk. They are named in every answer: a
	// repository dropped from the count without a word reads as searched.
	var absent []string
	if h.Workspace != nil {
		for _, r := range h.Workspace.Roots {
			if r.Missing {
				absent = append(absent, r.ID)
				lines = append(lines, fmt.Sprintf("// repo %s  %s  NOT SEARCHED: not checked out", r.ID, h.Workspace.DisplayPath(r)))
			}
		}
	}
	server := ""
	if h.Cfg != nil {
		server = h.Cfg.WorkspaceServer
	}
	if len(linked) == 0 && len(absent) == 0 && server == "" {
		return nil, "", 0
	}
	if len(keys) == 0 {
		return lines, "linked repositories not searched: cross-repository references are recorded for Go (functions, methods, types), TypeScript/JavaScript (calls) and Python (calls through imported names) only", 0
	}
	searched := 0
	var unread, staleRoots []string
	for _, root := range linked {
		rel := h.Workspace.RelPath(root.Path)
		stale, indexed := h.freshenLinked(ctx, root)
		var refs []grove.ExternalRef
		var err error
		if indexed {
			refs, err = grove.ReadExternalRefs(ctx, root.Path, keys)
		}
		if !indexed || err != nil {
			unread = append(unread, root.ID)
			lines = append(lines, fmt.Sprintf("// repo %s  %s  NOT SEARCHED: no index there", root.ID, rel))
			continue
		}
		searched++
		pin := requirePin(root.Path, keys)
		if stale != "" {
			staleRoots = append(staleRoots, root.ID)
			if pin != "" {
				pin += "  "
			}
			pin += stale
		}
		if len(refs) == 0 {
			if pin != "" {
				lines = append(lines, fmt.Sprintf("// repo %s  %s  %s: no references recorded", root.ID, rel, pin))
			}
			continue
		}
		header := fmt.Sprintf("// repo %s  %s", root.ID, rel)
		if pin != "" {
			header += "  " + pin
		}
		lines = append(lines, header)
		files := map[string][]string{}
		for _, ref := range refs {
			src, ok := files[ref.File]
			if !ok {
				if data, err := os.ReadFile(filepath.Join(root.Path, filepath.FromSlash(ref.File))); err == nil {
					src = strings.Split(string(data), "\n")
				}
				files[ref.File] = src
			}
			text := "(line not on disk: file changed since it was indexed)"
			if ref.Line >= 1 && ref.Line <= len(src) {
				text = strings.TrimSpace(strings.TrimRight(src[ref.Line-1], "\r"))
			}
			at := fmt.Sprintf("%s/%s:%d", rel, ref.File, ref.Line)
			if ref.Col > 0 {
				at += fmt.Sprintf(":%d", ref.Col)
			}
			tag := ""
			if isTestFilePath(ref.File) {
				tag = "  [test]"
			}
			switch {
			case strings.HasPrefix(ref.Key, "npm:"):
				// TypeScript records a callee by package and name, without
				// its owner type.
				tag += "  [matched by package and name]"
			case strings.HasPrefix(ref.Key, "py:"):
				// Python has no compiler pass: the call names something
				// the file imported under this module and name.
				tag += "  [import-scoped: matched by module and name]"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s  [in %s]%s", at, text, ref.From, tag))
			sites++
		}
	}
	scope = fmt.Sprintf("%d site(s) in %d of %d linked repositories searched; "+
		"functions and type declarations searched (a package-level variable naming it is not listed)", sites, searched, len(linked)+len(absent))
	if len(absent) > 0 {
		scope += "; not searched (not checked out): " + strings.Join(absent, ", ")
	}
	if len(staleRoots) > 0 {
		scope += "; answered from a STALE index: " + strings.Join(staleRoots, ", ")
	}
	if len(unread) > 0 {
		scope += "; not searched (no index): " + strings.Join(unread, ", ")
	}
	if server == "" {
		return lines, scope + "; repositories that are not linked are not covered", sites
	}
	remoteLines, remoteScope, remoteSites := h.serverConsumers(ctx, server, keys, linked)
	lines = append(lines, remoteLines...)
	return lines, scope + "; " + remoteScope, sites + remoteSites
}

// serverConsumers asks the shared link server for references in
// repositories that are not on this machine. Each answer describes a
// repository at the commit it uploaded, which the lines say; a server that
// cannot be reached is reported as not covered, never as no consumers.
func (h *Handler) serverConsumers(ctx context.Context, server string, keys []string, linked []workspace.Root) (lines []string, scope string, sites int) {
	req := linkserver.ConsumersRequest{Keys: keys}
	for _, root := range append([]string{h.Root}, rootPaths(linked)...) {
		for _, pkg := range grove.ManifestPackages(root) {
			req.ExcludePackages = append(req.ExcludePackages, pkg.Ecosystem+":"+pkg.Name)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := linkserver.AskConsumers(rctx, server, req)
	if err != nil {
		return nil, fmt.Sprintf("server %s NOT REACHED (%v): repositories only it knows are not covered", server, err), 0
	}
	for _, repo := range resp.Repos {
		sha := repo.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		header := fmt.Sprintf("// repo %s@%s", repo.Repo, sha)
		if repo.Ref != "" {
			header += " (" + repo.Ref + ")"
		}
		header += "  from the server, not checked out"
		if repo.Requires != "" {
			header += "  requires " + repo.Requires
		}
		if day, _, ok := strings.Cut(repo.Created, "T"); ok {
			header += "  uploaded " + day
		}
		lines = append(lines, header)
		for _, site := range repo.Sites {
			at := fmt.Sprintf("%s:%s:%d", repo.Repo, site.File, site.Line)
			if site.Col > 0 {
				at += fmt.Sprintf(":%d", site.Col)
			}
			tag := ""
			if site.Test {
				tag = "  [test]"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s  [in %s]%s", at, site.Text, site.From, tag))
			sites++
		}
	}
	scope = fmt.Sprintf("server %s: %d site(s) in %d of %d uploaded repositories, each at its uploaded commit; "+
		"repositories neither linked nor uploaded are not covered", server, sites, len(resp.Repos), resp.Searched)
	return lines, scope, sites
}

func rootPaths(roots []workspace.Root) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, r.Path)
	}
	return out
}

// requirePin says which version of the changed module a repository's
// manifests require, when they require it: the checkout searched may be
// ahead of or behind that pin.
func requirePin(root string, keys []string) string {
	for _, req := range grove.ManifestRequires(root) {
		if req.CoMember {
			continue
		}
		for _, k := range keys {
			eco, rest, _ := strings.Cut(k, ":")
			pkg, _, _ := strings.Cut(rest, "#")
			if eco != req.Ecosystem || !(pkg == req.Name || (eco == "go" && strings.HasPrefix(pkg, req.Name+"/"))) {
				continue
			}
			pin := "requires " + req.Name
			if req.Version != "" {
				pin += " " + req.Version
			}
			if req.LocalPath != "" {
				// go.work, a replace directive, file: — the version is
				// not what it is built against.
				pin += ", built against the local checkout"
			}
			return pin
		}
	}
	return ""
}
