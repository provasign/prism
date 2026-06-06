package compression

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

func makeSymbols(filePath string, n int) []grove.SymbolRecord {
	syms := make([]grove.SymbolRecord, n)
	for i := range syms {
		syms[i] = grove.SymbolRecord{
			FilePath:  filePath,
			Name:      "Func" + string(rune('A'+i)),
			Signature: "func Func" + string(rune('A'+i)) + "()",
			Kind:      "function",
			Span:      grove.SpanInfo{Start: i * 10, End: i*10 + 5},
		}
	}
	return syms
}

func freshOpts(tracker *session.Tracker, syms []grove.SymbolRecord) Options {
	return Options{
		Task:            "test task",
		Symbols:         syms,
		Session:         tracker,
		Ledger:          session.NewLedger("test"),
		TokenLedgerName: "prism_read",
		Confidence:      session.High,
	}
}

// TestFreshRead verifies that the first read of a file is delivered in full,
// byte-for-byte. Prism never trims symbol bodies on a first read — doing so
// silently drops content the model has never seen.
func TestFreshRead(t *testing.T) {
	tracker := session.NewTracker(100)
	syms := makeSymbols("pkg/foo.go", 3)
	content := "package pkg\n\nfunc FuncA() {}\nfunc FuncB() {}\nfunc FuncC() {}\n"

	r := CompressFileRead("pkg/foo.go", content, freshOpts(tracker, syms))
	if r.Strategy != "full-fresh" {
		t.Errorf("first read: want full-fresh (faithful), got %s", r.Strategy)
	}
	if r.Content != content {
		t.Errorf("first read must be byte-faithful, got:\n%s", r.Content)
	}
	if r.OriginalTokens == 0 {
		t.Error("originalTokens must be > 0")
	}
}

// TestReReadSHAPointer verifies that the 2nd read of an unchanged file emits a
// sha-pointer regardless of confidence level, and that the pointer is a single
// short line containing the SHA prefix and file path.
func TestReReadSHAPointer(t *testing.T) {
	// Use realistic-sized content so savings % is positive.
	// sha-pointer costs ~28 tokens; original must be significantly larger.
	content := strings.Repeat("// line of code with some substance\n", 30) +
		"package pkg\n\nfunc FuncA() { /* body */ }\nfunc FuncB(x int) (string, error) { return \"\", nil }\n"
	syms := makeSymbols("pkg/bar.go", 2)

	for _, conf := range []session.Confidence{session.High, session.Medium, session.Low} {
		tracker := session.NewTracker(100)
		opts := freshOpts(tracker, syms)
		opts.Confidence = conf

		// Round 1: fresh read — populates session
		CompressFileRead("pkg/bar.go", content, opts)

		// Round 2: re-read same content — should be sha-pointer
		opts.Confidence = conf
		r2 := CompressFileRead("pkg/bar.go", content, opts)

		if r2.Strategy != "sha-pointer" {
			t.Errorf("conf=%s: 2nd read: want sha-pointer, got %s", conf, r2.Strategy)
		}
		if !strings.Contains(r2.Content, "pkg/bar.go") {
			t.Errorf("sha-pointer must contain file path, got: %q", r2.Content)
		}
		if !strings.Contains(r2.Content, "@sha:") {
			t.Errorf("sha-pointer must contain @sha: prefix, got: %q", r2.Content)
		}
		if !strings.Contains(r2.Content, "no changes") {
			t.Errorf("sha-pointer must say 'no changes', got: %q", r2.Content)
		}
		// sha-pointer is one line, typically ~25–35 tokens regardless of file size.
		if r2.DeliveredTokens > 40 {
			t.Errorf("sha-pointer should cost ≤40 tokens, got %d", r2.DeliveredTokens)
		}
		if r2.SavingsPercent < 80.0 {
			t.Errorf("sha-pointer should save ≥80%% on a realistic file, got %.1f%%", r2.SavingsPercent)
		}
	}
}

// TestThirdReadSHAPointerHighConf verifies that 3rd read with High confidence
// also gets a sha-pointer (still within 30% of window).
func TestThirdReadSHAPointerHighConf(t *testing.T) {
	content := strings.Repeat("// enough code to make a cache pointer cheaper\n", 20) +
		"package pkg\n\nfunc X() {}\n"
	syms := makeSymbols("pkg/x.go", 1)
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	CompressFileRead("pkg/x.go", content, opts) // R1
	CompressFileRead("pkg/x.go", content, opts) // R2
	opts.Confidence = session.High
	r3 := CompressFileRead("pkg/x.go", content, opts) // R3

	if r3.Strategy != "sha-pointer" {
		t.Errorf("3rd read High: want sha-pointer, got %s", r3.Strategy)
	}
}

