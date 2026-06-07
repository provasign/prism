
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle. Its value is surfacing callers, callees, and test
contracts the agent would not find by grep+read alone.

### Decision tree

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | grep — not Prism |
| Callers/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") |
| Find docs about a topic | prism_query(task=..., include=["docs"]) — filenames only |
| Blast radius of a change | prism_query(terms=[...], graph_depth=3) |
| Symbols with no tests (before writing/fixing) | prism_query(terms=[...], include=["graph","coverage_gaps"]) |

### Canonical workflow

    grep <terms>                         <- locate anchor first; grep always wins here
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph","tests"],
           graph_depth=2
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

### prism_query parameters

| Parameter | Default | Purpose |
|---|---|---|
| task | required | What you are doing |
| terms | — | Grep terms — same precision as grep, plus graph expansion |
| include | ["graph","tests"] | "graph" (callers/callees), "tests", "docs" (filenames only), "coverage_gaps" (untested symbols — use when writing/fixing code) |
| graph_depth | 2 | BFS hops: 1 = immediate callers, 3+ = blast radius |
| budget | 8000 | Token ceiling. Increase for large refactors. |

### Other tools

| Tool | When |
|---|---|
| prism_index | Once at session start — not on every step |
| prism_compact | When context window is near capacity |
| prism_search | Find a symbol by name when file is unknown (not a grep replacement) |
| prism_evidence | Sub-agent to parent: typed citations instead of prose |

### Do NOT

- Do NOT call prism_query before grep — grep finds the anchor, prism expands from it
- Do NOT use prism_search as a grep replacement — it searches symbol names only, not source text
- Do NOT use prism_read for a single function — use prism_lookup instead
- Do NOT re-run prism_index on every step — delta indexing is automatic
