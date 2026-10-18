package mcp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

// The grep left after editSites (wide probes 2026-10-18) asked one thing:
// what else in the repo names X that I am not changing? Its new lines were
// docs (click get_command) or code naming another symbol (jackson
// forProperty: TypeSerializer.forProperty, an InjectableValues parameter).
// otherMentions answers it from the same whole-word search, so that the
// answer accounts for every line such a grep returns: the edit list, code
// naming other symbols called X, and comments/docs, with the total.

const otherMentionsListMax = 40

type mentionLine struct {
	file string
	line int
	text string
}

// impactOtherMentions partitions every whole-word line naming leaf outside
// the edit list. editKeys holds "file:line" of each edit row; unsure holds
// the lines the answer already lists as unconfirmed (a field access whose
// receiver could not be typed), which get their own group.
//
// partial: the answer says its site list is incomplete, so a code line the
// graph did not tie to the change may still be a use (zod
// _$ZodTypeInternals.parse: ~60 `inst._zod.parse` uses were filed as
// "naming another parse").
func (h *Handler) impactOtherMentions(ctx context.Context, r *grove.ChangeImpactResult, leaf string, editKeys, unsure map[string]bool, partial bool) []string {
	if h.Root == "" || leaf == "" || len(editKeys) == 0 {
		return nil
	}
	// Whole-word regex: a common name (zod parse, 5107 lines) passed the
	// old 5000-hit cap on substring hits alone (parseAsync, safeParse).
	res := textsearch.Search(ctx, h.Root, wordPattern(leaf), textsearch.Options{
		Regex: true, CaseSensitive: true, MaxHits: 50000, MaxPerFile: 5000, Timeout: 10 * time.Second,
	})
	if res.TimedOut || res.Truncated {
		return []string{fmt.Sprintf("Other lines naming %s: not checked (the text search hit its limit).", leaf)}
	}
	word := regexp.MustCompile(`(^|[^\pL\pN_$])` + regexp.QuoteMeta(leaf) + `($|[^\pL\pN_$])`)
	inSet := map[string]bool{}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family, r.Supers} {
		for _, d := range group {
			inSet[normalizePath(d.FilePath)+"\x00"+displayQN(d)] = true
		}
	}
	// A super contract's declaration line is related evidence, not a
	// namesake: it is listed in its own group below. Calling it "another
	// Match (not in this change)" contradicted the supers list printed in
	// the same answer (chi Routes.Match, 2026-10-17).
	superLines := map[string]string{}
	for _, sup := range r.Supers {
		superLines[fmt.Sprintf("%s:%d", normalizePath(sup.FilePath), sup.Span.Start)] = displayQN(sup)
	}
	// Other declarations sharing the name: a hit on one of their lines
	// is labelled with the symbol.
	otherDecl := map[string]string{}
	var otherDecls []string
	if h.Grove != nil {
		if cands, err := h.Grove.Resolve(ctx, leaf); err == nil {
			for _, c := range cands {
				if leafName(c.Name) != leaf || inSet[normalizePath(c.File)+"\x00"+c.Name] {
					continue
				}
				otherDecl[normalizePath(c.File)+":"+fmt.Sprint(c.Line)] = c.Name
				otherDecls = append(otherDecls, fmt.Sprintf("%s (%s:%d)", c.Name, normalizePath(c.File), c.Line))
			}
		}
	}
	sort.Strings(otherDecls)
	blocks := map[string]map[int]bool{}
	read := 0
	var code, comments, strs, unconfirmed, related []mentionLine
	edits := 0
	for _, hit := range res.Hits {
		if !word.MatchString(hit.Text) {
			continue
		}
		file := normalizePath(filepath.ToSlash(hit.File))
		if strings.HasPrefix(file, ".grove/") || strings.HasPrefix(file, ".claude/") {
			continue
		}
		if editKeys[fmt.Sprintf("%s:%d", file, hit.Line)] {
			edits++
			continue
		}
		ext := strings.ToLower(filepath.Ext(file))
		text := strings.TrimSpace(hit.Text)
		if len(text) > 140 {
			text = cutUTF8(text, 140) + "…"
		}
		m := mentionLine{file, hit.Line, text}
		if _, ok := superLines[fmt.Sprintf("%s:%d", file, hit.Line)]; ok {
			related = append(related, m)
			continue
		}
		if unsure[fmt.Sprintf("%s:%d", file, hit.Line)] {
			unconfirmed = append(unconfirmed, m)
			continue
		}
		kind := ""
		switch {
		case docFileExts[ext], isCommentLineExt(hit.Text, ext):
			kind = "comment"
		default:
			if _, ok := blocks[file]; !ok && read < 1000 {
				read++
				blocks[file] = blockCommentLines(filepath.Join(h.Root, file), ext)
			}
			t := strings.TrimSpace(hit.Text)
			switch {
			case blocks[file] != nil && blocks[file][hit.Line]:
				kind = "comment"
			case (ext == ".py" || ext == ".pyi") && (strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, `'''`)):
				kind = "comment" // a one-line docstring
			default:
				kind = mentionKind(hit.Text, leaf, ext)
			}
		}
		switch kind {
		case "comment":
			comments = append(comments, m)
		case "string":
			// Inside a string: a generated-code template, a getattr name, a
			// message. It may be a use the graph cannot see (werkzeug's
			// rules.py builds `converter.to_url(...)` from a string), so it
			// is never filed as "something else".
			strs = append(strs, m)
		default:
			code = append(code, m)
		}
	}
	total := edits + len(unconfirmed) + len(code) + len(strs) + len(comments) + len(related)
	inTests := func(ls []mentionLine) int {
		n := 0
		for _, l := range ls {
			if isTestFilePath(l.file) {
				n++
			}
		}
		return n
	}
	editTests := 0
	for k := range editKeys {
		if f, _, ok := strings.Cut(k, ":"); ok && isTestFilePath(f) {
			editTests++
		}
	}
	codeKind := "naming something else"
	if partial {
		codeKind = "not tied to this change"
	}
	var out []string
	if len(unconfirmed) == 0 {
		out = append(out, fmt.Sprintf("Every line naming %s in this repository (whole word, %d): %d in editSites above, %d code line(s) %s, %d string literal(s), %d comment/doc line(s). In test files: %d, %d, %d and %d of those.",
			leaf, total, edits, len(code), codeKind, len(strs), len(comments), editTests, inTests(code), inTests(strs), inTests(comments)))
	} else {
		out = append(out, fmt.Sprintf("Every line naming %s in this repository (whole word, %d): %d in editSites above, %d unconfirmed (receiver not typed), %d code line(s) %s, %d string literal(s), %d comment/doc line(s). In test files: %d, %d, %d, %d and %d of those.",
			leaf, total, edits, len(unconfirmed), len(code), codeKind, len(strs), len(comments), editTests, inTests(unconfirmed), inTests(code), inTests(strs), inTests(comments)))
	}
	if len(related) > 0 {
		out[0] += fmt.Sprintf(" %d of them declare a super contract of the queried symbol, listed as related below.", len(related))
	}
	if len(otherDecls) > 0 {
		shown := otherDecls
		if len(shown) > 12 {
			shown = append(shown[:12:12], fmt.Sprintf("and %d more", len(otherDecls)-12))
		}
		out = append(out, "other declarations named "+leaf+" (not in this change): "+strings.Join(shown, ", "))
	}
	relatedDecl := map[string]string{}
	for k, qn := range superLines {
		relatedDecl[k] = qn
	}
	out = append(out, renderMentionGroup("related contract declarations naming "+leaf+" (supers of the queried symbol; edit them only if the contract itself changes)", related, relatedDecl)...)
	out = append(out, renderMentionGroup("unconfirmed: names "+leaf+" but the receiver could not be typed (the ambiguousAccesses above; decide per line)", unconfirmed, nil)...)
	codeTitle := "code naming another " + leaf + " (not in this change)"
	if partial {
		codeTitle = "code naming " + leaf + " that the graph did not tie to this change (the answer above is partial, so some of these may be uses; decide per line)"
	}
	out = append(out, renderMentionGroup(codeTitle, code, otherDecl)...)
	out = append(out, renderMentionGroup("string literals naming "+leaf+" (a string may be a use the graph cannot see: generated code, getattr, reflection; decide per line)", strs, nil)...)
	out = append(out, renderMentionGroup("comments/docs naming "+leaf, comments, nil)...)
	return out
}

