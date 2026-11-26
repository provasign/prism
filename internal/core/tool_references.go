package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

func (h *Handler) toolReferences(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	file := stringArg(args, "file", "")
	// A qualified name or a file names one symbol: answer from its resolved
	// sites, not from every use of the bare name. references Client.Status
	// returned 29 hits for "Status", FileDrift.Status fields included
	// (BUG-14).
	if strings.Contains(name, ".") || file != "" {
		ciArgs := map[string]any{"query": name}
		if file != "" {
			ciArgs["file"] = file
		}
		ci, err := h.toolChangeImpact(ctx, ciArgs)
		if err != nil {
			return nil, err
		}
		if m, ok := ci.(map[string]any); ok {
			if rows := anySlice(m["editSites"]); len(rows) > 0 {
				rows = append(rows, h.typeUseRows(ctx, m)...)
				kinds := referenceKinds(m, rows)
				count := impactEditSiteCount(m)
				if extra := len(rows) - len(anySlice(m["editSites"])); extra > 0 {
					count += extra
				}
				if want := referenceKindFilter(args); len(want) > 0 {
					var kept []any
					for _, r := range rows {
						if want[kinds[siteKey(fmt.Sprint(r))]] {
							kept = append(kept, r)
						}
					}
					rows, count = kept, len(kept)
				}
				tagged := make([]any, 0, len(rows))
				for _, r := range rows {
					row := fmt.Sprint(r)
					if k := kinds[siteKey(row)]; k != "" {
						row += "  [" + k + "]"
					}
					tagged = append(tagged, row)
				}
				rows = tagged
				out := map[string]any{
					"name":     name,
					"resolved": true,
					"sites":    rows,
					"count":    count,
					"note": "sites resolved to " + name + ": its declaration(s), the implementations in its family, and every call or reference position tied to it; " +
						"otherMentions sorts the remaining lines naming " + leafName(name),
				}
				for _, k := range []string{"completeness", "completenessScope", "callerEvidence", "otherMentions", "degradedAnalysis"} {
					if v, ok := m[k]; ok {
						out[k] = v
					}
				}
				return out, nil
			}
		}
	}
	res, err := h.Grove.References(ctx, name)
	if err != nil {
		return nil, err
	}
	// One row per occurrence: path:line:col: text. The engine reports a
	// line once per occurrence and without a column, so a line naming the
	// symbol twice printed twice with nothing to tell the rows apart.
	leaf := leafName(name)
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(leaf) + `\b`)
	byFile := map[string][]map[string]any{}
	var sites []string
	seen := map[string]bool{}
	files := map[string][]string{}
	for _, r := range res.Refs {
		key := fmt.Sprintf("%s:%d", r.File, r.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		entry := map[string]any{"line": r.Line}
		if r.Enclosing != "" {
			entry["in"] = r.Enclosing
		}
		byFile[r.File] = append(byFile[r.File], entry)
		lines, ok := files[r.File]
		if !ok {
			if data, err := os.ReadFile(filepath.Join(h.Root, r.File)); err == nil {
				lines = strings.Split(string(data), "\n")
			}
			files[r.File] = lines
		}
		text := ""
		if r.Line >= 1 && r.Line <= len(lines) {
			text = strings.TrimRight(lines[r.Line-1], "\r")
		}
		cols := word.FindAllStringIndex(text, -1)
		if len(cols) == 0 {
			sites = append(sites, fmt.Sprintf("%s:%d: %s", normalizePath(r.File), r.Line, clipRunes(strings.TrimSpace(text), 100)))
			continue
		}
		for _, c := range cols {
			sites = append(sites, fmt.Sprintf("%s:%d:%d: %s", normalizePath(r.File), r.Line, c[0]+1, clipRunes(strings.TrimSpace(text), 100)))
		}
	}
	out := map[string]any{
		"name":        res.Name,
		"count":       len(sites),
		"definitions": res.DefCount,
		"ambiguous":   res.Ambiguous,
		"byFile":      byFile,
		"sites":       sites,
	}
	if res.Ambiguous {
		out["note"] = fmt.Sprintf("uses of the name %s, not resolved to one symbol (%d definitions share it); pass Type.member or file to get one symbol's sites", leaf, res.DefCount)
	}
	if res.SkippedFiles > 0 {
		out["skippedFiles"] = res.SkippedFiles
		out["warning"] = fmt.Sprintf("%d file(s) could not be read — this reference "+
			"set may be incomplete", res.SkippedFiles)
	}
	return out, nil
}

func (h *Handler) toolResolve(ctx context.Context, args map[string]any) (any, error) {
	name := stringArg(args, "name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}
	cands, err := h.Grove.Resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		entry := map[string]any{"name": c.Name, "kind": c.Kind, "file": c.File, "line": c.Line}
		if c.TestDouble {
			entry["testDouble"] = true
			entry["testLabel"] = c.TestLabel
		}
		out = append(out, entry)
	}
	if len(cands) > 1 {
		h.addPasteableNames(ctx, out)
	}
	return map[string]any{"name": name, "count": len(out), "candidates": out}, nil
}

// siteKey is "path:line" of a "path:line:col: text" row.
func siteKey(row string) string {
	f, l := splitSiteRow(row)
	return fmt.Sprintf("%s:%d", f, l)
}