// TestThirdReadMediumConf verifies 3rd read with Medium confidence uses
// session-signature (refresher but not full resend).
func TestThirdReadMediumConf(t *testing.T) {
	content := strings.Repeat("// enough code to make signatures cheaper than full content\n", 20) +
		"package pkg\n\nfunc X() {}\nfunc Y() {}\n"
	syms := makeSymbols("pkg/xy.go", 2)
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	CompressFileRead("pkg/xy.go", content, opts) // R1
	CompressFileRead("pkg/xy.go", content, opts) // R2 (sha-pointer)
	opts.Confidence = session.Medium
	r3 := CompressFileRead("pkg/xy.go", content, opts) // R3

	if r3.Strategy != "session-signature" {
		t.Errorf("3rd read Medium: want session-signature, got %s", r3.Strategy)
	}
}

// TestFourthReadEscalated verifies that the 4th read always forces full delivery.
func TestFourthReadEscalated(t *testing.T) {
	content := strings.Repeat("// line\n", 50)
	syms := makeSymbols("pkg/big.go", 4)
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	for i := 0; i < 3; i++ {
		CompressFileRead("pkg/big.go", content, opts)
	}
	opts.Confidence = session.High
	r4 := CompressFileRead("pkg/big.go", content, opts)

	if r4.Strategy != "escalated-full" {
		t.Errorf("4th read: want escalated-full, got %s", r4.Strategy)
	}
	if r4.DeliveredTokens != r4.OriginalTokens {
		t.Errorf("escalated-full must deliver all tokens: %d vs %d", r4.DeliveredTokens, r4.OriginalTokens)
	}
}

func TestCompressionNeverExpandsTinyFiles(t *testing.T) {
	content := "package pkg\n\nfunc Tiny() {}\n"
	syms := makeSymbols("pkg/tiny.go", 1)
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	CompressFileRead("pkg/tiny.go", content, opts)
	r2 := CompressFileRead("pkg/tiny.go", content, opts)

	if r2.DeliveredTokens > r2.OriginalTokens {
		t.Fatalf("tiny file delivery expanded: delivered=%d original=%d", r2.DeliveredTokens, r2.OriginalTokens)
	}
	if r2.SavingsPercent < 0 {
		t.Fatalf("tiny file savings must not be negative, got %.1f", r2.SavingsPercent)
	}
	if r2.Strategy != "full-fresh" {
		t.Fatalf("tiny expanded cache pointer should fall back to full-fresh, got %s", r2.Strategy)
	}
}

// TestChangedContentRestartsFromFresh verifies that if the file content changes
// the session counter resets and a fresh compressed delivery is made.
func TestChangedContentRestartsFromFresh(t *testing.T) {
	v1 := "package pkg\n\nfunc A() {}\n"
	v2 := "package pkg\n\nfunc A() {}\nfunc B() {} // added\n"
	syms := makeSymbols("pkg/c.go", 2)
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	CompressFileRead("pkg/c.go", v1, opts)       // R1 v1
	CompressFileRead("pkg/c.go", v1, opts)       // R2 v1 → sha-pointer
	r3 := CompressFileRead("pkg/c.go", v2, opts) // R3 v2 — content changed

	// makeSymbols carries no RawText, so no per-symbol SHAs were recorded and a
	// lossless delta is impossible — the changed file is re-delivered in full.
	if r3.Strategy != "full-fresh" {
		t.Errorf("changed content without symbol SHAs: want full-fresh, got %s", r3.Strategy)
	}
}

