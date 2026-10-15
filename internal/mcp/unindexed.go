package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/provasign/prism/internal/textsearch"
)

// Source in a language grove does not parse is not in the graph at all: a
// Ruby class or a TypeScript function's callers in .vue files are simply
// absent, and an answer built from the graph cannot say anything about them.
// The text is still there, so a name that appears in such files is reported,
// rather than letting "no symbol" or a "complete" caller list stand.
var unindexedCodeLanguages = map[string]string{
	".rb": "Ruby", ".rake": "Ruby", ".scala": "Scala", ".sc": "Scala",
	".lua": "Lua", ".ex": "Elixir", ".exs": "Elixir", ".dart": "Dart",
	".hs": "Haskell", ".clj": "Clojure", ".cljs": "Clojure", ".erl": "Erlang",
	".groovy": "Groovy", ".gradle": "Groovy", ".vue": "Vue", ".svelte": "Svelte",
	".astro": "Astro", ".pl": "Perl", ".pm": "Perl", ".jl": "Julia",
	".fs": "F#", ".ml": "OCaml", ".zig": "Zig", ".nim": "Nim",
}

const unindexedFilesShown = 8

// unindexedMentions returns the files in unindexed code languages whose text
// names leaf as a whole identifier, at most a few hundred, sorted.
func (h *Handler) unindexedMentions(ctx context.Context, leaf string) []string {
	if leaf == "" || h.Root == "" {
		return nil
	}
	globs := make([]string, 0, len(unindexedCodeLanguages))
	for ext := range unindexedCodeLanguages {
		globs = append(globs, "*"+ext)
	}
	sort.Strings(globs)
	res := textsearch.Search(ctx, h.Root, leaf, textsearch.Options{
		Glob: globs, CaseSensitive: true, MaxHits: 500, MaxPerFile: 3, Timeout: 5 * time.Second,
	})
	word := regexp.MustCompile(`(^|[^\pL\pN_$])` + regexp.QuoteMeta(leaf) + `($|[^\pL\pN_$])`)
	seen := map[string]bool{}
	var files []string
	for _, hit := range res.Hits {
		if _, ok := unindexedCodeLanguages[strings.ToLower(filepath.Ext(hit.File))]; !ok || seen[hit.File] || !word.MatchString(hit.Text) {
			continue
		}
		seen[hit.File] = true
		files = append(files, filepath.ToSlash(hit.File))
	}
	sort.Strings(files)
	return files
}

// unindexedNote says which files name leaf in languages prism does not
// index, or "" when none do.
func (h *Handler) unindexedNote(ctx context.Context, leaf string) string {
	files := h.unindexedMentions(ctx, leaf)
	if len(files) == 0 {
		return ""
	}
	langs := map[string]bool{}
	for _, f := range files {
		langs[unindexedCodeLanguages[strings.ToLower(filepath.Ext(f))]] = true
	}
	names := make([]string, 0, len(langs))
	for l := range langs {
		names = append(names, l)
	}
	sort.Strings(names)
	shown := files
	more := ""
	if len(shown) > unindexedFilesShown {
		more = fmt.Sprintf(" and %d more", len(shown)-unindexedFilesShown)
		shown = shown[:unindexedFilesShown]
	}
	return fmt.Sprintf("%q also appears in %d file(s) in %s, which prism does not index: %s%s. "+
		"Uses there are not in this answer; read those files directly.",
		leaf, len(files), strings.Join(names, ", "), strings.Join(shown, ", "), more)
}

// addUnindexedNote attaches the note to a change_impact answer and withdraws
// any completeness claim: callers in those files cannot be counted.
func (h *Handler) addUnindexedNote(ctx context.Context, out map[string]any, leaf string) {
	note := h.unindexedNote(ctx, leaf)
	if note == "" {
		return
	}
	out["unindexedNote"] = note
	if safe, _ := out["safeToClaimComplete"].(bool); safe {
		addIndexedCompletenessSafety(out)
		if _, ok := out["evidenceNote"]; ok {
			out["evidenceNote"] = indexedEvidenceNote
		}
	}
}
