// Package compression implements the file-read compression pipeline that
// produces a token-optimized rendering of a file given Grove's symbols and
// the current session state.
package compression

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// MaxTokensPerFile caps the size of any single compressed file response.
const MaxTokensPerFile = 50000

// Result is the output of CompressFileRead.
type Result struct {
	FilePath        string
	Content         string
	Strategy        string // "full-fresh" | "semantic-delta" | "session-signature" | "sha-pointer" | "escalated-full"
	OriginalTokens  int
	DeliveredTokens int
	SavingsPercent  float64
}

// Hash returns the SHA-256 hex of content. Used as the cache key.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Options configures CompressFileRead.
//
// Task and Embeddings are retained for API compatibility and potential future
// use, but they intentionally do NOT influence a first read: Prism delivers the
// complete file on first read and never trims symbol bodies by relevance, since
// a lossy first-read reconstruction silently drops content the model has never
// seen. All compression happens on the safe re-read paths (sha-pointer and the
// lossless semantic delta), which key off Session state, not relevance.
type Options struct {
	Task            string               // unused by file-read compression (see above)
	Symbols         []grove.SymbolRecord // symbols in this file (from Grove)
	Session         *session.Tracker
	Ledger          *session.Ledger
	TokenLedgerName string // tool name to bill ledger ("prism_read")
	Confidence      session.Confidence
	ContextUsed     int64                   // agent-reported context size at this call (0 = not reported)
	Embeddings      ranking.SemanticBackend // unused by file-read compression (see above)
}

// CompressFileRead returns the rendered output and ledger metadata.
func CompressFileRead(filePath, content string, opts Options) Result {
	originalTokens := ranking.EstimateTokens(content)
	r := Result{FilePath: filePath, OriginalTokens: originalTokens}
	hash := Hash(content)

	// Session lookup — decide on re-read strategy.
	entry, seen, sameHash := tryLookup(opts.Session, filePath, hash)

	// Re-read of unchanged file → use session-aware strategy.
	if seen && sameHash {
		switch {
		case entry.AccessCount >= 3 && opts.Confidence == session.High:
			// 4th+ read but the prior delivery is still demonstrably within
			// the attention window — re-sending the full file would spend
			// tokens fighting a problem that isn't present.
			r.Strategy = "sha-pointer"
			r.Content = renderSHAPointer(filePath, hash, entry.AccessCount)
			r.DeliveredTokens = ranking.EstimateTokens(r.Content)

		case entry.AccessCount >= 3:
			// 4th+ read at medium/low confidence: force full re-delivery.
			// The original content has likely scrolled out of the model's
			// effective attention window.
			r.Strategy = "escalated-full"
			r.Content = content
			r.DeliveredTokens = originalTokens

		case entry.AccessCount == 2:
			// 3rd read: use confidence to decide.
			switch opts.Confidence {
			case session.High:
				// Still within 30% of context window → a SHA pointer is enough.
				r.Strategy = "sha-pointer"
				r.Content = renderSHAPointer(filePath, hash, entry.AccessCount)
			case session.Medium:
				// 30–70% through the window → send signatures as a refresher.
				r.Strategy = "session-signature"
				r.Content = renderSignatures(opts.Symbols)
			default:
				// Past 70% of window → the prior delivery has almost certainly
				// scrolled out of attention. Re-deliver the complete file: a
				// lossy "refresh" of unchanged content the agent can no longer
				// see would be worse than no compression at all.
				r.Strategy = "full-fresh"
				r.Content = content
			}
			r.DeliveredTokens = ranking.EstimateTokens(r.Content)

		default:
			// 2nd read (AccessCount == 1): content was just delivered; a SHA
			// pointer is always sufficient regardless of confidence level.
			r.Strategy = "sha-pointer"
			r.Content = renderSHAPointer(filePath, hash, entry.AccessCount)
			r.DeliveredTokens = ranking.EstimateTokens(r.Content)
		}
	} else {
		// Fresh read, or content changed since last delivery.
		//
		// On a FIRST read Prism delivers the complete, byte-faithful file. We
		// deliberately do not trim or re-rank symbol bodies here: a lossy
		// first-read reconstruction silently drops inter-symbol content
		// (comments, statements a parser did not model as symbols, whole
		// columns of a SQL table) and is the single biggest source of
		// "the model can't read the file" failures. All of Prism's genuine
		// savings come from the SAFE re-read paths (sha-pointer, semantic
		// delta, escalated refresh), where the agent already received the
		// full content earlier in the session.
		if seen && !sameHash && len(opts.Symbols) > 0 && len(entry.SymbolSHAs) > 0 {
			// File was edited since last read. Attempt a lossless delta: pointer
			// the symbols whose body is unchanged (the agent still has them from
			// the first faithful delivery) and re-send everything else verbatim.
			if delta, ok := renderSemanticDelta(opts.Symbols, content, entry.SymbolSHAs); ok &&
				ranking.EstimateTokens(delta) < originalTokens {
				r.Strategy = "semantic-delta"
				r.Content = delta
			} else {
				// Spans untrustworthy, nothing saved, or delta expanded — deliver
				// the full file rather than risk a lossy rendering.
				r.Strategy = "full-fresh"
				r.Content = content
			}
		} else {
			r.Strategy = "full-fresh"
			r.Content = content
		}
		r.DeliveredTokens = ranking.EstimateTokens(r.Content)
	}

	// Safety cap.
	if r.DeliveredTokens > MaxTokensPerFile {
		r.Content = truncateToTokens(r.Content, MaxTokensPerFile)
		r.DeliveredTokens = ranking.EstimateTokens(r.Content)
	}
	if r.OriginalTokens > 0 && r.DeliveredTokens > r.OriginalTokens && r.Strategy != "full-fresh" && r.Strategy != "escalated-full" {
		r.Strategy = "full-fresh"
		r.Content = content
		r.DeliveredTokens = r.OriginalTokens
	}

	if r.OriginalTokens > 0 {
		r.SavingsPercent = (1.0 - float64(r.DeliveredTokens)/float64(r.OriginalTokens)) * 100.0
	}

	// Record in session + ledger.
	if opts.Session != nil {
		opts.Session.Record(filePath, hash, int64(r.DeliveredTokens), r.Strategy)
		opts.Session.RecordContextUsed(filePath, opts.ContextUsed)
		// Keep symbol-level SHAs up-to-date so the next read can delta-encode.
		if len(opts.Symbols) > 0 {
			opts.Session.UpdateSymbolSHAs(filePath, computeSymbolSHAs(opts.Symbols))
		}
	}
	if opts.Ledger != nil && opts.TokenLedgerName != "" {
		opts.Ledger.Record(opts.TokenLedgerName, r.OriginalTokens, r.DeliveredTokens)
	}
	return r
}

