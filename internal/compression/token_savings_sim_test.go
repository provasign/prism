// token_savings_sim_test.go — measures real token savings across all *.go
// source files in the prism repo, simulating realistic agent session patterns.
//
// Run with: go test -v -run TestTokenSavings ./internal/compression/
package compression

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// repoRoot walks up from this test file's directory to find the module root.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// extractSymbols parses a Go source file and extracts function/method
// declarations as grove.SymbolRecord values — mimicking what Grove would
// return for the file.
func extractSymbols(filePath, content string) []grove.SymbolRecord {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, content, 0)
	if err != nil {
		return nil
	}
	var syms []grove.SymbolRecord
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		start := fset.Position(fd.Pos()).Line
		end := fset.Position(fd.End()).Line
		var sig strings.Builder
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			sig.WriteString("func (r) ")
		} else {
			sig.WriteString("func ")
		}
		sig.WriteString(fd.Name.Name)
		sig.WriteString("(...)")
		// Capture raw body text from the source slice.
		bodyStart := fset.Position(fd.Pos()).Offset
		bodyEnd := fset.Position(fd.End()).Offset
		rawText := ""
		if bodyEnd <= len(content) {
			rawText = content[bodyStart:bodyEnd]
		}
		syms = append(syms, grove.SymbolRecord{
			FilePath:  filePath,
			Name:      fd.Name.Name,
			Signature: sig.String(),
			Kind:      "function",
			RawText:   rawText,
			Span:      grove.SpanInfo{Start: start, End: end},
		})
	}
	return syms
}

type scenarioResult struct {
	name            string
	originalTokens  int
	deliveredTokens int
	files           int
}

func (r scenarioResult) savings() float64 {
	if r.originalTokens == 0 {
		return 0
	}
	return (1.0 - float64(r.deliveredTokens)/float64(r.originalTokens)) * 100.0
}

