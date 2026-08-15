package compression

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
)

// buildContent generates a synthetic Go file with n functions.
func buildContent(n int) string {
	var sb strings.Builder
	sb.WriteString("package pkg\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "// Func%d is a generated function.\nfunc Func%d(x int) int {\n\treturn x + %d\n}\n\n", i, i, i)
	}
	return sb.String()
}

func buildSymbols(filePath string, n int) []grove.SymbolRecord {
	syms := make([]grove.SymbolRecord, n)
	for i := range syms {
		name := fmt.Sprintf("Func%d", i)
		body := fmt.Sprintf("// Func%d is a generated function.\nfunc Func%d(x int) int {\n\treturn x + %d\n}", i, i, i)
		syms[i] = grove.SymbolRecord{
			FilePath:  filePath,
			Name:      name,
			Signature: fmt.Sprintf("func %s(x int) int", name),
			Kind:      "function",
			RawText:   body,
			Span:      grove.SpanInfo{Start: i * 5, End: i*5 + 4},
		}
	}
	return syms
}

// ── Fresh read (first time, no session entry) ─────────────────────────────────

func BenchmarkCompressFileRead_FreshRead_10Symbols(b *testing.B) {
	content := buildContent(10)
	syms := buildSymbols("pkg/svc.go", 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := session.NewTracker(100)
		CompressFileRead("pkg/svc.go", content, Options{
			Task:            "implement caching",
			Symbols:         syms,
			Session:         tr,
			Ledger:          session.NewLedger("bench"),
			TokenLedgerName: "prism_read",
			Confidence:      session.Low,
		})
	}
}

func BenchmarkCompressFileRead_FreshRead_50Symbols(b *testing.B) {
	content := buildContent(50)
	syms := buildSymbols("pkg/svc.go", 50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := session.NewTracker(100)
		CompressFileRead("pkg/svc.go", content, Options{
			Task:            "implement caching",
			Symbols:         syms,
			Session:         tr,
			Ledger:          session.NewLedger("bench"),
			TokenLedgerName: "prism_read",
			Confidence:      session.Low,
		})
	}
}

// ── SHA-pointer path (second read, same hash) ─────────────────────────────────

func BenchmarkCompressFileRead_SHAPointer(b *testing.B) {
	content := buildContent(20)
	syms := buildSymbols("pkg/svc.go", 20)
	tr := session.NewTracker(100)
	ledger := session.NewLedger("bench")
	opts := Options{
		Task:            "fix bug",
		Symbols:         syms,
		Session:         tr,
		Ledger:          ledger,
		TokenLedgerName: "prism_read",
		Confidence:      session.High,
	}
	// Prime the session with one read.
	CompressFileRead("pkg/svc.go", content, opts)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Same hash → sha-pointer path.
		CompressFileRead("pkg/svc.go", content, opts)
	}
}

// ── Semantic delta (second read, different hash, symbols available) ───────────

func BenchmarkCompressFileRead_SemanticDelta(b *testing.B) {
	content1 := buildContent(20)
	content2 := buildContent(20) + "// tiny change\n"
	syms := buildSymbols("pkg/svc.go", 20)
	// Pre-set symbol SHAs on first read content.
	tr := session.NewTracker(100)
	ledger := session.NewLedger("bench")
	opts := Options{
		Task:            "fix bug",
		Symbols:         syms,
		Session:         tr,
		Ledger:          ledger,
		TokenLedgerName: "prism_read",
		Confidence:      session.High,
	}
	CompressFileRead("pkg/svc.go", content1, opts) // prime

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Different hash → semantic-delta (or renderRanked fallback) path.
		CompressFileRead("pkg/svc.go", content2, opts)
	}
}
