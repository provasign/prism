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
func (h *Handler) overloadCallerTags(ctx context.Context, r *grove.ChangeImpactResult) (map[string][]string, *overloadLines) {
	if h.Grove == nil || r == nil {
		return nil, nil
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
		return nil, nil
	}
	tags := map[string][]string{}
	callers := map[string]grove.SymbolRecord{}
	compiled := map[string]map[string][]int{} // caller -> overload -> compiler call lines
	for _, d := range overloads {
		params, ok := signatureParamTypes(d.Signature, d.Language)
		if !ok {
			return nil, nil
		}
		label := d.Name + "(" + strings.Join(params, ", ") + ")"
		one, err := h.Grove.ChangeImpactScoped(ctx, displayQN(d)+"("+strings.Join(params, ",")+")", d.FilePath)
		if err != nil || one == nil || len(one.Declarations) != 1 || one.Declarations[0].Span.Start != d.Span.Start {
			return nil, nil
		}
		for _, c := range one.Callers {
			k := overloadCallerKey(c.FilePath, c.Span.Start, displayQN(c))
			tags[k] = append(tags[k], label)
			callers[k] = c
			if ls := one.CallerLines[c.ID]; len(ls) > 0 {
				if compiled[k] == nil {
					compiled[k] = map[string][]int{}
				}
				compiled[k][label] = ls
			}
		}
	}
	for k := range tags {
		sort.Strings(tags[k])
	}
	arity := map[int]bool{}
	for _, d := range overloads {
		if params, ok := signatureParamTypes(d.Signature, d.Language); ok {
			arity[len(params)] = true
		}
	}
	return tags, overloadCallLines(overloads[0].Name, arity, tags, callers, compiled)
}

// overloadLines places each call line of an overloaded name under the
// overload it calls. A caller the compiler resolved to one overload gives
// its lines exactly; a caller that calls several (TypeAdapters'
// write(boolean) and write(long) in one body) gives "mixed" lines naming
// its overloads, which the agent resolves by reading them. A line claimed
// exactly (a lambda's call) is never also listed as mixed (its enclosing
// method, which javac credits with the lambda's calls too).
type overloadLines struct {
	exact map[string]map[string][]int // overload -> file -> lines
	mixed map[string]map[int]string   // file -> line -> "a | b"
	calls int
	// guessed: file -> line -> overloads, for callers reached only by name
	// where the compiler resolved the others.
	guessed map[string]map[int]string
	// fromCompiler: every exact line came from the compiler's per-call
	// resolution.
	fromCompiler bool
}

