package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/workspace"
)

// --- shared helpers ------------------------------------------------------

// requireDir rejects roots that do not exist as directories. Opening the
// engine creates <root>/.grove, so a mistyped CLI argument in the dir
// position (`prism edges --name routeService` put "routeService" there)
// used to CREATE and auto-index a stray directory instead of erroring.
func requireDir(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("directory does not exist: %s (a flag or symbol name in the dir position?)", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", root)
	}
	return nil
}

// newClient opens a one-shot client for a CLI command (see
// grove.Client.OneShot).
func newClient(dir string) (*config.Config, *grove.Client, error) {
	return newClientMode(dir, true)
}

// newClientMode opens a client; long-lived commands (watch) pass
// oneShot=false so the engine keeps resident workers between runs.
func newClientMode(dir string, oneShot bool) (*config.Config, *grove.Client, error) {
	return newClientOpts(dir, oneShot, false)
}

// newClientOpts is newClientMode with force: every Index call on the
// client re-extracts every file and re-runs every analyzer.
// indexAnywhere lets this process open an index in a directory with no
// project marker; prism index and prism watch set it.
var indexAnywhere bool

func newClientOpts(dir string, oneShot, force bool) (*config.Config, *grove.Client, error) {
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		return nil, nil, err
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	cfg.WarnProblems() // CON-08: a typo in prism.yaml used to be ignored silently
	// CON-11: a query in a directory that is no project (an empty folder,
	// ~/Downloads) used to create .grove there and index whatever it held.
	// Only prism index / watch build an index in such a directory.
	if !indexAnywhere && !workspace.LooksLikeProject(root) {
		return nil, nil, fmt.Errorf("no project here: %s has no .git, prism.yaml, .grove or build manifest; run `prism index %s` to index it anyway", root, root)
	}
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root))
	if oneShot {
		client.OneShot()
	}
	if force {
		client.Force()
	}
	if err := client.EnsureRunning(context.Background()); err != nil {
		if msg := err.Error(); strings.Contains(msg, "file is not a database") || strings.Contains(msg, "malformed") {
			return nil, nil, fmt.Errorf("grove: %w\n  fix: the index is damaged; run `prism index --rebuild`", err)
		}
		return nil, nil, fmt.Errorf("grove: %w", err)
	}
	return cfg, client, nil
}

func invokeTool(dir string, r core.Request) (any, error) {
	tool, args := r.Tool(), r.Args()
	timing := os.Getenv("PRISM_TIMING") != ""
	tInv := time.Now()
	stamp := func(stage string) {
		if timing {
			fmt.Fprintf(os.Stderr, "[prism-timing] cli:%-19s %8.0fms\n", stage, float64(time.Since(tInv).Milliseconds()))
		}
	}
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, err
	}
	stamp("newClient")
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		return nil, err
	}
	stamp("autoIndex")

	h := core.NewHandler(cfg, root, client)
	return h.Invoke(tool, args)
}

// invokeToolText runs a tool and renders its answer with the same renderer
// the MCP server uses (compact gateway wording), so --format text matches
// what an agent receives over MCP. rendered=false: caller prints out itself.
func invokeToolText(dir string, r core.Request) (out any, text string, rendered bool, err error) {
	tool, args := r.Tool(), r.Args()
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	cfg, client, err := newClient(root)
	if err != nil {
		return nil, "", false, err
	}
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		return nil, "", false, err
	}
	h := core.NewHandler(cfg, root, client)
	out, err = h.Invoke(tool, args)
	if err != nil {
		return nil, "", false, err
	}
	text, rendered = h.RenderToolText(true, tool, args, out)
	return out, text, rendered, nil
}

// invokeCompactReadText serves a compact read without the graph, as the MCP
// server does before its first index: a compact read is always a line window
// read from disk (safePathWithinRoot and the compact budgets still apply), so
// it neither opens the engine nor builds a missing index. A cold `prism read
// README.md` used to index the repository first, and an unusable
// .grove/grove.db failed a readable file with "file is not a database".
func invokeCompactReadText(dir string, r core.Request) (out any, text string, rendered bool, err error) {
	args := r.Args()
	root, sub := cliRoot(dir)
	rebaseArgs(args, root, sub)
	noteRoot(root, sub)
	if err := requireDir(root); err != nil {
		return nil, "", false, err
	}
	cfg, err := config.LoadFromDir(root)
	if err != nil {
		return nil, "", false, fmt.Errorf("config: %w", err)
	}
	// Never started: the read path does not call it.
	client := grove.NewClient().WithTokenFromDir(root).WithLinkedRoots(workspace.RootsOf(root)).OneShot()
	h := core.NewHandler(cfg, root, client)
	return h.InvokeCompactReadText(args)
}

// printToolResult prints the shared text rendering for --format text. Unknown
// result shapes use the same compact JSON fallback as the MCP server, so no
// fields disappear or acquire a CLI-only rendering.
func printToolResult(op string, out any, text string, rendered bool, format outputFormat) {
	if format == formatText {
		if rendered {
			text = fitShellOutput(op, cliHintSpellingLines(text))
			fmt.Print(text)
			if !strings.HasSuffix(text, "\n") {
				fmt.Println()
			}
			return
		}
		// The tool's renderer declined this answer: the same renderer over
		// the fields it knows plus the rest as key/value lines, never JSON
		// (CON-02, ENG-04).
		m, _ := out.(map[string]any) // the renderers expect the tool's own types
		if m == nil {
			m = toMap(out)
		}
		if m != nil {
			fmt.Print(core.RenderFallbackText("prism_"+strings.ReplaceAll(op, "-", "_"), m))
			return
		}
		printOutput(out, formatText)
		return
	}
	printOutput(out, format)
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return abs
}

func dirArg(args []string, idx int, def string) string {
	if idx < len(args) {
		a := args[idx]
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return def
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func printOutput(v any, format outputFormat) {
	if format == formatJSON || format == "" {
		printJSON(v)
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		printJSON(v)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		if format == formatJSON {
			printJSON(v)
			return
		}
		// Not an object (a bare list): still text, never JSON (CON-02).
		var any0 any
		_ = json.Unmarshal(b, &any0)
		fmt.Print(core.GenericText(any0))
		return
	}
	switch format {
	case formatText:
		fmt.Print(core.RenderAnswerText(m))
	default:
		printJSON(v)
	}
}

// ─── task-shaped renderers ───────────────────────────────────────────────────
//
// These render every returned site rather than truncating the view. Coverage
// remains bounded by the indexed graph and the result's completeness notes.

func anySliceCLI(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
