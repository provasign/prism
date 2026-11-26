package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/trust"
	"github.com/provasign/prism/internal/workspace"
)

func cmdIndex(args []string) int {
	indexAnywhere = true // an explicit index may build anywhere (CON-11)
	defer func() { indexAnywhere = false }()
	args, format, rc := takeFormat("index", args)
	if rc != 0 {
		return rc
	}
	dir, flags, rc := dirOnlyArgs("index", args, "--allow-heuristic", "--force", "--rebuild", "--untrusted", "--trusted")
	if rc != 0 {
		return rc
	}
	switch {
	case flags["--untrusted"] && flags["--trusted"]:
		return usageError(fmt.Errorf("index: --untrusted and --trusted are exclusive"))
	case flags["--untrusted"]:
		trust.SetOverride(trust.Untrusted)
	case flags["--trusted"]:
		trust.SetOverride(trust.Trusted)
	}
	allowHeuristic := flags["--allow-heuristic"]
	// An explicit index is the user trusting this repository's files, its
	// declared links included; they are confirmed before the engine is told
	// which repositories are linked.
	if own, err := config.LoadFromDir(mustAbs(dir)); err == nil {
		workspace.ConfirmLinks(mustAbs(dir), own)
	}
	if flags["--rebuild"] {
		// Before any engine opens the store: a broken index may not open.
		if err := requireDir(mustAbs(dir)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		removed, err := grove.RemoveIndex(context.Background(), mustAbs(dir))
		if err != nil {
			fmt.Fprintln(os.Stderr, "index --rebuild:", err)
			return 1
		}
		if len(removed) > 0 {
			fmt.Fprintf(os.Stderr, "prism: removed the index (%s); building it from scratch\n", strings.Join(removed, ", "))
		}
	}
	cfg, client, err := newClientOpts(dir, true, flags["--force"])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	workspace.Register(mustAbs(dir))
	_ = cfg
	// No default deadline: a deadline cancels the index itself, and a first
	// index of a never-built project compiles its dependencies once (grafana:
	// ~29 min cold Go build cache, ~60s after). Cancelling threw that work
	// away and the next command started over. PRISM_INDEX_TIMEOUT still sets
	// one; Ctrl+C stops the run.
	ctx, cancel := context.WithCancel(context.Background())
	if v := os.Getenv("PRISM_INDEX_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), d)
		}
	}
	defer cancel()
	if st, err := client.Status(ctx); err == nil && st != nil && st.FilesIndexed == 0 {
		fmt.Fprintln(os.Stderr, "prism: first index. Compiler-backed analysis compiles the project's dependencies once\n"+
			"       (Go build cache, javac, the TypeScript checker). If the project was never built on this\n"+
			"       machine this can take several minutes; later indexes are incremental.")
	}
	done := make(chan struct{})
	defer close(done)
	go func(start time.Time) {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				fmt.Fprintf(os.Stderr, "prism: still indexing (%s elapsed)\n", time.Since(start).Round(time.Second))
			}
		}
	}(time.Now())
	res, err := client.Index(ctx, mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "index:", err)
		return 1
	}
	printOutput(res, format)
	if len(res.Readiness) == 0 {
		return 0
	}
	// The baseline index must come from a project that compiles: without
	// the compiler pass, call and reference resolution for these languages
	// is name-based. The index is written either way; say so loudly.
	fmt.Fprintln(os.Stderr, "\nprism: compiler-backed analysis is missing for:")
	for _, r := range res.Readiness {
		fmt.Fprintf(os.Stderr, "  - %s: %s\n    fix: %s\n", r.Language, r.Problem, r.Fix)
	}
	if allowHeuristic {
		fmt.Fprintln(os.Stderr, "prism: continuing with name-based resolution (--allow-heuristic); accuracy for these languages is lower.")
		return 0
	}
	fmt.Fprintln(os.Stderr, "prism: the index was written, but results for these languages are name-based and less accurate.\n"+
		"       Fix the above and rerun `prism index`, or pass --allow-heuristic to accept name-based results.")
	return 3
}
