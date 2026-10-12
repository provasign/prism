package mcp

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// overloadCallerTags maps each caller of an overloaded declaration to the
// overload(s) it calls ("value(String)"), so an agent migrating overloads
// (gson: JsonWriter.value -> stringValue/booleanValue/numberValue) reads the
// classification from the answer instead of breaking the build to find it.
// Each overload is queried on its own (one overload's answer holds only the
// calls the compiler resolved to it). Any overload that does not resolve
// cleanly to exactly its declaration leaves the answer untagged: a partial
// tagging would read as complete.
func (h *Handler) overloadCallerTags(ctx context.Context, r *grove.ChangeImpactResult) map[string][]string {
	if h.Grove == nil || r == nil {
		return nil
	}
	groups := map[string][]grove.SymbolRecord{}
	for _, d := range r.Declarations {
		groups[d.FilePath+"\x00"+displayQN(d)] = append(groups[d.FilePath+"\x00"+displayQN(d)], d)
	}
	var overloads []grove.SymbolRecord
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sigs := map[string]bool{}
		for _, d := range g {
			sigs[d.Signature] = true
		}
		if len(sigs) == len(g) {
			overloads = append(overloads, g...)
		}
	}
	const maxOverloads = 16
	if len(overloads) < 2 || len(overloads) > maxOverloads {
		return nil
	}
	tags := map[string][]string{}
	for _, d := range overloads {
		params, ok := signatureParamTypes(d.Signature, d.Language)
		if !ok {
			return nil
		}
		label := d.Name + "(" + strings.Join(params, ", ") + ")"
		one, err := h.Grove.ChangeImpactScoped(ctx, displayQN(d)+"("+strings.Join(params, ",")+")", d.FilePath)
		if err != nil || one == nil || len(one.Declarations) != 1 || one.Declarations[0].Span.Start != d.Span.Start {
			return nil
		}
		for _, c := range one.Callers {
			k := overloadCallerKey(c.FilePath, c.Span.Start, displayQN(c))
			tags[k] = append(tags[k], label)
		}
	}
	for k := range tags {
		sort.Strings(tags[k])
	}
	return tags
}

func overloadCallerKey(file string, line int, qn string) string {
	return file + "\x00" + strconv.Itoa(line) + "\x00" + qn
}