func renderMentionGroup(title string, lines []mentionLine, decl map[string]string) []string {
	if len(lines) == 0 {
		return nil
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].file != lines[j].file {
			return lines[i].file < lines[j].file
		}
		return lines[i].line < lines[j].line
	})
	if len(lines) <= otherMentionsListMax {
		out := []string{fmt.Sprintf("%s (%d):", title, len(lines))}
		for _, l := range lines {
			label := ""
			if q := decl[fmt.Sprintf("%s:%d", l.file, l.line)]; q != "" {
				label = "  [declares " + q + "]"
			}
			out = append(out, fmt.Sprintf("  %s:%d: %s%s", l.file, l.line, l.text, label))
		}
		return out
	}
	per := map[string]int{}
	var files []string
	for _, l := range lines {
		if per[l.file] == 0 {
			files = append(files, l.file)
		}
		per[l.file]++
	}
	// Every file, never "and N more": a truncated list was read as
	// "something might be hidden" and searched (jackson forProperty probe).
	// Files are grouped under their directory so a deep path is written once:
	// jackson SettableBeanProperty.set, 2026-10-18 — two such lists were 15.8 KB
	// of a 31.8 KB answer, 6.6 KB of it repeated directory prefixes.
	return []string{fmt.Sprintf("%s (%d, by file): %s", title, len(lines), groupFilesByDir(files, per))}
}