func tryLookup(s *session.Tracker, path, hash string) (*session.Entry, bool, bool) {
	if s == nil {
		return nil, false, false
	}
	return s.Lookup(path, hash)
}

func renderSignatures(syms []grove.SymbolRecord) string {
	var sb strings.Builder
	for _, s := range syms {
		sb.WriteString(ranking.Render(s, ranking.DisclosureSignature))
		sb.WriteString("\n")
	}
	return sb.String()
}

// SHAPointer is the exported form of renderSHAPointer for delivery surfaces
// outside the compressor (prism_explore emits the same cache pointer when a
// file's full content was already delivered this session).
func SHAPointer(filePath, contentHash string, accessCount int) string {
	return renderSHAPointer(filePath, contentHash, accessCount)
}

// renderSHAPointer emits a single-line cache reference that costs ~10 tokens.
// The model already has the file content from the prior delivery; this line
// confirms the content is unchanged and suppresses a full resend.
// Format: // [prism:cached] path/to/file.go @sha:a1b2c3d4 (seen ×N, no changes)
func renderSHAPointer(filePath, contentHash string, accessCount int) string {
	short := contentHash
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("// [prism:cached] %s @sha:%s (seen ×%d, no changes — prior delivery still in context)\n",
		filePath, short, accessCount)
}

