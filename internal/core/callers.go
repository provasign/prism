package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// Transitive callers and call paths (GAP-03). CLI-only, like test-plan:
// an MCP op waits for an A/B showing agents use it.

const (
	callersDefaultDepth = 3
	callersMaxDepth     = 6
	callersMaxNodes     = 200
	precisionTier       = 0.9 // edge confidence at or above: resolved
)

type callerNode struct {
	ID, Name, File string
	Line, Depth    int
	Parent         string // ID of the node this one calls
	Tier           string // resolved | by-name
	Via            string // "via interface X" when the hop is dispatch
	Test           bool
}

// rootSymbol resolves name (any lookup form: qualified, path:line, with
// file=) to one symbol ID, or the lookup's ambiguity as an error.
func (h *Handler) rootSymbol(ctx context.Context, name, file string) (id, label, path string, line int, err error) {
	args := map[string]any{"name": name}
	if file != "" {
		args["file"] = file
	}
	out, err := h.lookupSymbol(ctx, args, "")
	if err != nil {
		return "", "", "", 0, err
	}
	m, _ := out.(map[string]any)
	if m == nil || m["matched"] == false {
		return "", "", "", 0, fmt.Errorf("no symbol named %q", name)
	}
	if m["ambiguous"] != nil {
		var alts []string
		for _, c := range anyMaps(m["candidates"]) {
			alts = append(alts, fmt.Sprintf("\n  %v  (%v:%v)", c["qualifiedName"], c["filePath"], c["line"]))
		}
		return "", "", "", 0, fmt.Errorf("%q is ambiguous — %d candidates:%s\nre-run with a qualified name or path:line", name, len(alts), strings.Join(alts, ""))
	}
	switch s := m["symbol"].(type) {
	case grove.SymbolRecord:
		label = s.QualifiedName
		if label == "" {
			label = s.Name
		}
		return s.ID, label, normalizePath(s.FilePath), s.Span.Start, nil
	case map[string]any:
		return fmt.Sprint(s["id"]), fmt.Sprint(s["qualifiedName"]), normalizePath(fmt.Sprint(s["filePath"])), 0, nil
	}
	return "", "", "", 0, errors.New("lookup returned no symbol")
}

// incomingCalls returns who calls id, plus callers of each interface or
// base method id overrides, marked as dispatch.
func (h *Handler) incomingCalls(ctx context.Context, id string) ([]grove.EdgeRecord, map[string]string, error) {
	direct, err := h.Grove.Edges(ctx, id, "in", []string{"calls"})
	if err != nil {
		return nil, nil, err
	}
	via := map[string]string{}
	supers, err := h.Grove.Edges(ctx, id, "out", []string{"overrides", "implements"})
	if err == nil {
		for _, s := range supers {
			if s.ID == "" {
				continue
			}
			through, err := h.Grove.Edges(ctx, s.ID, "in", []string{"calls"})
			if err != nil {
				continue
			}
			for _, e := range through {
				if _, seen := via[e.ID]; !seen {
					via[e.ID] = "via " + s.Name
				}
			}
			direct = append(direct, through...)
		}
	}
	return direct, via, nil
}

func (h *Handler) toolCallers(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	depth := intArg(args, "depth", callersDefaultDepth)
	if depth < 1 {
		depth = 1
	}
	if depth > callersMaxDepth {
		depth = callersMaxDepth
	}
	rootID, label, file, line, err := h.rootSymbol(ctx, name, stringArg(args, "file", ""))
	if err != nil {
		return nil, err
	}
	nodes := []callerNode{}
	seen := map[string]bool{rootID: true}
	frontier := []string{rootID}
	capped := false
	for d := 1; d <= depth && len(frontier) > 0 && !capped; d++ {
		var next []string
		for _, id := range frontier {
			edges, via, err := h.incomingCalls(ctx, id)
			if err != nil {
				return nil, err
			}
			sort.Slice(edges, func(i, j int) bool {
				if edges[i].File != edges[j].File {
					return edges[i].File < edges[j].File
				}
				return edges[i].Line < edges[j].Line
			})
			for _, e := range edges {
				if e.ID == "" || seen[e.ID] {
					continue
				}
				if len(nodes) >= callersMaxNodes {
					capped = true
					break
				}
				seen[e.ID] = true
				tier := "resolved"
				if e.Confidence < precisionTier {
					tier = "by-name"
				}
				nodes = append(nodes, callerNode{ID: e.ID, Name: e.Name, File: normalizePath(e.File), Line: e.Line,
					Depth: d, Parent: id, Tier: tier, Via: via[e.ID], Test: e.TestDouble || e.TestLabel != ""})
				next = append(next, e.ID)
			}
		}
		frontier = next
	}
	out := map[string]any{"name": label, "file": file, "line": line, "depth": depth, "nodes": nodes, "count": len(nodes)}
	if capped {
		out["note"] = fmt.Sprintf("stopped at %d callers; narrow with --depth", callersMaxNodes)
	} else if len(frontier) > 0 && depth < callersMaxDepth {
		out["note"] = fmt.Sprintf("stopped at depth %d; --depth %d walks one level further", depth, min(depth+1, callersMaxDepth))
	}
	return out, nil
}

