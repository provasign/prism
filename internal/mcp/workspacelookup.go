package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// lookupLinked answers a name this repository does not define from the
// repositories linked to it. It reads their existing indexes only; a linked
// repository with no index is returned in unread so the miss can name it,
// because "not looked at" must not read as "not there".
//
// A hit keeps the usual lookup shape. Its file path is relative to this
// root ("../grove/pkg/x.go"), which the agent's own file tools accept.
func (h *Handler) lookupLinked(ctx context.Context, q lookupQuery, fields []string) (out map[string]any, unread []string) {
	if h.Workspace == nil || len(q.segs) == 0 {
		return nil, nil
	}
	leaf := q.name()
	names := []string{leaf, strings.Join(q.segs, ".")}
	if n := len(q.segs); n > 2 {
		names = append(names, strings.Join(q.segs[n-2:], "."))
	}
	type hit struct {
		sym   grove.SymbolRecord
		repo  string
		score int
	}
	var hits []hit
	staleBy := map[string]string{}
	for _, r := range h.Workspace.Linked() {
		stale, indexed := h.freshenLinked(ctx, r)
		if !indexed {
			unread = append(unread, r.ID+" ("+h.Workspace.RelPath(r.Path)+")")
			continue
		}
		staleBy[r.ID] = stale
		syms, err := grove.ReadSymbolsNamed(ctx, r.Path, names)
		if err != nil {
			unread = append(unread, r.ID+" ("+h.Workspace.RelPath(r.Path)+")")
			continue
		}
		for _, s := range syms {
			score := 0
			switch {
			case len(q.segs) == 1 && s.QualifiedName == leaf:
				score = 3 // a bare name that is a top-level symbol
			case len(q.segs) > 1 && contains(names[1:], s.QualifiedName):
				score = 3 // Type.member as asked
			case s.Name == leaf && len(q.segs) == 1:
				score = 2 // a bare name that is some type's member
			case s.Name == leaf:
				score = 1 // the qualifier named a package or module, not an owner type
			default:
				continue
			}
			if isTestFilePath(s.FilePath) {
				score--
			}
			s.FilePath = h.Workspace.RelPath(filepath.Join(r.Path, filepath.FromSlash(s.FilePath)))
			hits = append(hits, hit{sym: s, repo: r.ID, score: score})
		}
	}
	if len(hits) == 0 {
		return nil, unread
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	best := hits[0]
	if len(fields) > 0 {
		out = projectSymbol(h.Root, best.sym, fields)
	} else {
		out = map[string]any{"symbol": best.sym, "content": h.sourceText(best.sym)}
	}
	note := fmt.Sprintf("not defined in this repository; shown from linked repository %s", best.repo)
	if stale := staleBy[best.repo]; stale != "" {
		note += " — " + stale
	}
	if len(unread) > 0 {
		note += "; linked repositories with no index, not searched: " + strings.Join(unread, ", ")
	}
	out["note"] = note
	var also []string
	for _, o := range hits[1:] {
		if len(also) == lookupAlsoNamedMax {
			also = append(also, "…")
			break
		}
		also = append(also, lookupCandidateLabel(o.sym))
	}
	if len(also) > 0 {
		out["alsoNamed"] = also
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