// TestRenderSHAPointer checks the exact format of the sha-pointer line.
func TestRenderSHAPointer(t *testing.T) {
	hash := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	out := renderSHAPointer("internal/auth/service.go", hash, 2)

	if !strings.HasPrefix(out, "// [prism:cached]") {
		t.Errorf("must start with '// [prism:cached]', got: %q", out)
	}
	if !strings.Contains(out, "internal/auth/service.go") {
		t.Errorf("must contain file path")
	}
	if !strings.Contains(out, "@sha:abcdef12") {
		t.Errorf("must contain first 8 chars of hash, got: %q", out)
	}
	if !strings.Contains(out, "×2") {
		t.Errorf("must contain access count, got: %q", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Errorf("sha-pointer must be exactly 1 line, got %d: %q", len(lines), out)
	}
	tokens := ranking.EstimateTokens(out)
	if tokens > 40 {
		t.Errorf("sha-pointer must cost ≤40 tokens, got %d for: %q", tokens, out)
	}
}

func TestTruncateToTokens_BelowCap(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := truncateToTokens(s, 100) // maxBytes = 400, len(s)=100 <= 400
	if got != s {
		t.Errorf("should return unchanged")
	}
}

func TestTruncateToTokens_ExceedsCap(t *testing.T) {
	s := strings.Repeat("x", 1000)
	got := truncateToTokens(s, 10) // maxBytes = 40, len(s)=1000 > 40
	if !strings.HasSuffix(got, "[truncated by Prism MaxTokensPerFile cap]\n") {
		t.Errorf("expected truncation marker, got %q", got[max(0, len(got)-60):])
	}
	if len(got) <= 40 {
		t.Errorf("got too short: %d bytes", len(got))
	}
}

// --- D: Semantic delta encoding ------------------------------------------

// fileWithBodies concatenates the given symbol bodies into a file and returns
// both the file content and symbols whose 1-based line spans match exactly.
// Realistic spans are required: the lossless delta encoder reconstructs the
// file from its own lines and refuses to operate on out-of-range spans.
func fileWithBodies(filePath string, bodies []string) (string, []grove.SymbolRecord) {
	var b strings.Builder
	syms := make([]grove.SymbolRecord, len(bodies))
	line := 1
	for i, body := range bodies {
		bt := body
		if !strings.HasSuffix(bt, "\n") {
			bt += "\n"
		}
		nlines := strings.Count(bt, "\n")
		syms[i] = grove.SymbolRecord{
			FilePath:  filePath,
			Name:      "Func" + string(rune('A'+i)),
			Signature: "func Func" + string(rune('A'+i)) + "()",
			Kind:      "function",
			RawText:   bt,
			Span:      grove.SpanInfo{Start: line, End: line + nlines - 1},
		}
		b.WriteString(bt)
		line += nlines
	}
	return b.String(), syms
}

// TestSemanticDelta_ChangedFile verifies that re-reading a file whose content
// changed produces the "semantic-delta" strategy (not compressed-fresh) when
// symbol SHAs were recorded on the first read.
func TestSemanticDelta_ChangedFile(t *testing.T) {
	v1bodies := []string{
		"func FuncA() { return 1 }\n",
		"func FuncB() { return 2 }\n",
		strings.Repeat("// unchanged padding\n", 10),
	}
	v2bodies := []string{
		"func FuncA() { return 1 }\n",                // unchanged
		"func FuncB() { return 999 /* edited */ }\n", // changed
		strings.Repeat("// unchanged padding\n", 10),
	}
	v1, symsV1 := fileWithBodies("pkg/delta.go", v1bodies)
	v2, symsV2 := fileWithBodies("pkg/delta.go", v2bodies)

	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, symsV1)

	// R1: first read — records symbol SHAs.
	CompressFileRead("pkg/delta.go", v1, opts)

	// R2: file content changed — should trigger semantic-delta.
	opts.Symbols = symsV2
	r2 := CompressFileRead("pkg/delta.go", v2, opts)

	if r2.Strategy != "semantic-delta" {
		t.Errorf("changed file with symbol SHAs: want semantic-delta, got %s", r2.Strategy)
	}
	// Unchanged symbol should be a [prism:cached] pointer.
	if !strings.Contains(r2.Content, "[prism:cached] FuncA") {
		t.Errorf("FuncA unchanged: want [prism:cached] pointer in delta, got:\n%s", r2.Content)
	}
	// Changed symbol should appear as full source.
	if !strings.Contains(r2.Content, "func FuncB()") {
		t.Errorf("FuncB changed: want full body in delta, got:\n%s", r2.Content)
	}
	// Delta should save tokens vs the original.
	if r2.DeliveredTokens >= r2.OriginalTokens {
		t.Errorf("delta should deliver fewer tokens: delivered=%d original=%d", r2.DeliveredTokens, r2.OriginalTokens)
	}
}

// TestSemanticDelta_NoSymbolSHAs verifies that without prior symbol SHAs
// (first read ever), a changed file falls back to compressed-fresh.
func TestSemanticDelta_NoSymbolSHAs(t *testing.T) {
	v1 := "package pkg\n\nfunc A() {}\n"
	v2 := "package pkg\n\nfunc A() {}\nfunc B() {} // new\n"
	syms := makeSymbols("pkg/nodelta.go", 2)
	// Fresh tracker — no prior reads, so no symbol SHAs stored.
	tracker := session.NewTracker(100)
	opts := freshOpts(tracker, syms)

	// First read of v1 populates entry but makeSymbols has no RawText → no SHAs.
	CompressFileRead("pkg/nodelta.go", v1, opts)

	// Now v2 is different content — should NOT produce semantic-delta
	// (no SHAs means we can't delta-encode).
	r2 := CompressFileRead("pkg/nodelta.go", v2, opts)
	if r2.Strategy == "semantic-delta" {
		t.Errorf("without symbol SHAs: should not produce semantic-delta, got %s", r2.Strategy)
	}
}
