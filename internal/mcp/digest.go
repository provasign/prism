package mcp

// One-call answers for large files.
//
// Measured on the jackson bed (2026-08-26), 36 cell x variant observations:
//
//	Δ(read CALLS) -> Δcache   r = 0.69
//	Δ(read CALLS) -> Δturns   r = 0.82
//	fewer calls: -17% cache / -2.4 turns   more calls: +27% / +4.6
//
// Cost is governed by how many times the agent asks for code, not by how
// much comes back. Every delivery policy tried before this one optimised
// payload size and lost: a bare outline is small but forces a follow-up,
// and each follow-up re-reads the whole transcript.
//
// So the objective is ANSWER IN ONE CALL. For a large file this digest
// returns the map (every symbol with its line range) AND the full bodies of
// the symbols this session is plausibly about — drawn from the read's own
// task= argument and from what the agent has been searching for. When
// nothing matches, it degrades to the outline; when the file is small, the
// caller delivers it whole as before.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

const (
	// digestMinLines: below this a whole file is a few KB and cheaper than
	// any round trip it might save.
	digestMinLines = 800
	// digestBodyBudget bounds the bodies included, in bytes. Sized so a
	// digest stays far under a whole large file (~84KB measured) while
	// carrying several real methods.
	digestBodyBudget = 14000
	// digestMaxBodies caps how many symbols get full bodies.
	digestMaxBodies = 6
	// outlineSymbolCap bounds the map itself — it must not become the
	// territory.
	outlineSymbolCap = 60
)

// fileDigest renders a large file as map + relevant bodies. ok=false means
// "not a large file / nothing indexed": deliver normally.
func (h *Handler) fileDigest(path, content string, syms []grove.SymbolRecord, hints []string) (map[string]any, bool) {
	if len(syms) == 0 {
		return nil, false
	}
	total := strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
	if total < digestMinLines {
		return nil, false
	}
	ordered := append([]grove.SymbolRecord(nil), syms...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Span.Start < ordered[j].Span.Start })

	relevant := pickRelevant(ordered, hints)

	var b strings.Builder
	fmt.Fprintf(&b, "// %d lines, %d indexed symbols. Full bodies for the %d symbol(s) matching "+
		"this session's focus are included below; everything else is listed by line range.\n",
		total, len(ordered), len(relevant))
	b.WriteString("// Need more? prism_lookup name=\"<Type.member>\" · prism_read offset=/limit= · prism_read full=true\n")

	// The map.
	shown := ordered
	if len(shown) > outlineSymbolCap {
		shown = shown[:outlineSymbolCap]
	}
	inFull := map[string]bool{}
	for _, s := range relevant {
		inFull[s.ID] = true
	}
	for _, s := range shown {
		mark := " "
		if inFull[s.ID] {
			mark = "*"
		}
		sig := strings.TrimSpace(firstLine(s.Signature))
		if sig == "" {
			sig = s.Kind
		}
		fmt.Fprintf(&b, "%s%5d-%-5d %s\n", mark, s.Span.Start, s.Span.End, truncateOutline(sig, 108))
	}
	if len(ordered) > outlineSymbolCap {
		fmt.Fprintf(&b, "// +%d more symbols — prism_search to locate one by name\n", len(ordered)-outlineSymbolCap)
	}

	// The bodies.
	for _, s := range relevant {
		name := s.QualifiedName
		if name == "" {
			name = s.Name
		}
		fmt.Fprintf(&b, "\n// ── %s  (lines %d-%d) ──\n%s\n", name, s.Span.Start, s.Span.End,
			strings.TrimRight(s.RawText, "\n"))
	}
	return map[string]any{
		"file": path, "strategy": "digest", "totalLines": total,
		"bodiesIncluded": len(relevant), "content": b.String(),
	}, true
}

// pickRelevant selects the symbols a session focused on these hints most
// likely wants in full, best match first, within budget.
//
// Ranking matters as much as matching: a first cut spent three of six body
// slots on ONE-LINE FIELD DECLARATIONS (`_anySetter` at 137-137) that the
// outline already lists, because they matched a hint token in their
// signature. Bodies are the scarce resource here — they go to symbols that
// have a body worth reading, preferring an exact name hit over an
// incidental mention.
func pickRelevant(syms []grove.SymbolRecord, hints []string) []grove.SymbolRecord {
	type scored struct {
		s    grove.SymbolRecord
		rank int
	}
	var cands []scored
	seen := map[string]bool{}
	for hi, hint := range hints {
		for _, tok := range hintTokens(hint) {
			lt := strings.ToLower(tok)
			for _, s := range syms {
				if seen[s.ID] || s.RawText == "" {
					continue
				}
				// A declaration is not a body: the outline already gives its
				// line and signature, so spending a slot on it buys nothing.
				if s.Span.End-s.Span.Start < 2 {
					continue
				}
				r := 0
				switch {
				case strings.EqualFold(s.Name, tok):
					r = 100
				case strings.Contains(strings.ToLower(s.Name), lt):
					r = 60
				case strings.Contains(strings.ToLower(s.QualifiedName), lt):
					r = 50
				case strings.Contains(strings.ToLower(s.Signature), lt),
					strings.Contains(strings.ToLower(s.Docstring), lt):
					r = 20
				default:
					continue
				}
				seen[s.ID] = true
				cands = append(cands, scored{s, r - hi}) // earlier hints win ties
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank > cands[j].rank })
	var out []grove.SymbolRecord
	budget := digestBodyBudget
	for _, c := range cands {
		if len(out) >= digestMaxBodies || len(c.s.RawText) > budget {
			continue
		}
		budget -= len(c.s.RawText)
		out = append(out, c.s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Span.Start < out[j].Span.Start })
	return out
}

// hintTokens splits a hint ("BeanDeserializerBase.deserializeFromObject",
// "fix the null handling in resolve") into matchable identifier tokens,
// dropping words too short or too generic to discriminate.
func hintTokens(h string) []string {
	repl := func(r rune) rune {
		if r == '.' || r == '(' || r == ')' || r == ',' || r == '"' || r == '\'' {
			return ' '
		}
		return r
	}
	var out []string
	for _, w := range strings.Fields(strings.Map(repl, h)) {
		if len(w) < 4 || genericWord[strings.ToLower(w)] {
			continue
		}
		out = append(out, w)
	}
	return out
}

var genericWord = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "when": true,
	"then": true, "test": true, "tests": true, "code": true, "file": true,
	"class": true, "method": true, "fix": true, "bug": true, "issue": true,
	"java": true, "null": true, "true": true, "false": true, "return": true,
	"public": true, "private": true, "static": true, "void": true, "value": true,
}

func symbolMentions(s grove.SymbolRecord, tok string) bool {
	lt := strings.ToLower(tok)
	for _, hay := range []string{s.Name, s.QualifiedName, s.Signature, s.Docstring} {
		if hay != "" && strings.Contains(strings.ToLower(hay), lt) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateOutline(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " …"
}