// arity holds the overloads' parameter counts: a same-named call with
// another count (an annotation's value()) is a different method.
// compiled holds, per caller and overload, the lines the compiler resolved
// (javac call lines): those place every line exactly, a caller calling
// several overloads included. Callers without them fall back to their call
// sites and the per-caller tags.
func overloadCallLines(name string, arity map[int]bool, tags map[string][]string, callers map[string]grove.SymbolRecord, compiled map[string]map[string][]int) *overloadLines {
	out := &overloadLines{exact: map[string]map[string][]int{}, mixed: map[string]map[int]string{}, guessed: map[string]map[int]string{}, fromCompiler: true}
	claimed := map[string]bool{} // file:line placed exactly
	place := func(label, file string, ln int) {
		pos := file + ":" + strconv.Itoa(ln)
		if claimed[pos] {
			return
		}
		claimed[pos] = true
		if out.exact[label] == nil {
			out.exact[label] = map[string][]int{}
		}
		out.exact[label][file] = append(out.exact[label][file], ln)
		out.calls++
	}
	lineSet := func(c grove.SymbolRecord) []int {
		var lines []int
		seen := map[int]bool{}
		for _, cs := range c.CallSites {
			callee := cs.Callee
			if i := strings.LastIndexByte(callee, '.'); i >= 0 {
				callee = callee[i+1:]
			}
			if callee == name && cs.Line > 0 && !seen[cs.Line] && (cs.Argc == 0 && arity[0] || arity[cs.Argc]) {
				seen[cs.Line] = true
				lines = append(lines, cs.Line)
			}
		}
		return lines
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Compiler lines first: they are per call, so they settle every line
	// they cover, whatever else the caller calls.
	for _, k := range keys {
		byLabel := compiled[k]
		labels := make([]string, 0, len(byLabel))
		for l := range byLabel {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			for _, ln := range byLabel[l] {
				place(l, callers[k].FilePath, ln)
			}
		}
	}
	// Where the compiler supplied lines, a caller without any is a name
	// guess (a same-named static value() in another library): its lines
	// are listed apart, never among the compiler's.
	compilerRan := len(compiled) > 0
	for _, k := range keys {
		if len(compiled[k]) > 0 {
			continue
		}
		c := callers[k]
		if compilerRan {
			for _, ln := range lineSet(c) {
				if !claimed[c.FilePath+":"+strconv.Itoa(ln)] {
					if out.guessed[c.FilePath] == nil {
						out.guessed[c.FilePath] = map[int]string{}
					}
					out.guessed[c.FilePath][ln] = strings.Join(tags[k], " | ")
					out.calls++
				}
			}
			continue
		}
		out.fromCompiler = false
		if t := tags[k]; len(t) == 1 {
			for _, ln := range lineSet(c) {
				place(t[0], c.FilePath, ln)
			}
		}
	}
	for _, k := range keys {
		if len(compiled[k]) > 0 || compilerRan {
			continue
		}
		if t := tags[k]; len(t) > 1 {
			c := callers[k]
			for _, ln := range lineSet(c) {
				if claimed[c.FilePath+":"+strconv.Itoa(ln)] {
					continue
				}
				if out.mixed[c.FilePath] == nil {
					out.mixed[c.FilePath] = map[int]string{}
				}
				out.mixed[c.FilePath][ln] = strings.Join(t, " | ")
				out.calls++
			}
		}
	}
	if out.calls == 0 {
		return nil
	}
	return out
}

// render lays the lines out per overload and file:
//
//	value(String) — 120 calls
//	  gson/.../EnumTypeAdapter.java: 127
func (o *overloadLines) render() []string {
	var lines []string
	labels := make([]string, 0, len(o.exact))
	for l := range o.exact {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		n := 0
		files := make([]string, 0, len(o.exact[l]))
		for f, ls := range o.exact[l] {
			files = append(files, f)
			n += len(ls)
		}
		sort.Strings(files)
		lines = append(lines, l+" — "+strconv.Itoa(n)+" call(s)")
		for _, f := range files {
			ls := append([]int(nil), o.exact[l][f]...)
			sort.Ints(ls)
			lines = append(lines, "  "+f+": "+joinInts(ls))
		}
	}
	if len(o.mixed) > 0 {
		lines = append(lines, "mixed — a function that calls several overloads; read these lines to tell which:")
		files := make([]string, 0, len(o.mixed))
		for f := range o.mixed {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			ls := make([]int, 0, len(o.mixed[f]))
			for ln := range o.mixed[f] {
				ls = append(ls, ln)
			}
			sort.Ints(ls)
			parts := make([]string, 0, len(ls))
			for _, ln := range ls {
				parts = append(parts, strconv.Itoa(ln)+" ("+o.mixed[f][ln]+")")
			}
			lines = append(lines, "  "+f+": "+strings.Join(parts, ", "))
		}
	}
	if len(o.guessed) > 0 {
		lines = append(lines, "name-matched — the compiler resolved none of these calls to the overloads (likely a different method of the same name); check before editing:")
		files := make([]string, 0, len(o.guessed))
		for f := range o.guessed {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			ls := make([]int, 0, len(o.guessed[f]))
			for ln := range o.guessed[f] {
				ls = append(ls, ln)
			}
			sort.Ints(ls)
			parts := make([]string, 0, len(ls))
			for _, ln := range ls {
				parts = append(parts, strconv.Itoa(ln)+" ("+o.guessed[f][ln]+")")
			}
			lines = append(lines, "  "+f+": "+strings.Join(parts, ", "))
		}
	}
	return lines
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ", ")
}

func overloadCallerKey(file string, line int, qn string) string {
	return file + "\x00" + strconv.Itoa(line) + "\x00" + qn
}
