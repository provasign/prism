package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// addWorkspaceConsumers extends a change-impact answer past this
// repository: the places in linked repositories whose compiler resolved
// them to a symbol of the change set.
func (h *Handler) addWorkspaceConsumers(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult) {
	if h.Workspace == nil || r == nil {
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
		if k, ok := grove.SymbolKey(h.Root, s); ok && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
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
	if h.Workspace == nil {
		return nil, "", 0
	}
	linked := h.Workspace.Linked()
	if len(linked) == 0 {
		return nil, "", 0
	}
	if len(keys) == 0 {
		return nil, "linked repositories not searched: cross-repository references are recorded for Go (functions, methods, types) and TypeScript/JavaScript (calls) only", 0
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
			if strings.HasPrefix(ref.Key, "npm:") {
				// TypeScript records a callee by package and name, without
				// its owner type.
				tag += "  [matched by package and name]"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s  [in %s]%s", at, text, ref.From, tag))
			sites++
		}
	}
	scope = fmt.Sprintf("%d site(s) in %d of %d linked repositories searched; "+
		"references inside functions only (a package-level declaration naming it is not listed)", sites, searched, len(linked))
	if len(staleRoots) > 0 {
		scope += "; answered from a STALE index: " + strings.Join(staleRoots, ", ")
	}
	if len(unread) > 0 {
		scope += "; not searched (no index): " + strings.Join(unread, ", ")
	}
	scope += "; repositories that are not linked are not covered"
	return lines, scope, sites
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
