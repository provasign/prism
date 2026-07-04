
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle. Its value is surfacing callers, callees, and test
contracts the agent would not find by grep+read alone.

### When MCP tools are available

Use the registered prism_* MCP tools:

| Situation | Tool |
|---|---|
| Renaming/changing a method signature | prism_change_impact(query="Type.method(ParamType, ...)") — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | prism_change_impact — override family + all callers |
| Renaming a class, struct, or type | prism_change_impact for each public method — all usages |
| Deprecating a symbol (need all callers to migrate) | prism_change_impact — complete caller list |
| ANY task that says "find all X" for a specific method | prism_change_impact first, before any grep |
| Locate a string, symbol, or file | shell tools (grep, find, rg, etc.) — not Prism |
| Callers/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") |
| Find docs about a topic | prism_query(task=..., include=["docs"]) — filenames only |
| Blast radius of a change | prism_query(terms=[...], graph_depth=3) |
| Symbols with no tests (before writing/fixing) | prism_query(terms=[...], include=["graph","coverage_gaps"]) |

**Pre-task rule:** before writing any code on a task that involves changing or
renaming an existing symbol, call prism_change_impact FIRST — even if the change
looks small. Small changes can have large blast radii through inheritance and
indirect callers that grep will not find.

Canonical workflow (non-refactor tasks):

    grep/find/rg <terms>                 <- locate anchor first; shell tools always win here
      -> prism_query(                    <- expand from anchor: callers, callees, tests
           terms=["same-grep-terms"],
           include=["graph","tests"],
           graph_depth=2
         )
      then selectively:
      -> prism_read(file=...)            <- whole file, session-compressed
      -> prism_lookup(name=...)          <- one function body (~5x cheaper than prism_read)

### When only Bash is available (subagents, CI)

Use the prism CLI with --format text instead of MCP tools:

| Situation | Command |
|---|---|
| Renaming/changing a method signature | `prism change-impact 'Type.method(ParamType, ...)'` — declaration + overrides + callers |
| Adding/changing a method on an interface or base class | `prism change-impact 'Type.method'` — override family + callers |
| Renaming a class, struct, or type | `prism change-impact 'Type.method'` for each public method |
| Deprecating a symbol (need all callers to migrate) | `prism change-impact 'Type.method'` — complete caller list |
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/tests for a symbol just found | `prism query "<task>" --terms a,b --include graph,tests --format text` |
| Read a whole file | `prism read <file> --format text` |
| Read one function body | `prism lookup <pkg.FuncName> --format text` |
| Blast radius of a change | `prism query "<task>" --terms a,b --depth 3 --format text` |
| Symbols with no tests | `prism query "<task>" --terms a,b --include graph,coverage_gaps --format text` |

### Do NOT

- Do NOT call prism_query (or prism query) before searching — use shell tools first; prism expands from the anchor
- Do NOT manually chain references + edges + lookup to enumerate a signature change's impact — prism_change_impact / prism change-impact computes the complete set in one call
- Do NOT use prism_search / prism search as a search replacement — it searches symbol names only, not source text
- Do NOT use prism_read / prism read for a single function — use prism_lookup / prism lookup instead
- Do NOT re-run prism_index / prism index on every step — delta indexing is automatic
- Do NOT manually cross-reference coverage_gaps output — treat it as authoritative and use it as the terminal step, not the start of a manual verification chain
- For coverage audits, use 1-2 terms per query and union the results — each query audits only its seeds + blast radius
