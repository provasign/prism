package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// A rename is applied as a text edit (both bench arms used sed in 63 of 64
// wide-bed cells; Edit needs a native Read per file). sed consumes lines;
// change_impact returned symbols grouped by declaration, family and caller,
// so the agent re-derived the line list with grep before every edit (37
// greps after 32 answers, 2026-10-18). editSites is that line list, already
// restricted to the change set: one row per occurrence, in grep's shape.

const editSitesMax = 150

type editSite struct {
	file string
	line int
	col  int
	text string
}

// impactEditSites lists every occurrence of the change set's name the
// change touches: each declaration's and family member's name, and every
// call position the compiler or resolver tied to the set. Positions the
// answer marks as another receiver or a misfit are not edits and are left
// out. Nil when there is nothing positional to list or the list is too long
// to be an edit list.
func (h *Handler) impactEditSites(r *grove.ChangeImpactResult, callers, members map[string]callerPositions) ([]string, map[string]bool) {
	if r == nil || h.Root == "" {
		return nil, nil
	}
	files := map[string][]string{}
	lines := func(path string) []string {
		if l, ok := files[path]; ok {
			return l
		}
		data, err := os.ReadFile(filepath.Join(h.Root, path))
		if err != nil || len(data) > 4<<20 {
			files[path] = nil
			return nil
		}
		l := strings.Split(string(data), "\n")
		files[path] = l
		return l
	}
	seen := map[string]bool{}
	var out []editSite
	add := func(path string, line, col int, name string) {
		src := lines(path)
		if line <= 0 || line > len(src) {
			return
		}
		text := src[line-1]
		if col <= 0 {
			col = wordIndex(text, name) + 1
			if col <= 0 {
				return
			}
		}
		key := path + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, editSite{path, line, col, strings.TrimRight(text, "\r")})
	}
	addPositions := func(path string, ps []string, name string) {
		for _, p := range ps {
			l, c := parsePosition(p)
			add(path, l, c, name)
		}
	}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		for _, d := range group {
			path := normalizePath(d.FilePath)
			if l := declNameLine(lines(path), d.Span.Start, d.Name); l > 0 {
				add(path, l, 0, d.Name)
			}
			if p, ok := members[overloadCallerKey(d.FilePath, d.Span.Start, displayQN(d))]; ok {
				addPositions(path, p.positions, d.Name)
			}
		}
	}
	callee := ""
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		if len(group) > 0 {
			callee = group[0].Name
			break
		}
	}
	for _, c := range r.Callers {
		if p, ok := callers[overloadCallerKey(c.FilePath, c.Span.Start, displayQN(c))]; ok {
			addPositions(normalizePath(c.FilePath), p.positions, callee)
		}
	}
	if len(out) == 0 || len(out) > editSitesMax {
		return nil, nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].col < out[j].col
	})
	rows := make([]string, 0, len(out))
	keys := make(map[string]bool, len(out))
	for _, s := range out {
		keys[s.file+":"+strconv.Itoa(s.line)] = true
		text := strings.TrimSpace(s.text)
		if len(text) > 160 {
			text = text[:160] + "…"
		}
		rows = append(rows, fmt.Sprintf("%s:%d:%d: %s", s.file, s.line, s.col, text))
	}
	return rows, keys
}

// parsePosition reads "12:5" (line:col) or "12" (line only).
func parsePosition(p string) (int, int) {
	p = strings.TrimSpace(p)
	l, c, _ := strings.Cut(p, ":")
	line, _ := strconv.Atoi(strings.TrimRight(l, "t"))
	col, _ := strconv.Atoi(c)
	return line, col
}

// declNameLine finds the line holding a declaration's name at or just after
// its span start (decorators and annotations come first).
func declNameLine(src []string, start int, name string) int {
	if name == "" {
		return 0
	}
	for l := start; l > 0 && l <= len(src) && l < start+8; l++ {
		t := strings.TrimSpace(src[l-1])
		if strings.HasPrefix(t, "@") || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") {
			continue
		}
		if wordIndex(src[l-1], name) >= 0 {
			return l
		}
	}
	return 0
}

var wordRes = map[string]*regexp.Regexp{}

// wordIndex is the byte index of name as a whole identifier in s, or -1.
func wordIndex(s, name string) int {
	re, ok := wordRes[name]
	if !ok {
		re = regexp.MustCompile(`(^|[^\pL\pN_$])` + regexp.QuoteMeta(name) + `($|[^\pL\pN_$])`)
		wordRes[name] = re
	}
	m := re.FindStringSubmatchIndex(s)
	if m == nil {
		return -1
	}
	return m[3]
}