// referenceKinds labels each resolved site (GAP-02): decl for a
// declaration line, read/write/init for a member access, type-use for a
// line naming a type, call otherwise.
func referenceKinds(impact map[string]any, rows []any) map[string]string {
	kinds := map[string]string{}
	for _, r := range anySlice(impact["relaySites"]) {
		// "path:line:Owner [read]"
		row := fmt.Sprint(r)
		i, j := strings.LastIndex(row, "["), strings.LastIndex(row, "]")
		if i < 0 || j < i {
			continue
		}
		tag := row[i+1 : j]
		switch tag {
		case "decl", "read", "write", "init":
			parts := strings.SplitN(row, ":", 3)
			if len(parts) >= 2 {
				kinds[normalizePath(parts[0])+":"+parts[1]] = tag
			}
		}
	}
	isType := false
	for _, d := range anyMaps(impact["declarations"]) {
		kinds[fmt.Sprintf("%s:%v", normalizePath(fmt.Sprint(d["filePath"])), d["line"])] = "decl"
		switch d["kind"] {
		case "struct", "class", "interface", "type", "enum", "trait", "record":
			isType = true
		}
	}
	for _, r := range rows {
		k := siteKey(fmt.Sprint(r))
		if kinds[k] == "" {
			if isType {
				kinds[k] = "type-use"
			} else {
				kinds[k] = "call"
			}
		}
	}
	return kinds
}

// referenceKindFilter reads kind= (a string or list; "type" means
// type-use, "write" includes init).
func referenceKindFilter(args map[string]any) map[string]bool {
	var raw []string
	switch v := args["kind"].(type) {
	case string:
		raw = strings.Split(v, ",")
	case []any:
		for _, x := range v {
			raw = append(raw, fmt.Sprint(x))
		}
	}
	want := map[string]bool{}
	for _, k := range raw {
		switch k = strings.TrimSpace(k); k {
		case "":
		case "type":
			want["type-use"] = true
		case "write":
			want["write"], want["init"] = true, true
		default:
			want[k] = true
		}
	}
	return want
}

// typeUseRows lists, for a type, each line in a resolved user's body that
// names it (GAP-02 step 3). change-impact gives a type only its
// declaration as an edit site and its users as callers without a line, so
// references reported one site for a type used everywhere.
func (h *Handler) typeUseRows(ctx context.Context, impact map[string]any) []any {
	var typeName string
	for _, d := range anyMaps(impact["declarations"]) {
		switch d["kind"] {
		case "struct", "class", "interface", "type", "enum", "trait", "record":
			typeName = fmt.Sprint(d["name"])
		}
	}
	if typeName == "" {
		return nil
	}
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(typeName) + `\b`)
	seen := map[string]bool{}
	for _, r := range anySlice(impact["editSites"]) {
		seen[siteKey(fmt.Sprint(r))] = true
	}
	var rows []any
	for _, c := range anyMaps(impact["callers"]) {
		file := normalizePath(fmt.Sprint(c["filePath"]))
		start, _ := c["line"].(int)
		if start == 0 {
			if f, ok := c["line"].(float64); ok {
				start = int(f)
			}
		}
		end := start
		if syms, err := h.Grove.FileSymbols(ctx, file); err == nil {
			for _, s := range syms {
				if s.Span.Start == start && s.Name == fmt.Sprint(c["name"]) {
					end = s.Span.End
				}
			}
		}
		abs, _, err := safePathWithinRoot(h.Root, file)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for n := start; n <= end && n <= len(lines); n++ {
			text := lines[n-1]
			loc := word.FindStringIndex(text)
			if loc == nil {
				continue
			}
			key := fmt.Sprintf("%s:%d", file, n)
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, fmt.Sprintf("%s:%d:%d: %s", file, n, loc[0]+1, strings.TrimSpace(text)))
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		fi, li := splitSiteRow(fmt.Sprint(rows[i]))
		fj, lj := splitSiteRow(fmt.Sprint(rows[j]))
		if fi != fj {
			return fi < fj
		}
		return li < lj
	})
	return rows
}

// addPasteableNames gives each of several candidates a name that resolves
// to it alone (GAP-01): package/directory-qualified when lookup resolves
// that form to exactly this file, else path:line, which every symbol
// command accepts (CON-10).
func (h *Handler) addPasteableNames(ctx context.Context, cands []map[string]any) {
	for _, c := range cands {
		name, _ := c["name"].(string)
		file, _ := c["file"].(string)
		line, _ := c["line"].(int)
		paste := fmt.Sprintf("%s:%d", file, line)
		if dir := filepath.Base(filepath.Dir(filepath.FromSlash(file))); dir != "." && dir != string(filepath.Separator) && name != "" {
			q := dir + "." + name
			if out, err := h.lookupSymbol(ctx, map[string]any{"name": q}, ""); err == nil {
				if m, ok := out.(map[string]any); ok && m["ambiguous"] == nil && m["matched"] != false {
					if sym, _ := m["symbol"].(grove.SymbolRecord); normalizePath(sym.FilePath) == normalizePath(file) {
						paste = q
					} else if sm, ok := m["symbol"].(map[string]any); ok && normalizePath(fmt.Sprint(sm["filePath"])) == normalizePath(file) {
						paste = q
					}
				}
			}
		}
		c["pasteable"] = paste
	}
}
