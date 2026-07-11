package assist

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Invoker runs one prism operation and returns its result map — satisfied by
// (*mcp.Handler).Invoke, injected to avoid an import cycle and to make the
// loop testable with a fake.
type Invoker func(tool string, args map[string]any) (any, error)


// asSlice normalizes a result group to []any: h.Invoke returns typed Go
// slices ([]map[string]any, []grove.RenameEdit rendered via JSON round-trip
// upstream), not []any — a bare type assertion silently missed every group.
func asSlice(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	case []map[string]any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = t[i]
		}
		return out
	default:
		// JSON round-trip for any other typed slice (e.g. []grove.RenameEdit).
		b, err := json.Marshal(v)
		if err != nil || len(b) == 0 || b[0] != '[' {
			return nil
		}
		var out []any
		if json.Unmarshal(b, &out) != nil {
			return nil
		}
		return out
	}
}

// Options configures one assist session.
type Options struct {
	Model    string // provider spec, e.g. "ollama:qwen2.5-coder:14b", "claude:claude-haiku-4-5-20251001"
	Apply    bool   // apply rename_plan confirmed edits to the working tree
	ApplyAmbiguous bool // ALSO apply Ambiguous edits (verify is the safety net)
	Verify   string // shell command to run after apply (build/tests); "" = skip
	Root     string // project root (edits are applied relative to it)
	MaxTurns int
	Out      *os.File // rendering destination (default os.Stdout)
}

// systemPrompt is the ENTIRE model-facing contract. There are no steering
// files: the harness owns tool exposure, result rendering, and edit
// application, so the model's job is reduced to the two things every measured
// tier — down to a local 14B — does reliably: route and summarize.
const systemPrompt = `You route a natural-language code task to deterministic code-graph operations.

Rules:
- If the target symbol is ambiguous or not yet known, call search_symbols first.
- Pick the operation by task shape:
  * change_impact     — a method/interface signature changes, a deprecation, or "find every site that must change / every caller"
  * rename_plan       — a rename where the user wants the concrete edits
  * missing_implementations — "who fails to implement X / who breaks if X becomes required"
  * untested_surface  — "what should I test before changing X / which sites lack tests"
  * dead_code         — unused/unreachable code cleanup
- For a multi-method interface change, call change_impact once per method.
- Symbols are written Type.method (parameter types optional).
- The harness prints every operation's full result itself. NEVER enumerate
  result sites in your own words — you only receive counts and flags.
- When the task is answered, call submit with a 2-3 sentence summary that
  mentions the totals and any completeness warning. Do not list sites.`

