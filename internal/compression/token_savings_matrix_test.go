package compression

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
)

type turn struct {
	content    string
	syms       []grove.SymbolRecord
	confidence session.Confidence
}

func syntheticFile(path string, funcs int, changedBodyIdx int) (string, []grove.SymbolRecord) {
	var b strings.Builder
	b.WriteString("package bench\n\n")
	syms := make([]grove.SymbolRecord, 0, funcs)
	line := 3
	for i := 0; i < funcs; i++ {
		name := fmt.Sprintf("Func%d", i)
		ret := i
		if i == changedBodyIdx {
			ret = 1000 + i
		}
		raw := fmt.Sprintf("func %s(x int) int {\n\treturn x + %d\n}\n", name, ret)
		b.WriteString(raw)
		b.WriteString("\n")
		syms = append(syms, grove.SymbolRecord{
			FilePath:  path,
			Name:      name,
			Kind:      "function",
			Signature: fmt.Sprintf("func %s(x int) int", name),
			RawText:   raw,
			Span:      grove.SpanInfo{Start: line, End: line + 2},
		})
		line += 4
	}
	return b.String(), syms
}

func runTurns(path string, turns []turn) (original int, delivered int) {
	tr := session.NewTracker(200)
	ledger := session.NewLedger("token-matrix")
	for _, t := range turns {
		r := CompressFileRead(path, t.content, Options{
			Task:            "complex token savings matrix",
			Symbols:         t.syms,
			Session:         tr,
			Ledger:          ledger,
			TokenLedgerName: "prism_read",
			Confidence:      t.confidence,
		})
		original += r.OriginalTokens
		delivered += r.DeliveredTokens
	}
	return original, delivered
}

func percentSavings(original, delivered int) float64 {
	if original == 0 {
		return 0
	}
	return (1.0 - float64(delivered)/float64(original)) * 100.0
}

func TestTokenSavings_MultiTurnMatrix(t *testing.T) {
	v1Content, v1Syms := syntheticFile("pkg/matrix.go", 30, -1)
	v2Content, v2Syms := syntheticFile("pkg/matrix.go", 30, 7) // one changed function body

	cases := []struct {
		name       string
		turns      []turn
		minSavings float64
	}{
		{
			name: "single-turn compressed-fresh",
			turns: []turn{{
				content:    v1Content,
				syms:       v1Syms,
				confidence: session.High,
			}},
			minSavings: 40,
		},
		{
			name: "two-turn unchanged with sha-pointer",
			turns: []turn{
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v1Content, syms: v1Syms, confidence: session.High},
			},
			minSavings: 60,
		},
		{
			name: "three-turn medium-confidence refresher",
			turns: []turn{
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v1Content, syms: v1Syms, confidence: session.Medium},
			},
			minSavings: 60,
		},
		{
			name: "three-turn low-confidence recompress",
			turns: []turn{
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v1Content, syms: v1Syms, confidence: session.Low},
			},
			minSavings: 55,
		},
		{
			name: "two-turn changed file semantic-delta",
			turns: []turn{
				{content: v1Content, syms: v1Syms, confidence: session.High},
				{content: v2Content, syms: v2Syms, confidence: session.High},
			},
			minSavings: 40,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original, delivered := runTurns("pkg/matrix.go", tc.turns)
			s := percentSavings(original, delivered)
			if s < tc.minSavings {
				t.Fatalf("savings %.1f%% < min %.1f%% (original=%d delivered=%d)", s, tc.minSavings, original, delivered)
			}
		})
	}
}

func TestTokenSavings_ComplexMixedWorkflow(t *testing.T) {
	tr := session.NewTracker(1000)
	ledger := session.NewLedger("complex-workflow")

	files := 24
	funcsPerFile := 20
	originalTotal := 0
	deliveredTotal := 0

	// Turn 1: initial read of all files.
	for i := 0; i < files; i++ {
		path := fmt.Sprintf("pkg/file_%02d.go", i)
		content, syms := syntheticFile(path, funcsPerFile, -1)
		r := CompressFileRead(path, content, Options{
			Task:            "complex savings workflow turn1",
			Symbols:         syms,
			Session:         tr,
			Ledger:          ledger,
			TokenLedgerName: "prism_read",
			Confidence:      session.High,
		})
		originalTotal += r.OriginalTokens
		deliveredTotal += r.DeliveredTokens
	}

	// Turn 2: 1/3 unchanged (sha-pointer), 1/3 changed (semantic-delta),
	// 1/3 unchanged but medium confidence (signature path on 3rd read later).
	for i := 0; i < files; i++ {
		path := fmt.Sprintf("pkg/file_%02d.go", i)
		base, baseSyms := syntheticFile(path, funcsPerFile, -1)
		changed, changedSyms := syntheticFile(path, funcsPerFile, 5)

		content := base
		syms := baseSyms
		if i%3 == 1 {
			content = changed
			syms = changedSyms
		}
		r := CompressFileRead(path, content, Options{
			Task:            "complex savings workflow turn2",
			Symbols:         syms,
			Session:         tr,
			Ledger:          ledger,
			TokenLedgerName: "prism_read",
			Confidence:      session.High,
		})
		originalTotal += r.OriginalTokens
		deliveredTotal += r.DeliveredTokens
	}

	// Turn 3: medium confidence across unchanged files to force signature refresh
	// on files that were unchanged in turn 2.
	for i := 0; i < files; i++ {
		path := fmt.Sprintf("pkg/file_%02d.go", i)
		base, baseSyms := syntheticFile(path, funcsPerFile, -1)
		r := CompressFileRead(path, base, Options{
			Task:            "complex savings workflow turn3",
			Symbols:         baseSyms,
			Session:         tr,
			Ledger:          ledger,
			TokenLedgerName: "prism_read",
			Confidence:      session.Medium,
		})
		originalTotal += r.OriginalTokens
		deliveredTotal += r.DeliveredTokens
	}

	s := percentSavings(originalTotal, deliveredTotal)
	if s < 50 {
		t.Fatalf("complex mixed workflow savings %.1f%% < 50%% (original=%d delivered=%d)", s, originalTotal, deliveredTotal)
	}
}
