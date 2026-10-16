package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/provasign/prism/internal/grove"
)

// A rename is applied as a text edit (both bench arms used sed in 63 of 64
// wide-bed cells; Edit needs a native Read per file). sed consumes lines;
// change_impact returned symbols grouped by declaration, family and caller,
// so the agent re-derived the line list with grep before every edit (37
// greps after 32 answers, 2026-10-18). editSites is that line list, already
// restricted to the change set: one row per occurrence, in grep's shape.

const editSitesMax = 150

// editSitesFileMax bounds the compact form: past editSitesMax rows the list
// is given per file as line:col positions. Dropping it (the first cut) left
// gson's 137-declaration rename with no list at all; that agent fell back to
// compile-and-fix and grep (2026-10-18 A/B, 3 of 3 such cells lost or tied).
const editSitesFileMax = 2000

type editSite struct {
	file string
	line int
	col  int
	text string
}

// editCollector gathers edit occurrences from source lines, one per
// file:line:col.
type editCollector struct {
	root  string
	files map[string][]string
	seen  map[string]bool
	out   []editSite
}

func newEditCollector(root string) *editCollector {
	return &editCollector{root: root, files: map[string][]string{}, seen: map[string]bool{}}
}

func (c *editCollector) lines(path string) []string {
	if l, ok := c.files[path]; ok {
		return l
	}
	data, err := os.ReadFile(filepath.Join(c.root, path))
	if err != nil || len(data) > 4<<20 {
		c.files[path] = nil
		return nil
	}
	l := strings.Split(string(data), "\n")
	c.files[path] = l
	return l
}

// add records the occurrence of name at path:line (col 0: find it on the line).
func (c *editCollector) add(path string, line, col int, name string) {
	src := c.lines(path)
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
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.out = append(c.out, editSite{path, line, col, strings.TrimRight(text, "\r")})
}

// addDecl records a declaration's name line.
func (c *editCollector) addDecl(d grove.SymbolRecord) {
	path := normalizePath(d.FilePath)
	if l := declNameLine(c.lines(path), d.Span.Start, d.Name); l > 0 {
		c.add(path, l, 0, d.Name)
	}
}

// finish renders the rows: path:line:col: text up to editSitesMax, else one
// row per file listing line:col positions. keys holds every file:line.
func (c *editCollector) finish() (rows []string, keys map[string]bool) {
	if len(c.out) == 0 || len(c.out) > editSitesFileMax {
		return nil, nil
	}
	sort.Slice(c.out, func(i, j int) bool {
		a, b := c.out[i], c.out[j]
		if a.file != b.file {
			return a.file < b.file
		}
		if a.line != b.line {
			return a.line < b.line
		}
		return a.col < b.col
	})
	keys = make(map[string]bool, len(c.out))
	for _, s := range c.out {
		keys[s.file+":"+strconv.Itoa(s.line)] = true
	}
	if len(c.out) <= editSitesMax {
		rows = make([]string, 0, len(c.out))
		for _, s := range c.out {
			text := strings.TrimSpace(s.text)
			if len(text) > 160 {
				text = text[:160] + "…"
			}
			rows = append(rows, fmt.Sprintf("%s:%d:%d: %s", s.file, s.line, s.col, text))
		}
		return rows, keys
	}
	for i := 0; i < len(c.out); {
		j := i
		var pos []string
		for ; j < len(c.out) && c.out[j].file == c.out[i].file; j++ {
			pos = append(pos, fmt.Sprintf("%d:%d", c.out[j].line, c.out[j].col))
		}
		rows = append(rows, fmt.Sprintf("%s (%d): %s", c.out[i].file, j-i, strings.Join(pos, " ")))
		i = j
	}
	return rows, keys
}

// count is the number of occurrences collected.
func (c *editCollector) count() int { return len(c.out) }

