package mcp

import (
	"context"
	"fmt"
	"os"
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
// the edit list. editKeys holds "file:line" of each edit row.
func (h *Handler) impactOtherMentions(ctx context.Context, r *grove.ChangeImpactResult, leaf string, editKeys map[string]bool) []string {
	if h.Root == "" || leaf == "" || len(editKeys) == 0 {
		return nil
	}
	res := textsearch.Search(ctx, h.Root, leaf, textsearch.Options{
		CaseSensitive: true, MaxHits: 5000, MaxPerFile: 400, Timeout: 5 * time.Second,
	})
	if res.TimedOut || res.Truncated {
		return []string{fmt.Sprintf("Other lines naming %s: not checked (the text search hit its limit).", leaf)}
	}
	word := regexp.MustCompile(`(^|[^\pL\pN_$])` + regexp.QuoteMeta(leaf) + `($|[^\pL\pN_$])`)
	inSet := map[string]bool{}
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Family} {
		for _, d := range group {
			inSet[normalizePath(d.FilePath)+"\x00"+displayQN(d)] = true
		}
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
	var code, comments []mentionLine
	strs := 0
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
			text = text[:140] + "…"
		}
		m := mentionLine{file, hit.Line, text}
		kind := ""
		switch {
		case docFileExts[ext], isCommentLineExt(hit.Text, ext):
			kind = "comment"
		default:
			if _, ok := blocks[file]; !ok && read < 80 {
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
			strs++
			code = append(code, m)
		default:
			code = append(code, m)
		}
	}
	total := edits + len(code) + len(comments)
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
	out := []string{fmt.Sprintf("Every line naming %s in this repository (whole word, %d): %d in editSites above, %d code line(s) naming something else, %d comment/doc line(s). In test files: %d, %d and %d of those.",
		leaf, total, edits, len(code), len(comments), editTests, inTests(code), inTests(comments))}
	if len(otherDecls) > 0 {
		shown := otherDecls
		if len(shown) > 12 {
			shown = append(shown[:12:12], fmt.Sprintf("and %d more", len(otherDecls)-12))
		}
		out = append(out, "other declarations named "+leaf+" (not in this change): "+strings.Join(shown, ", "))
	}
	out = append(out, renderMentionGroup("code naming another "+leaf+" (not in this change)", code, otherDecl)...)
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
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, fmt.Sprintf("%s (%d)", f, per[f]))
	}
	return []string{fmt.Sprintf("%s (%d, by file): %s", title, len(lines), strings.Join(parts, ", "))}
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
		if j := strings.Index(l, "/*"); j >= 0 && !strings.Contains(l[j:], "*/") {
			in = true
			out[i+1] = true
		}
	}
	return out
}
