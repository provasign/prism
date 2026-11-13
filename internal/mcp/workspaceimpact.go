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
// repository: the call sites in linked repositories whose compiler resolved
// them to a symbol of the change set. It reads their existing indexes only.
//
// The answer always says what was and was not searched. A linked repository
// with no index, a language with no cross-repository records, and
// repositories nobody linked are each named, because a consumer list that
// stops silently reads as "no other consumers".
func (h *Handler) addWorkspaceConsumers(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult) {
	if h.Workspace == nil || r == nil {
		return
	}
	linked := h.Workspace.Linked()
	if len(linked) == 0 {
		return
	}
	seen := map[string]bool{}
	var keys []string
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Supers, r.Family} {
		for _, s := range group {
			if k, ok := grove.GoSymbolKey(h.Root, s); ok && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		out["workspaceScope"] = "linked repositories not searched: cross-repository references are recorded for Go functions and methods only"
		return
	}
	sort.Strings(keys)
	var lines []string
	searched, sites := 0, 0
	var unread []string
	for _, root := range linked {
		rel := h.Workspace.RelPath(root.Path)
		refs, err := grove.ReadExternalRefs(ctx, root.Path, keys)
		if err != nil {
			unread = append(unread, root.ID)
			lines = append(lines, fmt.Sprintf("// repo %s  %s  NOT SEARCHED: no index there", root.ID, rel))
			continue
		}
		searched++
		pin := requirePin(root.Path, keys)
		if len(refs) == 0 {
			if pin != "" {
				lines = append(lines, fmt.Sprintf("// repo %s  %s  %s: no calls recorded", root.ID, rel, pin))
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
			lines = append(lines, fmt.Sprintf("  %s: %s  [in %s]", at, text, ref.From))
			sites++
		}
	}
	out["workspaceConsumers"] = lines
	scope := fmt.Sprintf("%d call site(s) in %d of %d linked repositories searched, as last indexed there; "+
		"calls only (a function passed as a value is not listed)", sites, searched, len(linked))
	if len(unread) > 0 {
		scope += "; not searched (no index): " + strings.Join(unread, ", ")
	}
	scope += "; repositories that are not linked are not covered"
	out["workspaceScope"] = scope
}

// requirePin says which version of the changed module a repository's
// manifests require, when they require it: the checkout searched may be
// ahead of or behind that pin.
func requirePin(root string, keys []string) string {
	for _, req := range grove.ManifestRequires(root) {
		if req.Ecosystem != "go" || req.CoMember {
			continue
		}
		for _, k := range keys {
			pkg := strings.TrimPrefix(k[:strings.IndexByte(k, '#')], "go:")
			if pkg == req.Name || strings.HasPrefix(pkg, req.Name+"/") {
				pin := "requires " + req.Name
				if req.Version != "" {
					pin += " " + req.Version
				}
				if req.LocalPath != "" {
					// go.work or a replace directive: the version is not
					// what it is built against.
					pin += ", built against the local checkout"
				}
				return pin
			}
		}
	}
	return ""
}
