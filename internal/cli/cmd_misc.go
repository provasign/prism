package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/assist"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
)

func cmdCompact(args []string) int {
	dir, _, rc := dirOnlyArgs("compact", args)
	if rc != 0 {
		return rc
	}
	var turns []map[string]any
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&turns); err != nil {
		fmt.Fprintln(os.Stderr, "compact: stdin must be a JSON array of turns:", err)
		return 2
	}
	out, err := invokeTool(dir, core.CompactRequest{Turns: turns})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compact:", err)
		return 1
	}
	printJSON(out)
	return 0
}

func cmdFeedback(args []string) int {
	tool := ""
	queryID := ""
	notes := ""
	rating := -1
	list := false
	dir, dirSet := ".", false

	it := newArgIter("feedback", args)
	for it.next() {
		switch it.Flag {
		case "--tool":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			tool = v
		case "--query-id":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			queryID = v
		case "--rating":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil || n < 0 || n > 5 {
				return usageError(fmt.Errorf("feedback: --rating wants an integer 0-5, got %q", v))
			}
			rating = n
		case "--notes":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			notes = v
		case "--list":
			list = true
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("feedback", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}

	if list {
		root, _ := cliRoot(dir)
		data, err := os.ReadFile(core.FeedbackPath(root))
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "feedback:", err)
			return 1
		}
		fmt.Print(string(data))
		return 0
	}
	if rating < 0 || rating > 5 {
		fmt.Fprintln(os.Stderr, "usage: prism feedback --tool <name> --rating <0-5> [--notes <text>] [--query-id <id>] [dir]")
		return 2
	}
	if tool == "" {
		tool = "prism_query"
	}

	out, err := invokeTool(dir, core.FeedbackRequest{ToolName: tool, QueryID: queryID, Rating: rating, Notes: notes})
	if err != nil {
		fmt.Fprintln(os.Stderr, "feedback:", err)
		return 1
	}
	printJSON(out)
	return 0
}

// cmdDrift refuses: drift compares files with what the MCP server
// delivered in its session, and a one-shot CLI call has none, so it always
// reported checkedFiles: 0 as success (BUG-12).
func cmdDrift(args []string) int {
	fmt.Fprintln(os.Stderr, "drift: MCP only. It compares files with what a running `prism mcp` server delivered this session; a CLI call has no session. Use the prism_drift tool, or rerun the reads you depend on.")
	return 2
}

// cmdConfig prints every prism.yaml key with its resolved value and source
// (CON-08). --check exits 1 on an unknown key or an invalid value.
func cmdConfig(args []string) int {
	args, format, rc := takeFormat("config", args)
	if rc != 0 {
		return rc
	}
	check := false
	var rest []string
	for _, a := range args {
		if a == "--check" {
			check = true
			continue
		}
		rest = append(rest, a)
	}
	dir, _, rc := dirOnlyArgs("config", rest)
	if rc != 0 {
		return rc
	}
	cfg, err := config.LoadFromDir(mustAbs(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	if format == formatJSON {
		keys := []map[string]string{}
		for _, k := range cfg.Keys() {
			keys = append(keys, map[string]string{"key": k[0], "value": k[1], "source": k[2]})
		}
		problems := cfg.Problems
		if problems == nil {
			problems = []string{}
		}
		printOutput(map[string]any{"keys": keys, "problems": problems}, format)
	} else {
		for _, k := range cfg.Keys() {
			v := k[1]
			if v == "" {
				v = "-"
			}
			fmt.Printf("%-17s %-30s %s\n", k[0], v, k[2])
		}
		for _, p := range cfg.Problems {
			fmt.Println("problem:", p)
		}
	}
	if check && len(cfg.Problems) > 0 {
		return exitFinding
	}
	return 0
}

// cmdAssist runs the model-agnostic harness: a natural-language task routed by
// any chat model (local Ollama / Anthropic / OpenAI) to the deterministic task
// ops. No steering files: the harness owns tool exposure, renders every result
// itself (the model never relays payloads — relay fidelity by construction),
// and optionally applies rename edits + runs a verify command. If a `shale`
// binary is present, the session emits an evidence trail (intent/note/done).
func cmdAssist(args []string) int {
	dir := "."
	model := ""
	apply := false
	applyAmbiguous := false
	verify := ""
	it := newArgIter("assist", args)
	for it.next() {
		switch it.Flag {
		case "--model":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			model = v
		case "--apply":
			apply = true
		case "--apply-ambiguous":
			apply = true
			applyAmbiguous = true
		case "--verify":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			verify = v
		case "--dir":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			dir = v
		default:
			return it.unknown()
		}
	}
	task := strings.TrimSpace(strings.Join(it.Pos, " "))
	if task == "" {
		fmt.Fprintln(os.Stderr, `usage: prism assist [--model <spec>] [--apply] [--verify "<cmd>"] [--dir <root>] "<task>"
  model specs: ollama:<tag> | claude:<model> | openai:<model>  (default: auto-detect)`)
		return 2
	}
	if model == "" {
		detected, err := assist.DetectDefaultModel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "assist:", err)
			return 1
		}
		model = detected
	}
	provider, err := assist.NewProvider(model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}

	root := mustAbs(dir)
	cfg, client, err := newClient(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}
	defer client.Shutdown()
	if err := client.AutoIndexIfEmpty(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}

	h := core.NewHandler(cfg, root, client)

	fmt.Printf("assist: %s @ %s\n", provider.Name(), root)
	_, err = assist.Run(task, provider, h.Invoke, assist.Options{
		Model: model, Apply: apply, ApplyAmbiguous: applyAmbiguous, Verify: verify, Root: root,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "assist:", err)
		return 1
	}
	return 0
}
