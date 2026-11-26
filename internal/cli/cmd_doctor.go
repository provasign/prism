package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/trust"
	"github.com/provasign/prism/internal/version"
)

// printDoctor prints doctor's answer: JSON with --format json, else a
// short text summary; the per-language manifest stays in JSON (CON-01).
func printDoctor(format outputFormat, m map[string]any) {
	if format == formatJSON {
		printJSON(m)
		return
	}
	langs := m["languages"]
	delete(m, "languages")
	text := genericTextOf(m)
	fmt.Print(text)
	if langs != nil {
		fmt.Println("languages: per-language analysis tiers and caveats with --format json")
	}
}

// genericTextOf renders any value (struct, map) as generic text.
func genericTextOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v) + "\n"
	}
	var x any
	_ = json.Unmarshal(b, &x)
	return core.GenericText(x)
}

func cmdDoctor(args []string) int {
	config.QuietProblems = true // listed under warnings instead
	args, format, rc := takeFormat("doctor", args)
	if rc != 0 {
		return rc
	}
	args, checkIntegrity := takeBoolFlag(args, "integrity")
	dir, _, rc := dirOnlyArgs("doctor", args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	// The integrity check runs before the engine opens the index, so a
	// corrupt file is reported rather than failing the open.
	var integrity map[string]any
	var integrityWarnings []string
	if checkIntegrity {
		integrity, integrityWarnings = integrityResult(root)
	}
	_, client, err := newClient(root)
	if err != nil {
		m := map[string]any{
			"status":  "error",
			"version": version.Version,
			"root":    root,
			"error":   err.Error(),
		}
		if integrity != nil {
			m["integrity"] = integrity
			m["warnings"] = integrityWarnings
		}
		printDoctor(format, m)
		return 1
	}
	defer client.Shutdown()

	graph, err := client.Status(context.Background())
	if err != nil {
		printDoctor(format, map[string]any{
			"status":  "error",
			"version": version.Version,
			"root":    root,
			"engine":  "grove",
			"error":   err.Error(),
		})
		return 1
	}

	state := "ok"
	warnings := []string{}
	if graph.FilesIndexed == 0 {
		state = "warning"
		warnings = append(warnings, "repository is not indexed; run prism index")
	}
	for _, r := range graph.Readiness {
		state = "warning"
		warnings = append(warnings, r.Language+": compiler-backed analysis missing ("+r.Problem+"); results are name-based. Fix: "+r.Fix)
	}
	untracked := largeUntrackedWarnings(root)
	if len(integrityWarnings)+len(untracked) > 0 {
		state = "warning"
	}
	warnings = append(warnings, integrityWarnings...)
	warnings = append(warnings, sharedConfigWarnings(root)...)
	warnings = append(warnings, untracked...)
	if ng := noGraphSummary(root); ng != "" {
		warnings = append(warnings, ng)
	}
	if c, err := config.LoadFromDir(root); err == nil {
		for _, p := range c.Problems {
			warnings = append(warnings, "prism.yaml: "+p)
		}
	}
	trustState := trust.For(root).String()
	if strings.HasPrefix(trustState, "untrusted") {
		warnings = append(warnings, "repository is "+trustState+": its build tools do not run, so Java (Maven/Gradle), TypeScript and Rust (rust-analyzer) analysis is name-based; `prism trust` enables it")
	}
	groveLinked := "unknown"
	if g, ok := version.LinkedGrove(); ok {
		groveLinked = g.String()
		if g.Replaced {
			warnings = append(warnings, "grove engine is "+g.String()+", not a released version; results may differ from a release build")
		}
	}
	report := map[string]any{
		"status":   state,
		"version":  version.Version,
		"grove":    groveLinked,
		"trust":    trustState,
		"root":     root,
		"engine":   "grove",
		"index":    graph,
		"warnings": warnings,
		"capabilities": map[string]any{
			"changeImpact":       true,
			"testSelection":      true,
			"sessionDelivery":    true,
			"deliveryCacheScope": "process",
			"qualityContract":    "operation-reported",
			// Which engine backs the merged full-text search (prism_query /
			// prism_search): rg > grep > the built-in scanner. "native" means
			// no external searcher was found — correct everywhere, slower on
			// large repos; install ripgrep to upgrade it.
			"textSearch": textsearch.Backend(),
		},
		// grove.Capabilities() is Grove's own release-level per-language and
		// per-operation quality manifest (indexing/resolution tier,
		// limitations, caveats) -- `grove doctor` reports it directly; a user
		// asking "does prism support X" had no answer from `prism doctor`
		// before this, only from a separate `grove doctor` run.
		"languages": grove.Capabilities(),
	}
	if integrity != nil {
		report["integrity"] = integrity
	}
	printDoctor(format, report)
	return 0
}
