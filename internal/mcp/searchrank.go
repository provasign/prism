package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

type rankedSearchSymbol struct {
	symbol    grove.SymbolRecord
	matchKind string
	tier      int
}

// rankSearchSymbols applies Prism's declared match tiers before the delivery
// cap. Grove's order remains the stable tie-breaker within each tier.
func rankSearchSymbols(symbols []grove.SymbolRecord, query string) []rankedSearchSymbol {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]rankedSearchSymbol, len(symbols))
	for i, sym := range symbols {
		kind, tier := symbolMatchTier(sym, q)
		if sym.Kind == "document" {
			tier = 2
		} else if isTestDouble(sym.FilePath) && kind != "name-exact" {
			tier = 1
		}
		out[i] = rankedSearchSymbol{symbol: sym, matchKind: kind, tier: tier}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].tier > out[j].tier })
	// For weaker path/signature/body matches, give each file a first slot
	// before taking a second from the same file. Keep exact/name matches in
	// Grove order: an explicitly named symbol must not lose its position.
	var diversified []rankedSearchSymbol
	for start := 0; start < len(out); {
		end := start + 1
		for end < len(out) && out[end].tier == out[start].tier {
			end++
		}
		if out[start].tier > 5 {
			diversified = append(diversified, out[start:end]...)
		} else {
			byFile := make(map[string][]rankedSearchSymbol)
			var files []string
			for _, item := range out[start:end] {
				path := item.symbol.FilePath
				if len(byFile[path]) == 0 {
					files = append(files, path)
				}
				byFile[path] = append(byFile[path], item)
			}
			for round := 0; ; round++ {
				added := false
				for _, path := range files {
					if round < len(byFile[path]) {
						diversified = append(diversified, byFile[path][round])
						added = true
					}
				}
				if !added {
					break
				}
			}
		}
		start = end
	}
	return diversified
}

func symbolMatchTier(sym grove.SymbolRecord, q string) (string, int) {
	name := strings.ToLower(sym.Name)
	qualified := strings.ToLower(sym.QualifiedName)
	switch {
	case q == "":
		return "unclassified", 0
	case name == q:
		return "name-exact", 9
	case strings.HasPrefix(name, q):
		return "name-prefix", 8
	case strings.Contains(name, q):
		return "name-substring", 7
	case strings.Contains(qualified, q):
		return "qualified", 6
	case strings.Contains(strings.ToLower(sym.FilePath), q):
		return "path", 5
	case strings.Contains(strings.ToLower(sym.Signature), q):
		return "signature", 4
	case strings.Contains(strings.ToLower(sym.Docstring), q):
		return "doc", 3
	case strings.Contains(strings.ToLower(sym.RawText), q):
		return "body", 2
	default:
		return "unclassified", 0
	}
}

// pathOnlySymbolCap bounds symbols listed only because the term occurs in
// their file path once any symbol matched by name.
const pathOnlySymbolCap = 3

// condenseSearchSymbols removes two kinds of padding from a ranked symbol
// list and returns a note naming what was left out:
//
//   - members that matched only through an enclosing name ("qualified")
//     when that enclosing symbol is itself listed. Measured (jackson pr6030):
//     term "2883" matched UnwrappedPropertyConflict2883Test and then every
//     nested member through its qualified name; those used the shared
//     budget and 0 of 8 text matches were shown, one of them the edit site.
//   - symbols matched only by file path once a name match exists. Measured
//     (gin): term "Errors" listed every symbol in errors.go after the two
//     real matches.
func condenseSearchSymbols(ranked []rankedSearchSymbol) ([]rankedSearchSymbol, string) {
	listed := map[string]bool{}
	nameMatched := false
	for _, item := range ranked {
		if item.matchKind != "qualified" {
			if qn := item.symbol.QualifiedName; qn != "" {
				listed[qn] = true
			}
		}
		if strings.HasPrefix(item.matchKind, "name-") {
			nameMatched = true
		}
	}
	collapsed := map[string]int{}
	var containers []string
	pathOnly := 0
	var pathFiles []string
	seenPathFile := map[string]bool{}
	kept := make([]rankedSearchSymbol, 0, len(ranked))
	for _, item := range ranked {
		if item.matchKind == "qualified" {
			if owner := listedOwner(item.symbol.QualifiedName, listed); owner != "" {
				if collapsed[owner] == 0 {
					containers = append(containers, owner)
				}
				collapsed[owner]++
				continue
			}
		}
		if item.matchKind == "path" && nameMatched {
			pathOnly++
			if pathOnly > pathOnlySymbolCap {
				if f := item.symbol.FilePath; !seenPathFile[f] {
					seenPathFile[f] = true
					pathFiles = append(pathFiles, f)
				}
				continue
			}
		}
		kept = append(kept, item)
	}
	var notes []string
	for _, owner := range containers {
		notes = append(notes, fmt.Sprintf("%d member(s) of %s matched only through its name and are not listed (op=lookup %s for its outline)",
			collapsed[owner], owner, owner))
	}
	if n := pathOnly - pathOnlySymbolCap; n > 0 {
		notes = append(notes, fmt.Sprintf("%d more symbol(s) matched only by file path (%s) and are not listed; use paths= to list a file's symbols",
			n, strings.Join(pathFiles, ", ")))
	}
	return kept, strings.Join(notes, "; ")
}

// listedOwner returns the nearest listed enclosing qualified name of qn.
func listedOwner(qn string, listed map[string]bool) string {
	for i := strings.LastIndexByte(qn, '.'); i > 0; i = strings.LastIndexByte(qn[:i], '.') {
		if listed[qn[:i]] {
			return qn[:i]
		}
	}
	return ""
}
