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
	Strategy        string // "full-fresh" | "compressed-fresh" | "session-reference" | "session-signature" | "escalated-full"
	OriginalTokens  int
	DeliveredTokens int
	SavingsPercent  float64
}

// Hash returns the SHA-256 hex of content. Used as the cache key.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// CompressFileRead produces a fidelity-appropriate rendering of a file based
// on the symbols Grove has indexed for it, the current task, and the
// session state.
//
// The "task" is optional context for ranking; if empty, all symbols are
// treated as equally relevant.
type Options struct {
	Task            string
	Symbols         []grove.SymbolRecord // symbols in this file (from Grove)
	Session         *session.Tracker
	Ledger          *session.Ledger
	TokenLedgerName string // tool name to bill ledger ("prism_read")
	Confidence      session.Confidence
	Embeddings      ranking.SemanticBackend // optional; nil → no semantic signal
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
		case entry.AccessCount >= 3:
			// 4th+ read: force full re-delivery regardless of confidence.
			// The original content has almost certainly scrolled out of the
			// model's effective attention window.
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
				// Past 70% of window → recompress and resend.
				r.Strategy = "compressed-fresh"
				r.Content = renderRanked(opts, content)
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
		// Fresh read (or content changed).
		if len(opts.Symbols) == 0 {
			r.Strategy = "full-fresh"
			r.Content = content
			r.DeliveredTokens = originalTokens
		} else {
			r.Strategy = "compressed-fresh"
			r.Content = renderRanked(opts, content)
			r.DeliveredTokens = ranking.EstimateTokens(r.Content)
		}
	}

	// Safety cap.
	if r.DeliveredTokens > MaxTokensPerFile {
		r.Content = truncateToTokens(r.Content, MaxTokensPerFile)
		r.DeliveredTokens = ranking.EstimateTokens(r.Content)
	}

	if r.OriginalTokens > 0 {
		r.SavingsPercent = (1.0 - float64(r.DeliveredTokens)/float64(r.OriginalTokens)) * 100.0
	}

	// Record in session + ledger.
	if opts.Session != nil {
		opts.Session.Record(filePath, hash, int64(r.DeliveredTokens), r.Strategy)
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

// renderRanked picks the right disclosure per symbol; for non-relevant ones
// emits a signature line. Falls back to full content when symbols don't
// cover the whole file (best-effort compression, not lossless).
func renderRanked(opts Options, full string) string {
	if len(opts.Symbols) == 0 {
		return full
	}
	// Sort by span start for stable output.
	syms := append([]grove.SymbolRecord(nil), opts.Symbols...)
	sort.SliceStable(syms, func(i, j int) bool {
		return syms[i].Span.Start < syms[j].Span.Start
	})
	var sb strings.Builder
	sb.WriteString("// file: ")
	sb.WriteString(opts.Symbols[0].FilePath)
	sb.WriteString("\n")
	for _, s := range syms {
		score := 0.0
		if opts.Embeddings != nil && opts.Task != "" {
			score = opts.Embeddings.Similarity(opts.Task, s)
		}
		level := ranking.DisclosureFull
		if opts.Task != "" && score < ranking.RelevanceThreshold {
			level = ranking.DisclosureSignature
		}
		sb.WriteString(ranking.Render(s, level))
		sb.WriteString("\n\n")
	}
	out := sb.String()
	// If our reconstruction is somehow larger than the original (small files
	// with rich docstrings), fall back to original.
	if len(out) > len(full) {
		return full
	}
	return out
}

func renderSignatures(syms []grove.SymbolRecord) string {
	var sb strings.Builder
	for _, s := range syms {
		sb.WriteString(ranking.Render(s, ranking.DisclosureSignature))
		sb.WriteString("\n")
	}
	return sb.String()
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

func truncateToTokens(s string, maxTok int) string {
	maxBytes := maxTok * 4
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n// ... [truncated by Prism MaxTokensPerFile cap]\n"
}