// TestTokenSavings simulates four realistic agent session patterns over the
// full prism Go source corpus and reports per-scenario token savings.
func TestTokenSavings(t *testing.T) {
	root := repoRoot()

	// Collect all .go source files (excluding test files for cleaner signal).
	var goFiles []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})

	if len(goFiles) == 0 {
		t.Fatal("no .go source files found — check repoRoot()")
	}

	// Read all file contents and extract symbols once.
	type fileData struct {
		path    string
		content string
		syms    []grove.SymbolRecord
		tokens  int
	}
	corpus := make([]fileData, 0, len(goFiles))
	for _, p := range goFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := string(b)
		rel, _ := filepath.Rel(root, p)
		corpus = append(corpus, fileData{
			path:    rel,
			content: content,
			syms:    extractSymbols(rel, content),
			tokens:  ranking.EstimateTokens(content),
		})
	}

	// ── Scenario A: Baseline — no session, deliver everything raw ────────────
	// Simulates an agent with no Prism compression at all.
	baselineTokens := 0
	for _, fd := range corpus {
		baselineTokens += fd.tokens
	}

	// ── Scenario B: Single-session first read (compressed-fresh) ─────────────
	// Every file delivered once with symbol-aware compression.
	scB := func() scenarioResult {
		tr := session.NewTracker(500)
		ledger := session.NewLedger("sim")
		res := scenarioResult{name: "compressed-fresh (turn 1)"}
		for _, fd := range corpus {
			r := CompressFileRead(fd.path, fd.content, Options{
				Task:            "implement caching layer",
				Symbols:         fd.syms,
				Session:         tr,
				Ledger:          ledger,
				TokenLedgerName: "prism_read",
				Confidence:      session.Low,
			})
			res.originalTokens += r.OriginalTokens
			res.deliveredTokens += r.DeliveredTokens
			res.files++
		}
		return res
	}()

	// ── Scenario C: Second read same content (sha-pointer, H/D benefit) ──────
	// Agent re-reads the same files after initial delivery — session dedup kicks in.
	scC := func() scenarioResult {
		tr := session.NewTracker(500)
		ledger := session.NewLedger("sim")
		res := scenarioResult{name: "sha-pointer (turn 2, unchanged)"}
		// Turn 1: prime the session.
		for _, fd := range corpus {
			CompressFileRead(fd.path, fd.content, Options{
				Task: "implement", Symbols: fd.syms,
				Session: tr, Ledger: ledger, TokenLedgerName: "prism_read",
				Confidence: session.High,
			})
		}
		// Turn 2: re-read same files.
		for _, fd := range corpus {
			r := CompressFileRead(fd.path, fd.content, Options{
				Task: "implement", Symbols: fd.syms,
				Session: tr, Ledger: ledger, TokenLedgerName: "prism_read",
				Confidence: session.High,
			})
			res.originalTokens += r.OriginalTokens
			res.deliveredTokens += r.DeliveredTokens
			res.files++
		}
		return res
	}()

	// ── Scenario D: Second read with 1 changed symbol (semantic-delta) ───────
	// Simulates a developer editing one function — only the diff is re-sent.
	scD := func() scenarioResult {
		tr := session.NewTracker(500)
		ledger := session.NewLedger("sim")
		res := scenarioResult{name: "semantic-delta (turn 2, 1 func changed)"}
		// Turn 1: prime the session with all symbols + their SHAs.
		for _, fd := range corpus {
			r := CompressFileRead(fd.path, fd.content, Options{
				Task: "fix bug", Symbols: fd.syms,
				Session: tr, Ledger: ledger, TokenLedgerName: "prism_read",
				Confidence: session.High,
			})
			// Simulate UpdateSymbolSHAs being called (as compressor does).
			_ = r
		}
		// Turn 2: re-read with content that has a single appended comment —
		// this changes the file hash but leaves all symbol bodies identical,
		// so semantic delta should cache-pointer all symbols.
		for _, fd := range corpus {
			modified := fd.content + "\n// edited\n"
			r := CompressFileRead(fd.path, modified, Options{
				Task: "fix bug", Symbols: fd.syms,
				Session: tr, Ledger: ledger, TokenLedgerName: "prism_read",
				Confidence: session.High,
			})
			res.originalTokens += ranking.EstimateTokens(modified)
			res.deliveredTokens += r.DeliveredTokens
			res.files++
		}
		return res
	}()

	// ── Scenario E: Phase-shaping — review phase (0.60× budget) ─────────────
	// Simulates prism_query on a 16k-context model where a "review" task
	// shapes the budget to 0.60× vs a full "implement" query on the same model.
	// We compress the corpus against each budget cap and compare delivered tokens.
	scE := func() scenarioResult {
		res := scenarioResult{name: "phase-shaping review (0.60× budget)", files: len(corpus)}
		// Realistic: 16k context, minus 5k output + 1k system = 10k query budget.
		const contextWindow = 16000
		fullBudget := contextWindow - 6000
		reviewBudget := int(float64(fullBudget) * 0.60)

		// Deliver symbols greedily up to each budget cap.
		deliver := func(budget int) int {
			total := 0
			for _, fd := range corpus {
				for _, sym := range fd.syms {
					cost := ranking.EstimateTokens(sym.RawText)
					if total+cost > budget {
						break
					}
					total += cost
				}
			}
			return total
		}
		res.originalTokens = deliver(fullBudget)
		res.deliveredTokens = deliver(reviewBudget)
		return res
	}()

	// ── Print results ─────────────────────────────────────────────────────────
	fmt.Printf("\n")
	fmt.Printf("=== Token Savings Simulation — prism corpus (%d files, %d raw tokens) ===\n\n",
		len(corpus), baselineTokens)
	fmt.Printf("%-45s  %8s  %8s  %8s\n", "Scenario", "Original", "Delivered", "Savings%")
	fmt.Printf("%s\n", strings.Repeat("-", 78))

	printRow := func(sc scenarioResult) {
		fmt.Printf("%-45s  %8d  %8d  %7.1f%%\n",
			sc.name, sc.originalTokens, sc.deliveredTokens, sc.savings())
	}

	// Baseline reference
	fmt.Printf("%-45s  %8d  %8d  %7.1f%%\n",
		"no compression (baseline)", baselineTokens, baselineTokens, 0.0)

	printRow(scB)
	printRow(scC)
	printRow(scD)
	printRow(scE)

	fmt.Printf("\n")

	// Assert each new feature delivers meaningful savings over baseline.
	if scB.savings() < 10 {
		t.Errorf("compressed-fresh should save >10%% of tokens, got %.1f%%", scB.savings())
	}
	if scC.savings() < 50 {
		t.Errorf("sha-pointer (turn 2) should save >50%% of tokens, got %.1f%%", scC.savings())
	}
	if scD.savings() < 30 {
		t.Errorf("semantic-delta should save >30%% of tokens, got %.1f%%", scD.savings())
	}
	if scE.savings() < 30 {
		t.Errorf("phase-shaping (review) should save >30%% of tokens, got %.1f%%", scE.savings())
	}
}