// SymbolKey is the identity a symbol's SHA is stored under: the qualified
// name when present (unique within a file since Grove v0.6.0 qualifies
// members by parent), else the bare name. Keying by bare name alone made
// same-named members in one file (two receivers' Close()) collide, which
// could pointer a changed body inside a "lossless" delta.
func SymbolKey(s grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

// computeSymbolSHAs returns a map of SymbolKey → SHA-256(RawText) for each
// symbol. BlobSha is used if RawText is empty (Grove may populate either).
// Keys that collide within the file are dropped entirely: a SHA whose
// identity is ambiguous cannot be trusted for delta encoding.
func computeSymbolSHAs(syms []grove.SymbolRecord) map[string]string {
	m := make(map[string]string, len(syms))
	dup := map[string]bool{}
	for _, s := range syms {
		key := SymbolKey(s)
		if key == "" {
			continue
		}
		if _, exists := m[key]; exists {
			dup[key] = true
			continue
		}
		switch {
		case s.RawText != "":
			m[key] = Hash(s.RawText)
		case s.BlobSha != "":
			m[key] = s.BlobSha
		}
	}
	for key := range dup {
		delete(m, key)
	}
	return m
}

// renderSemanticDelta reconstructs the file from its own lines, replacing only
// the bodies of symbols whose content is unchanged since the last read with a
// compact, recoverable [prism:cached] pointer. Every other byte — inter-symbol
// comments, blank lines, statements no parser modelled as a symbol, and the
// full text of changed/new symbols — is emitted verbatim. This makes the delta
// lossless: the agent never silently loses content, and a pointered symbol was
// already delivered in full earlier in the session.
//
// It returns ok=false (and the caller delivers the full file) when the symbol
// spans cannot be trusted to reconstruct the file — out-of-range or overlapping
// line ranges, no usable per-symbol SHAs, or nothing actually changed at symbol
// granularity. Refusing to guess is what keeps the rendering safe.
func renderSemanticDelta(symbols []grove.SymbolRecord, content string, prevSHAs map[string]string) (string, bool) {
	if len(symbols) == 0 || len(prevSHAs) == 0 {
		return "", false
	}

	// Treat the file as 1-based inclusive lines. Drop the synthetic trailing
	// empty element produced when the file ends in a newline so reconstruction
	// does not double it.
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	lineCount := len(lines)

	type spanSym struct {
		sym        grove.SymbolRecord
		start, end int // 1-based inclusive
	}
	spans := make([]spanSym, 0, len(symbols))
	for _, s := range symbols {
		st, en := s.Span.Start, s.Span.End
		if st < 1 || en < st || en > lineCount {
			return "", false // span out of range — cannot reconstruct losslessly
		}
		spans = append(spans, spanSym{sym: s, start: st, end: en})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start <= spans[i-1].end {
			return "", false // overlapping spans would duplicate or drop lines
		}
	}

	currentSHAs := computeSymbolSHAs(symbols)
	var sb strings.Builder
	emit := func(from, to int) { // emit verbatim 1-based inclusive line range
		for i := from; i <= to; i++ {
			sb.WriteString(lines[i-1])
			sb.WriteByte('\n')
		}
	}

	pointered := 0
	cursor := 1
	for _, sp := range spans {
		if sp.start > cursor {
			emit(cursor, sp.start-1) // gap before this symbol — verbatim
		}
		cur := currentSHAs[SymbolKey(sp.sym)]
		prev := prevSHAs[SymbolKey(sp.sym)]
		if cur != "" && prev != "" && cur == prev {
			// Body unchanged: the agent already has it from the first faithful
			// read. Emit a pointer. Any surrounding edit (a new comment, a moved
			// blank line) is still captured verbatim as gap content above/below,
			// so the delta stays lossless even when only inter-symbol text moved.
			sb.WriteString(deltaPointer(sp.sym, cur))
			pointered++
		} else {
			emit(sp.start, sp.end) // changed/new symbol — verbatim from the file
		}
		cursor = sp.end + 1
	}
	if cursor <= lineCount {
		emit(cursor, lineCount) // trailing gap — verbatim
	}
	if pointered == 0 {
		// Every symbol body changed — there is nothing to pointer, so the
		// "delta" is just the full file with extra sentinel noise. Let the
		// caller deliver the file cleanly instead.
		return "", false
	}
	return sb.String(), true
}

// deltaPointer renders the single-line, recoverable placeholder substituted for
// an unchanged symbol body. The comment marker is chosen to match the file's
// language so the rendering stays syntactically valid, and it names a
// prism_lookup key so the agent can re-expand the body on demand.
func deltaPointer(sym grove.SymbolRecord, sha string) string {
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	name := sym.QualifiedName
	if name == "" {
		name = sym.Name
	}
	// Keep this line compact: it must be cheaper than the body it replaces.
	// The symbol name is a valid prism_lookup key, so the body is recoverable
	// without spelling that out on every line.
	return fmt.Sprintf("%s [prism:cached] %s @sha:%s (unchanged — prism_lookup to expand)\n",
		commentPrefix(sym.Language), name, short)
}

// commentPrefix returns the single-line comment token for a language so Prism's
// injected sentinel lines do not corrupt the file when an agent copies them.
// Defaults to "//" for the large C-family/Go/JS/etc. majority.
func commentPrefix(language string) string {
	switch strings.ToLower(language) {
	case "sql", "lua", "haskell", "ada":
		return "--"
	case "python", "ruby", "shell", "bash", "sh", "zsh", "yaml", "yml",
		"toml", "perl", "r", "makefile", "dockerfile", "elixir", "powershell":
		return "#"
	default:
		return "//"
	}
}

func truncateToTokens(s string, maxTok int) string {
	maxBytes := maxTok * 4
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n// ... [truncated by Prism MaxTokensPerFile cap]\n"
}