// groupFilesByDir renders "dir/: a.go (2), b.go (1); other/: c.go (1)",
// directories in first-seen order. Every file and count stays in the text.
func groupFilesByDir(files []string, per map[string]int) string {
	var dirs []string
	byDir := map[string][]string{}
	for _, f := range files {
		dir, base := path.Split(f)
		if _, seen := byDir[dir]; !seen {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], fmt.Sprintf("%s (%d)", base, per[f]))
	}
	groups := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names := strings.Join(byDir[dir], ", ")
		if dir == "" {
			groups = append(groups, names)
			continue
		}
		groups = append(groups, dir+": "+names)
	}
	return strings.Join(groups, "; ")
}

var docFileExts = map[string]bool{".md": true, ".mdx": true, ".rst": true, ".txt": true, ".adoc": true, ".html": true}

// isCommentLineExt: a line that is a comment by its first characters.
func isCommentLineExt(line, ext string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "#"):
		return ext == ".py" || ext == ".pyi" || ext == ".rb" || ext == ".sh" || ext == ".php" || ext == ".yaml" || ext == ".yml" || ext == ".toml"
	case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "/*"), t == "*", strings.HasPrefix(t, "* "), strings.HasPrefix(t, "*/"), strings.HasPrefix(t, "///"), strings.HasPrefix(t, "--"):
		return true
	}
	return false
}

// mentionKind classifies a mention on a code line that is not comment-shaped:
// after a trailing comment marker it is a comment, inside quotes a string,
// else code.
func mentionKind(line, leaf, ext string) string {
	i := strings.Index(line, leaf)
	if i < 0 {
		return "code"
	}
	prefix := line[:i]
	quotes := strings.Count(prefix, `"`) + strings.Count(prefix, "'") + strings.Count(prefix, "`")
	inString := quotes%2 == 1
	hash := ext == ".py" || ext == ".pyi" || ext == ".rb" || ext == ".sh" || ext == ".php"
	if !inString {
		if strings.Contains(prefix, "//") || (hash && strings.Contains(prefix, "#")) {
			return "comment"
		}
		return "code"
	}
	return "string"
}

// blockCommentLines marks the lines of a file that lie inside a block comment
// (/* … */) or, for Python, a triple-quoted string (a docstring). Only the
// lines strictly inside count; the opening line is classified on its own.
func blockCommentLines(path, ext string) map[int]bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 2<<20 {
		return nil
	}
	out := map[int]bool{}
	lines := strings.Split(string(data), "\n")
	if ext == ".py" || ext == ".pyi" {
		open := ""
		for i, l := range lines {
			if open != "" {
				out[i+1] = true
				if strings.Contains(l, open) {
					open = ""
				}
				continue
			}
			for _, q := range []string{`"""`, `'''`} {
				if n := strings.Count(l, q); n%2 == 1 {
					open = q
					out[i+1] = true
					break
				}
			}
		}
		return out
	}
	in := false
	for i, l := range lines {
		if in {
			out[i+1] = true
			if strings.Contains(l, "*/") {
				in = false
			}
			continue
		}
		if j := blockCommentOpen(l); j >= 0 && !strings.Contains(l[j:], "*/") {
			in = true
			out[i+1] = true
		}
	}
	return out
}

// blockCommentOpen is the index of a "/*" that opens a comment on line, or
// -1: a "/*" inside a string ("/admin/**", h3 routes) or after "//" does not.
func blockCommentOpen(line string) int {
	var quote byte
	for i := 0; i+1 < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && line[i+1] == '/':
			return -1
		case c == '/' && line[i+1] == '*':
			return i
		}
	}
	return -1
}

// wordPattern is a regex for leaf as a whole word; \b is only placed next to
// a word character (a leading $ has no boundary before it).
func wordPattern(leaf string) string {
	isWord := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	p := regexp.QuoteMeta(leaf)
	if isWord(leaf[0]) {
		p = `\b` + p
	}
	if isWord(leaf[len(leaf)-1]) {
		p += `\b`
	}
	return p
}
