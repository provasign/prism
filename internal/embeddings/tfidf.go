// Package embeddings provides Prism's semantic similarity backends. Today
// only TF-IDF is implemented (zero external deps); ONNX is wired via an
// interface so it can be added later without changing the ranking package.
package embeddings

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/tabladrum/grove-suite/prism/internal/grove"
)

// Backend is the Prism semantic-similarity interface used by the ranker.
type Backend interface {
	// Similarity returns cosine similarity in [0,1] between the task and
	// the symbol's descriptive text.
	Similarity(task string, sym grove.SymbolRecord) float64
}

// TFIDF is an in-process TF-IDF cosine similarity backend. It indexes a
// corpus of SymbolRecords once and answers queries in O(query_terms × df).
type TFIDF struct {
	mu      sync.RWMutex
	idf     map[string]float64
	vectors map[string]map[string]float64 // symbolID -> term -> tf-idf
	norms   map[string]float64            // symbolID -> L2 norm
}

// NewTFIDF builds an empty engine. Call Index() to populate.
func NewTFIDF() *TFIDF {
	return &TFIDF{
		idf:     make(map[string]float64),
		vectors: make(map[string]map[string]float64),
		norms:   make(map[string]float64),
	}
}

// Index ingests the corpus. Safe to call multiple times — replaces state.
func (e *TFIDF) Index(syms []grove.SymbolRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.idf = make(map[string]float64)
	e.vectors = make(map[string]map[string]float64, len(syms))
	e.norms = make(map[string]float64, len(syms))
	if len(syms) == 0 {
		return
	}

	// Pass 1 — term frequencies + document frequencies.
	docFreq := make(map[string]int)
	tfs := make(map[string]map[string]int, len(syms))
	for _, s := range syms {
		tokens := tokenize(documentText(s))
		if len(tokens) == 0 {
			continue
		}
		tf := make(map[string]int, len(tokens))
		for _, t := range tokens {
			tf[t]++
		}
		tfs[s.ID] = tf
		for t := range tf {
			docFreq[t]++
		}
	}
	n := float64(len(syms))
	for t, df := range docFreq {
		e.idf[t] = math.Log((n + 1) / (float64(df) + 1))
	}
	// Pass 2 — tf-idf vectors and norms.
	for id, tf := range tfs {
		v := make(map[string]float64, len(tf))
		var sumSq float64
		for term, count := range tf {
			weight := (1.0 + math.Log(float64(count))) * e.idf[term]
			v[term] = weight
			sumSq += weight * weight
		}
		e.vectors[id] = v
		e.norms[id] = math.Sqrt(sumSq)
	}
}

// Similarity returns cosine similarity between task and sym. Symbol must
// have been Index()-ed; if not, returns 0.
func (e *TFIDF) Similarity(task string, sym grove.SymbolRecord) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v, ok := e.vectors[sym.ID]
	if !ok || e.norms[sym.ID] == 0 {
		return 0
	}
	qTokens := tokenize(task)
	if len(qTokens) == 0 {
		return 0
	}
	// Build query vector inline.
	qTF := make(map[string]int, len(qTokens))
	for _, t := range qTokens {
		qTF[t]++
	}
	var dot, qSumSq float64
	for term, count := range qTF {
		idf := e.idf[term]
		if idf == 0 {
			continue
		}
		w := (1.0 + math.Log(float64(count))) * idf
		qSumSq += w * w
		if dv := v[term]; dv != 0 {
			dot += w * dv
		}
	}
	if qSumSq == 0 {
		return 0
	}
	return dot / (math.Sqrt(qSumSq) * e.norms[sym.ID])
}

// Scored pairs a symbol with its cosine score.
type Scored struct {
	Symbol grove.SymbolRecord
	Score  float64
}

// Query returns the top-limit symbols by cosine similarity. Only useful for
// "from scratch" semantic search; the ranker uses Similarity() per candidate.
func (e *TFIDF) Query(task string, corpus []grove.SymbolRecord, limit int) []Scored {
	out := make([]Scored, 0, len(corpus))
	for _, s := range corpus {
		score := e.Similarity(task, s)
		if score > 0 {
			out = append(out, Scored{Symbol: s, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// documentText joins symbol fields into the canonical text representation.
func documentText(s grove.SymbolRecord) string {
	parts := []string{s.Name, s.QualifiedName, s.Signature, s.Docstring, s.ParentSymbol}
	return strings.Join(parts, " ")
}

var stopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "of": {}, "for": {}, "to": {},
	"in": {}, "and": {}, "or": {}, "with": {}, "this": {}, "that": {},
	"be": {}, "on": {}, "as": {}, "it": {},
	"func": {}, "function": {}, "method": {}, "class": {}, "struct": {},
	"return": {}, "returns": {}, "var": {}, "let": {}, "const": {},
	"void": {}, "int": {}, "str": {}, "string": {}, "bool": {},
	"true": {}, "false": {}, "null": {}, "none": {}, "self": {},
}

func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 16)
	for _, raw := range splitNonAlnum(s) {
		for _, part := range splitCamel(raw) {
			p := strings.ToLower(part)
			if len(p) < 2 {
				continue
			}
			if _, skip := stopwords[p]; skip {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

func splitNonAlnum(s string) []string {
	out := []string{}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// splitCamel splits camelCase + acronym runs + letter↔digit boundaries.
func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	rs := []rune(s)
	out := []string{}
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		boundary := false
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			boundary = true
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			boundary = true
		case unicode.IsLetter(prev) != unicode.IsLetter(cur):
			boundary = true
		}
		if boundary {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	out = append(out, string(rs[start:]))
	return out
}
