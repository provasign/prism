package mcp

import (
	"path/filepath"
	"sort"
	"strings"
)

func isProductionSourcePath(file string) bool {
	if isTestFilePath(file) || strings.HasPrefix(file, "examples/") {
		return false
	}
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go", ".py", ".rs", ".java", ".js", ".jsx", ".ts", ".tsx",
		".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".php", ".rb",
		".kt", ".kts", ".scala", ".swift", ".m", ".mm", ".sql", ".sh", ".proto":
		return true
	}
	return false
}

// searchLeads puts a few distinct source anchors ahead of a batched result.
// It ranks only evidence already delivered by the exact searches; it is not
// a semantic interpretation of the terms or a claim of complete coverage.
func searchLeads(results []map[string]any) []map[string]any {
	type lead struct {
		file, match    string
		line, strength int
		terms          map[string]bool
	}
	byFile := map[string]*lead{}
	add := func(file string, line, strength int, kind, term string) {
		if file == "" || line < 1 {
			return
		}
		item := byFile[file]
		if item == nil {
			item = &lead{file: file, terms: map[string]bool{}}
			byFile[file] = item
		}
		item.terms[term] = true
		if strength > item.strength {
			item.line, item.strength, item.match = line, strength, kind
		}
	}
	for _, result := range results {
		term, _ := result["query"].(string)
		for _, raw := range anySlice(result["symbols"]) {
			sym, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			span, _ := sym["span"].(map[string]any)
			kind, _ := sym["matchKind"].(string)
			strength := map[string]int{"name-exact": 7, "name-prefix": 6, "name-substring": 5,
				"qualified": 4, "path": 3, "signature": 2, "doc": 1, "body": 1}[kind]
			file, _ := sym["filePath"].(string)
			add(file, intArg(span, "start", 0), strength, kind, term)
		}
		for _, raw := range anySlice(result["textHits"]) {
			group, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := group["file"].(string)
			for _, rawHit := range anySlice(group["hits"]) {
				hit, ok := rawHit.(map[string]any)
				if ok {
					line, _ := hit["text"].(string)
					add(file, intArg(hit, "line", 0), 2+evidenceLineScore(line), "text", term)
				}
			}
		}
	}
	var candidates []*lead
	for _, item := range byFile {
		// A frequent syntax word in a batch can put examples or docs first by
		// sheer hit count. Show a lead only when an indexed name resolves or
		// independent terms agree on a production source file.
		strong := (item.match != "text" && item.strength >= 4) || (len(item.terms) >= 2 &&
			isProductionSourcePath(item.file) && item.strength >= 2)
		if strong {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	score := func(item *lead) int {
		value := item.strength + 3*(len(item.terms)-1)
		if isTestFilePath(item.file) {
			value--
		} else if isProductionSourcePath(item.file) {
			value += 2
		}
		return value
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if score(a) != score(b) {
			return score(a) > score(b)
		}
		return a.file < b.file
	})
	var out []map[string]any
	for _, item := range candidates[:minInt(3, len(candidates))] {
		out = append(out, map[string]any{"file": item.file, "line": item.line,
			"match": item.match, "terms": len(item.terms)})
	}
	return out
}