// impactEditSites lists every occurrence of the change set's name the
// change touches: each declaration's and family member's name, and every
// call position the compiler or resolver tied to the set. Positions the
// answer marks as another receiver or a misfit are not edits and are left
// out. Nil when there is nothing positional to list.
func (h *Handler) impactEditSites(r *grove.ChangeImpactResult, callers, members map[string]callerPositions) *editCollector {
	if r == nil || h.Root == "" {
		return nil
	}
	c := newEditCollector(h.Root)
	addPositions := func(path string, ps []string, name string) {
		for _, p := range ps {
			l, col := parsePosition(p)
			c.add(path, l, col, name)
		}
	}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		for _, d := range group {
			c.addDecl(d)
			if p, ok := members[overloadCallerKey(d.FilePath, d.Span.Start, displayQN(d))]; ok {
				addPositions(normalizePath(d.FilePath), p.positions, d.Name)
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
	for _, cl := range r.Callers {
		if p, ok := callers[overloadCallerKey(cl.FilePath, cl.Span.Start, displayQN(cl))]; ok {
			addPositions(normalizePath(cl.FilePath), p.positions, callee)
		}
	}
	return c
}

// memberEditSites lists a data member's confirmed occurrences: its
// declarations and every access line with receiver or scope evidence.
// Ambiguous accesses are not edits; their file:line keys come back
// separately so the line partition can name them.
func (h *Handler) memberEditSites(r *grove.ChangeImpactResult, leaf string) (*editCollector, map[string]bool) {
	if r == nil || h.Root == "" {
		return nil, nil
	}
	c := newEditCollector(h.Root)
	for _, d := range r.Declarations {
		c.addDecl(d)
	}
	for _, a := range r.Accesses {
		c.add(normalizePath(a.FilePath), a.Line, 0, leaf)
	}
	unsure := map[string]bool{}
	for _, a := range r.AmbiguousAccesses {
		unsure[fmt.Sprintf("%s:%d", normalizePath(a.FilePath), a.Line)] = true
	}
	return c, unsure
}

// attachEditSites puts editSites and the line partition on an answer.
func (h *Handler) attachEditSites(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult, c *editCollector, leaf string, unsure map[string]bool) {
	if c == nil {
		return
	}
	rows, keys := c.finish()
	if len(rows) == 0 {
		return
	}
	out["editSites"] = rows
	out["editSiteCount"] = c.count()
	// Dynamic-language answers are always partial (callers are inferred);
	// the "some of these may be uses" label there sent agents to grep the
	// listed lines (rich highlight, 2 of 2 runs, 2026-10-16), so it is kept
	// for answers partial for another reason (zod _zod.parse, TypeScript).
	completeness, _ := out["completeness"].(string)
	partial := completeness == "partial" && out["coverageNote"] != dynamicCallerNote
	other := h.impactOtherMentions(ctx, r, leaf, keys, unsure, partial)
	if len(other) == 0 {
		return
	}
	out["otherMentions"] = other
	if out["coverageNote"] == dynamicCallerNote && !strings.Contains(other[0], "not checked") {
		// The partition is the text search this note asked for.
		out["coverageNote"] = "dynamic-language callers are inferred from names and receiver types; the line count below editSites accounts for every line naming the symbol."
	}
}

// formatEditSitesText renders editSites and the line partition.
func formatEditSitesText(out map[string]any) string {
	var b strings.Builder
	if rows := anySlice(out["editSites"]); len(rows) > 0 {
		n, _ := out["editSiteCount"].(int)
		if n <= len(rows) {
			fmt.Fprintf(&b, "editSites (%d; every occurrence this change touches, as path:line:col: text):\n", len(rows))
		} else {
			fmt.Fprintf(&b, "editSites (%d in %d files; every occurrence this change touches, by file as path (count): line:col ...):\n", n, len(rows))
		}
		for _, l := range rows {
			fmt.Fprintf(&b, "  %v\n", l)
		}
	}
	for _, l := range anySlice(out["otherMentions"]) {
		fmt.Fprintf(&b, "%v\n", l)
	}
	return b.String()
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

var (
	wordResMu sync.Mutex
	wordRes   = map[string]*regexp.Regexp{}
)

// wordIndex is the byte index of name as a whole identifier in s, or -1.
func wordIndex(s, name string) int {
	wordResMu.Lock()
	re, ok := wordRes[name]
	if !ok {
		re = regexp.MustCompile(`(^|[^\pL\pN_$])` + regexp.QuoteMeta(name) + `($|[^\pL\pN_$])`)
		wordRes[name] = re
	}
	wordResMu.Unlock()
	m := re.FindStringSubmatchIndex(s)
	if m == nil {
		return -1
	}
	return m[3]
}
