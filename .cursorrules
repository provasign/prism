
## Provasign — certified merge gate (ALWAYS use these tools)

This project uses [Provasign](https://github.com/provasign/provasign) for
certified code admission. Provasign MCP tools are registered. Follow this
workflow on EVERY coding task:

### Pre-Flight Autopilot

1. **Open an intent BEFORE making code changes** — call provasign_intent_open with
   {title: <short summary>, description: <verbatim user request>}.
   Save the returned intent_id.

2. **Before asking the user to review** — call provasign_check with the unified
   diff plus {intent: <intent_id>, brief: <one-liner>}.

3. **If Allowed=false** — for each policy entry with Verdict != "allow":
   - Call provasign_explain {gate, rule} for the recommended fix.
   - Apply the fix, re-diff, and re-call provasign_check.
   - Loop up to 3 times; on the 3rd failure surface the verdict to the user.

4. **Only call provasign_submit when provasign_check returns Allowed=true** on the
   EXACT same diff. Never call provasign_submit speculatively.

5. **Close the intent when done** — call provasign_intent_close {intent_id}.
   Pass the returned trailer_block to provasign_submit so the commit is linked
   to the intent YAML.

### Tool quick-reference

| Tool                      | When                                          |
|---------------------------|-----------------------------------------------|
| provasign_intent_open     | First — capture the user request as an intent |
| provasign_check           | Before every review request                   |
| provasign_explain         | On any Verdict != allow                       |
| provasign_submit          | Only after provasign_check Allowed=true       |
| provasign_policy          | Discover which gates are active               |
| provasign_intent_close    | When the task is complete                     |


## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle. Its value is surfacing callers, callees, and test
contracts the agent would not find by grep+read alone.

### Decision tree

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | **grep** — not Prism |
| Callers/tests for a symbol just found | `prism_query(terms=[...], include=["graph","tests"])` |
| Read a whole file | `prism_read` — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | `prism_lookup(name="pkg.FuncName")` |
| Find docs about a topic | `prism_query(task=..., include=["docs"])` — filenames only |
| Blast radius of a change | `prism_query(terms=[...], graph_depth=3)` |

### Canonical workflow

```
grep <terms>                         ← locate anchor first — grep always wins here
  └─▶ prism_query(                   ← expand from anchor: callers, callees, tests
        terms=["same-grep-terms"],
        include=["graph","tests"],
        graph_depth=2
      )
  then selectively:
  └─▶ prism_read(file=...)           ← whole file, session-compressed
  └─▶ prism_lookup(name=...)         ← one function body (~5× cheaper than prism_read)
```

### prism_query parameters

| Parameter | Default | Purpose |
|---|---|---|
| `task` | required | What you are doing |
| `terms` | — | Grep terms — same precision as grep, plus graph expansion |
| `include` | `["graph","tests"]` | `"graph"` (callers/callees), `"tests"`, or `"docs"` (filenames only) |
| `graph_depth` | 2 | BFS hops: 1 = immediate callers, 3+ = blast radius |
| `budget` | 8000 | Token ceiling. Increase for large refactors. |

### Other tools

| Tool | When |
|---|---|
| `prism_index` | Once at session start — not on every step |
| `prism_compact` | When context window is near capacity |
| `prism_search` | Find a symbol by name when file is unknown (not a grep replacement) |
| `prism_evidence` | Sub-agent → parent: typed citations instead of prose |

### Do NOT

- Do NOT call `prism_query` before grep — grep finds the anchor, prism expands from it
- Do NOT use `prism_search` as a grep replacement — it searches symbol names only, not source text
- Do NOT use `prism_read` for a single function — use `prism_lookup` instead
- Do NOT re-run `prism_index` on every step — delta indexing is automatic