// toolCallPath finds the shortest chain of incoming calls from `to` back to
// `from`: how from reaches to.
func (h *Handler) toolCallPath(ctx context.Context, args map[string]any) (any, error) {
	fromName, toName := stringArg(args, "from", ""), stringArg(args, "to", "")
	if fromName == "" || toName == "" {
		return nil, errors.New("from and to are required")
	}
	fromID, fromLabel, _, _, err := h.rootSymbol(ctx, fromName, "")
	if err != nil {
		return nil, err
	}
	toID, toLabel, toFile, toLine, err := h.rootSymbol(ctx, toName, "")
	if err != nil {
		return nil, err
	}
	type hop struct {
		rec    grove.EdgeRecord
		parent string
		via    string
	}
	prev := map[string]hop{toID: {}}
	frontier := []string{toID}
	found := fromID == toID
	visited := 0
	for d := 0; d < callersMaxDepth*2 && len(frontier) > 0 && !found; d++ {
		var next []string
		for _, id := range frontier {
			edges, via, err := h.incomingCalls(ctx, id)
			if err != nil {
				return nil, err
			}
			for _, e := range edges {
				if e.ID == "" {
					continue
				}
				if _, ok := prev[e.ID]; ok {
					continue
				}
				prev[e.ID] = hop{rec: e, parent: id, via: via[e.ID]}
				visited++
				if e.ID == fromID {
					found = true
					break
				}
				next = append(next, e.ID)
			}
			if found || visited > callersMaxNodes*10 {
				break
			}
		}
		frontier = next
	}
	out := map[string]any{"from": fromLabel, "to": toLabel}
	if !found {
		out["path"] = []map[string]any{}
		out["note"] = fmt.Sprintf("no resolved call path from %s to %s within %d hops", fromLabel, toLabel, callersMaxDepth*2)
		return out, nil
	}
	var chain []map[string]any
	for id := fromID; id != toID; {
		hp := prev[id]
		tier := "resolved"
		if hp.rec.Confidence < precisionTier {
			tier = "by-name"
		}
		step := map[string]any{"name": hp.rec.Name, "file": normalizePath(hp.rec.File), "line": hp.rec.Line, "tier": tier}
		if hp.via != "" {
			step["via"] = hp.via
		}
		chain = append(chain, step)
		id = hp.parent
	}
	chain = append(chain, map[string]any{"name": toLabel, "file": toFile, "line": toLine})
	out["path"] = chain
	return out, nil
}

// renderCallersText prints the caller tree, deepest last.
func renderCallersText(out map[string]any) string {
	var b strings.Builder
	nodes, _ := out["nodes"].([]callerNode)
	fmt.Fprintf(&b, "// %v — %d transitive caller(s), depth %v\n", out["name"], len(nodes), out["depth"])
	if note, _ := out["note"].(string); note != "" {
		fmt.Fprintf(&b, "// %s\n", note)
	}
	children := map[string][]callerNode{}
	for _, n := range nodes {
		children[n.Parent] = append(children[n.Parent], n)
	}
	rootID := ""
	if len(nodes) > 0 {
		for _, n := range nodes {
			if n.Depth == 1 {
				rootID = n.Parent
				break
			}
		}
	}
	fmt.Fprintf(&b, "%v  %v:%v\n", out["name"], out["file"], out["line"])
	var walk func(id string, indent string)
	walk = func(id, indent string) {
		for _, n := range children[id] {
			extra := ""
			if n.Tier != "resolved" {
				extra += "  [" + n.Tier + "]"
			}
			if n.Via != "" {
				extra += "  [" + n.Via + "]"
			}
			if n.Test {
				extra += "  [test]"
			}
			fmt.Fprintf(&b, "%s<- %s  %s:%d%s\n", indent, n.Name, n.File, n.Line, extra)
			walk(n.ID, indent+"   ")
		}
	}
	walk(rootID, "  ")
	return b.String()
}

// renderCallPathText prints from -> ... -> to.
func renderCallPathText(out map[string]any) string {
	var b strings.Builder
	chain, _ := out["path"].([]map[string]any)
	if len(chain) == 0 {
		fmt.Fprintf(&b, "// %v -> %v: %v\n", out["from"], out["to"], out["note"])
		return b.String()
	}
	fmt.Fprintf(&b, "// %v -> %v: %d hop(s)\n", out["from"], out["to"], len(chain)-1)
	for i, s := range chain {
		arrow := "   "
		if i > 0 {
			arrow = "-> "
		}
		extra := ""
		if t, _ := s["tier"].(string); t != "" && t != "resolved" {
			extra += "  [" + t + "]"
		}
		if v, _ := s["via"].(string); v != "" {
			extra += "  [" + v + "]"
		}
		fmt.Fprintf(&b, "%s%v  %v:%v%s\n", arrow, s["name"], s["file"], s["line"], extra)
	}
	return b.String()
}
