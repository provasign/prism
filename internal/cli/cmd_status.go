package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/trust"
	"github.com/provasign/prism/internal/workspace"
)

// cmdTrust records the user's trust decision for a repository (SEC-01).
func cmdTrust(cmd string, args []string) int {
	dir, _, rc := dirOnlyArgs(cmd, args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	state := trust.Trusted
	if cmd == "untrust" {
		state = trust.Untrusted
	}
	if err := trust.Set(root, state); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		return 1
	}
	fmt.Printf("%s: %s (recorded in %s)\n", root, trust.For(root), trust.Path())
	if state == trust.Untrusted {
		fmt.Println("Indexing will not run this repository's build tools; Java, TypeScript and Rust answers become name-based. Run `prism index --rebuild` to drop edges an earlier trusted index produced.")
	}
	return 0
}

func cmdStatus(args []string) int {
	args, format, rc := takeFormat("status", args)
	if rc != 0 {
		return rc
	}
	dir, _, rc := dirOnlyArgs("status", args)
	if rc != 0 {
		return rc
	}
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Store-only fast path: status is three COUNT(*) queries; booting the full
	// engine (newClient -> EnsureRunning) rehydrates the whole graph first —
	// ~1.3s of work on a 500k-edge index that status never reads. Same counts,
	// same output shape, ~5ms.
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root)).OneShot()
	res, err := client.QuickStatus(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	printOutput(res, format)
	if format == formatText {
		if ng := noGraphSummary(root); ng != "" {
			fmt.Println(ng) // GAP-09
		}
	}
	return 0
}