func toolDefs() []ToolDef {
	sym := map[string]any{"type": "object",
		"properties": map[string]any{"symbol": map[string]any{
			"type": "string", "description": "Type.method, e.g. JsonSerializer.serialize"}},
		"required": []string{"symbol"}}
	return []ToolDef{
		{Name: "search_symbols", Description: "Find symbols by name fragment to disambiguate the target.",
			Parameters: map[string]any{"type": "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []string{"query"}}},
		{Name: "change_impact", Description: "Complete change-set for a method signature change: declaration, override family, callers, declaring types.",
			Parameters: sym},
		{Name: "rename_plan", Description: "Rename a method: every concrete edit line, review-and-apply.",
			Parameters: map[string]any{"type": "object",
				"properties": map[string]any{
					"symbol":  map[string]any{"type": "string"},
					"newName": map[string]any{"type": "string"}},
				"required": []string{"symbol", "newName"}}},
		{Name: "missing_implementations", Description: "Every type in the contract's closure lacking an implementation.",
			Parameters: sym},
		{Name: "untested_surface", Description: "The change-set split into test-covered and untested sites.",
			Parameters: sym},
		{Name: "dead_code", Description: "Unreachable production symbols, safe-to-delete list with caveats.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "submit", Description: "Finish: a 2-3 sentence summary (totals + warnings; never site lists).",
			Parameters: map[string]any{"type": "object",
				"properties": map[string]any{"summary": map[string]any{"type": "string"}},
				"required":   []string{"summary"}}},
	}
}

// Run executes one assist session. Returns the model's final summary.
func Run(task string, provider Provider, invoke Invoker, opts Options) (string, error) {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if opts.MaxTurns == 0 {
		opts.MaxTurns = 10
	}
	tools := toolDefs()
	msgs := []Msg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}
	shale := newShaleTrail(opts.Root, task)
	defer shale.done()

	var lastRenamePlan map[string]any
	var summary string

	for turn := 0; turn < opts.MaxTurns; turn++ {
		// Invocation wall on the first turn only: small local models emit
		// tool calls unreliably without it; capable APIs are unaffected.
		reply, err := provider.Chat(msgs, tools, turn == 0)
		if err != nil {
			return "", fmt.Errorf("%s: %w", provider.Name(), err)
		}
		if len(reply.Calls) == 0 {
			// Text-only reply: accept as an implicit summary if we already
			// produced results, else nudge once.
			if summary == "" && strings.TrimSpace(reply.Content) != "" && turn > 0 {
				summary = strings.TrimSpace(reply.Content)
				break
			}
			msgs = append(msgs, reply, Msg{Role: "user",
				Content: "Call one of the tools (or submit) — do not answer in prose."})
			continue
		}
		msgs = append(msgs, reply)

		finished := false
		for _, call := range reply.Calls {
			if call.Name == "submit" {
				summary, _ = call.Args["summary"].(string)
				finished = true
				break
			}
			meta, full, err := runOp(call, invoke)
			if err != nil {
				msgs = append(msgs, Msg{Role: "tool", CallID: call.ID, Name: call.Name,
					Content: "error: " + err.Error()})
				continue
			}
			if call.Name == "rename_plan" {
				lastRenamePlan = full
			}
			// Render the FULL deterministic result to the user...
			render(out, call, full)
			shale.note(call, meta)
			// ...and give the MODEL only compact metadata. Relay fidelity is
			// structural: the payload never enters the model's context, so it
			// cannot be dropped, re-filtered, or re-derived — and a 310-site
			// result costs the model a dozen tokens instead of thousands.
			msgs = append(msgs, Msg{Role: "tool", CallID: call.ID, Name: call.Name, Content: meta})
		}
		if finished {
			break
		}
	}

	if summary != "" {
		fmt.Fprintf(out, "\n%s\n", summary)
	}

	if opts.Apply && lastRenamePlan != nil {
		if err := applyRenamePlan(out, opts.Root, lastRenamePlan, opts.ApplyAmbiguous); err != nil {
			return summary, err
		}
		if opts.Verify != "" {
			fmt.Fprintf(out, "\nverify: %s\n", opts.Verify)
			cmd := exec.Command("sh", "-c", opts.Verify)
			cmd.Dir = opts.Root
			vOut, vErr := cmd.CombinedOutput()
			if vErr != nil {
				fmt.Fprintf(out, "VERIFY FAILED:\n%s\n", truncate(string(vOut), 2000))
				shale.noteText("verify FAILED: " + opts.Verify)
				return summary, fmt.Errorf("verification failed: %w", vErr)
			}
			fmt.Fprintf(out, "VERIFY PASSED\n")
			shale.noteText("verify passed: " + opts.Verify)
		}
	}
	return summary, nil
}

// runOp invokes the prism operation behind a model tool call and returns
// (compact metadata for the model, full result for rendering).
func runOp(call ToolCall, invoke Invoker) (string, map[string]any, error) {
	var tool string
	args := map[string]any{}
	switch call.Name {
	case "search_symbols":
		tool = "prism_search"
		args["query"], _ = call.Args["query"].(string)
		args["limit"] = 10
	case "change_impact":
		tool = "prism_change_impact"
		args["query"], _ = call.Args["symbol"].(string)
	case "rename_plan":
		tool = "prism_rename_plan"
		args["query"], _ = call.Args["symbol"].(string)
		args["newName"], _ = call.Args["newName"].(string)
	case "missing_implementations":
		tool = "prism_missing_implementations"
		args["query"], _ = call.Args["symbol"].(string)
	case "untested_surface":
		tool = "prism_untested_surface"
		args["query"], _ = call.Args["symbol"].(string)
	case "dead_code":
		tool = "prism_dead_code"
	default:
		return "", nil, fmt.Errorf("unknown tool %q", call.Name)
	}
	res, err := invoke(tool, args)
	if err != nil {
		return "", nil, err
	}
	full, ok := res.(map[string]any)
	if !ok {
		b, _ := json.Marshal(res)
		full = map[string]any{}
		_ = json.Unmarshal(b, &full)
	}
	return compactMeta(call.Name, full), full, nil
}

// compactMeta is what the model sees: counts + flags, never payloads.
func compactMeta(op string, full map[string]any) string {
	m := map[string]any{}
	switch op {
	case "search_symbols":
		// The one exception: disambiguation NEEDS the names. Cap it small.
		var names []string
		if syms := asSlice(full["symbols"]); syms != nil {
			for _, s := range syms {
				if sm, ok := s.(map[string]any); ok {
					n, _ := sm["qualifiedName"].(string)
					if n == "" {
						n, _ = sm["name"].(string)
					}
					kind, _ := sm["kind"].(string)
					names = append(names, n+" ("+kind+")")
				}
				if len(names) >= 10 {
					break
				}
			}
		}
		m["matches"] = names
	default:
		for _, group := range []string{"declarations", "family", "callers",
			"declaringTypes", "supers", "edits", "ambiguous", "missing",
			"abstractMissing", "unverifiable", "untested", "covered", "dead",
			"exportedUnreferenced"} {
			if v := asSlice(full[group]); len(v) > 0 {
				m[group] = len(v)
			}
		}
		for _, scalar := range []string{"totalSites", "completeness",
			"implementedCount", "defaultProvided", "warning"} {
			if v, ok := full[scalar]; ok && v != nil {
				m[scalar] = v
			}
		}
		if v := asSlice(full["unresolved"]); len(v) > 0 {
			m["unresolved"] = v // short list; the agent must know these exist
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// render prints the full deterministic result — the harness, not the model,
// is the relay.
func render(out *os.File, call ToolCall, full map[string]any) {
	fmt.Fprintf(out, "\n── %s", call.Name)
	if s, ok := call.Args["symbol"].(string); ok {
		fmt.Fprintf(out, " %s", s)
	}
	if n, ok := call.Args["newName"].(string); ok {
		fmt.Fprintf(out, " → %s", n)
	}
	fmt.Fprintln(out, " ─────────────────────────────")

	site := func(v any) string {
		sm, _ := v.(map[string]any)
		if sm == nil {
			b, _ := json.Marshal(v)
			return string(b)
		}
		fp, _ := sm["filePath"].(string)
		name, _ := sm["qualifiedName"].(string)
		if name == "" {
			name, _ = sm["name"].(string)
		}
		line := ""
		if l, ok := sm["line"].(float64); ok {
			line = fmt.Sprintf(":%d", int(l))
		}
		return fmt.Sprintf("%s%s  %s", fp, line, name)
	}

	groups := []string{"declarations", "declaringTypes", "family", "supers",
		"callers", "missing", "abstractMissing", "unverifiable", "untested",
		"dead", "exportedUnreferenced", "symbols"}
	for _, g := range groups {
		items := asSlice(full[g])
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(out, "%s (%d):\n", g, len(items))
		lines := make([]string, 0, len(items))
		for _, it := range items {
			lines = append(lines, "  "+site(it))
		}
		sort.Strings(lines)
		for _, l := range lines {
			fmt.Fprintln(out, l)
		}
	}
	if edits := asSlice(full["edits"]); len(edits) > 0 {
		fmt.Fprintf(out, "edits (%d):\n", len(edits))
		for _, e := range edits {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			fmt.Fprintf(out, "  %s:%v\n    - %s\n    + %s\n",
				em["filePath"], em["line"],
				strings.TrimSpace(fmt.Sprint(em["before"])),
				strings.TrimSpace(fmt.Sprint(em["after"])))
		}
	}
	for _, k := range []string{"completeness", "warning", "note", "unresolvedNote", "ambiguousNote"} {
		if v, ok := full[k].(string); ok && v != "" {
			fmt.Fprintf(out, "%s: %s\n", k, v)
		}
	}
	if u := asSlice(full["unresolved"]); len(u) > 0 {
		fmt.Fprintf(out, "unresolved (%d): %v\n", len(u), u)
	}
	if cav := asSlice(full["caveats"]); len(cav) > 0 {
		for _, c := range cav {
			fmt.Fprintf(out, "caveat: %v\n", c)
		}
	}
}

// applyRenamePlan applies the CONFIRMED edits (never Ambiguous/Unresolved) to
// the working tree, verifying each line still matches `before` first.
func applyRenamePlan(out *os.File, root string, plan map[string]any, includeAmbiguous bool) error {
	edits := asSlice(plan["edits"])
	if includeAmbiguous {
		amb := asSlice(plan["ambiguous"])
		if len(amb) > 0 {
			// Ambiguous lines may contain a same-named call on a DIFFERENT
			// receiver; applying them trades the manual review for the verify
			// command as the safety net. Only sane WITH --verify.
			fmt.Fprintf(out, "\napply: including %d AMBIGUOUS edit(s) under --apply-ambiguous — verify is the safety net\n", len(amb))
			edits = append(edits, amb...)
		}
	}
	if len(edits) == 0 {
		fmt.Fprintln(out, "\napply: no confirmed edits to apply")
		return nil
	}
	byFile := map[string][]map[string]any{}
	for _, e := range edits {
		em, _ := e.(map[string]any)
		if em == nil {
			continue
		}
		fp, _ := em["filePath"].(string)
		byFile[fp] = append(byFile[fp], em)
	}
	applied, skipped := 0, 0
	for fp, es := range byFile {
		path := fp
		if root != "" && !strings.HasPrefix(fp, "/") {
			path = root + "/" + fp
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("apply: %s: %w", fp, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, em := range es {
			ln := int(em["line"].(float64)) - 1
			before, _ := em["before"].(string)
			after, _ := em["after"].(string)
			if ln < 0 || ln >= len(lines) || strings.TrimRight(lines[ln], "\r") != before {
				fmt.Fprintf(out, "apply: SKIP %s:%d (line changed since plan)\n", fp, ln+1)
				skipped++
				continue
			}
			lines[ln] = after
			applied++
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return fmt.Errorf("apply: %s: %w", fp, err)
		}
	}
	fmt.Fprintf(out, "\napply: %d edit(s) applied, %d skipped", applied, skipped)
	if amb := asSlice(plan["ambiguous"]); len(amb) > 0 && !includeAmbiguous {
		fmt.Fprintf(out, "; %d AMBIGUOUS edits NOT applied (verify receivers manually, or use --apply-ambiguous with --verify)", len(amb))
	}
	fmt.Fprintln(out)
	return nil
}

// --- shale evidence trail (best-effort, silent when shale is absent) --------

type shaleTrail struct {
	active bool
	root   string
}

func newShaleTrail(root, task string) *shaleTrail {
	if _, err := exec.LookPath("shale"); err != nil {
		return &shaleTrail{}
	}
	cmd := exec.Command("shale", "intent", task)
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		return &shaleTrail{}
	}
	return &shaleTrail{active: true, root: root}
}

func (s *shaleTrail) note(call ToolCall, meta string) {
	if !s.active {
		return
	}
	cmd := exec.Command("shale", "note", fmt.Sprintf("prism %s: %s", call.Name, truncate(meta, 300)))
	cmd.Dir = s.root
	_ = cmd.Run()
}

func (s *shaleTrail) noteText(text string) {
	if !s.active {
		return
	}
	cmd := exec.Command("shale", "note", text)
	cmd.Dir = s.root
	_ = cmd.Run()
}

func (s *shaleTrail) done() {
	if !s.active {
		return
	}
	cmd := exec.Command("shale", "done")
	cmd.Dir = s.root
	_ = cmd.Run()
}
