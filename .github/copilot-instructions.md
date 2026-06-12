
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle. Its value is surfacing callers, callees, and test
contracts the agent would not find by grep+read alone.

### When MCP tools are available

Use the registered prism_* MCP tools:

| Situation | Tool |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg, etc.) — not Prism |
| Callers/tests for a symbol just found | prism_query(terms=[...], include=["graph","tests"]) |
| Read a whole file | prism_read — SHA-pointer (~10 tokens) on repeat reads |
| Read one function body | prism_lookup(name="pkg.FuncName") |
| Find docs about a topic | prism_query(task=..., include=["docs"]) — filenames only |
| Blast radius of a change | prism_query(terms=[...], graph_depth=3) |
| Symbols with no tests (before writing/fixing) | prism_query(terms=[...], include=["graph","coverage_gaps"]) |

Canonical workflow:

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
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/tests for a symbol just found | `prism query "<task>" --terms a,b --include graph,tests --format text` |
| Read a whole file | `prism read <file> --format text` |
| Read one function body | `prism lookup <pkg.FuncName> --format text` |
| Blast radius of a change | `prism query "<task>" --terms a,b --depth 3 --format text` |
| Symbols with no tests | `prism query "<task>" --terms a,b --include graph,coverage_gaps --format text` |

### Do NOT

- Do NOT call prism_query (or prism query) before searching — use shell tools first; prism expands from the anchor
- Do NOT use prism_search / prism search as a search replacement — it searches symbol names only, not source text
- Do NOT use prism_read / prism read for a single function — use prism_lookup / prism lookup instead
- Do NOT re-run prism_index / prism index on every step — delta indexing is automatic
- Do NOT manually cross-reference coverage_gaps output — treat it as authoritative and use it as the terminal step, not the start of a manual verification chain
- For coverage audits, use 1-2 terms per query and union the results — each query audits only its seeds + blast radius

<!-- shale-start -->
## Shale — agent evidence (do not remove)

Before editing any file, call:
  shale intent "<one line: what you are about to do>" [--body "<why, constraints, approach>"]

After completing the task (before asking for review), call:
  shale done [--note "<brief: what you did, any deviations>"] \
             [--tokens-in <n>] [--tokens-out <n>] \
             [--model <model-id>] [--iterations <n>]

Everything else (file tracking, command recording) is automatic.

If shale is not on your PATH, do not try to install it yourself. Tell the
user: "Shale CLI is not installed. Install it with:
  brew tap provasign/shale
  brew install shale
or download the latest release from https://github.com/provasign/shale/releases/latest
and put the shale binary on your PATH." Then continue the task without it.
<!-- shale-end -->
