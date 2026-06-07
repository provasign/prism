
## Prism — context delivery (ALWAYS use these tools)

Prism is a call-graph oracle available as a CLI. Use it via Bash to get
callers, callees, and test contracts the agent would not find by grep+read alone.

### Decision tree

| Situation | Command |
|---|---|
| Locate a string, symbol, or file | shell tools (grep, find, rg) — not Prism |
| Callers/tests for a symbol just found | `prism query "<task>" --terms a,b --include graph,tests --format text` |
| Read a whole file | `prism read <file> --format text` |
| Read one function body | `prism lookup <pkg.FuncName> --format text` |
| Find docs about a topic | `prism query "<task>" --include docs --format text` |
| Blast radius of a change | `prism query "<task>" --terms a,b --depth 3 --format text` |
| Symbols with no tests (before writing/fixing) | `prism query "<task>" --terms a,b --include graph,coverage_gaps --format text` |

### Canonical workflow

    grep/find/rg <terms>                      <- locate anchor first; shell tools always win here
      -> prism query "<task>" \               <- expand from anchor: callers, callees, tests
           --terms <same-terms> \
           --include graph,tests \
           --format text
      then selectively:
      -> prism read <file> --format text      <- whole file, session-compressed
      -> prism lookup <pkg.FuncName> --format text  <- one function (~5x cheaper than read)

### prism query flags

| Flag | Default | Purpose |
|---|---|---|
| --terms a,b | — | Anchor on specific symbol names (grep-precision + graph expansion) |
| --include a,b | graph,tests | graph (callers/callees), tests, docs (filenames only), coverage_gaps |
| --depth N | 2 | BFS hops: 1 = immediate callers, 3+ = blast radius |
| --format | text | text (default), lean (compact JSON), json (full metadata) |

### Other commands

| Command | When |
|---|---|
| prism index [dir] | Once at session start — not on every step |
| prism search <keyword> --format text | Find a symbol by name when file is unknown |

### Do NOT

- Do NOT call prism query before searching — use shell tools (grep, find, rg) to find the anchor first; prism expands from it
- Do NOT use prism search as a search replacement — it searches symbol names only, not source text
- Do NOT use prism read for a single function — use prism lookup instead
- Do NOT re-run prism index on every step — delta indexing is automatic
- Do NOT manually cross-reference coverage_gaps output — treat it as authoritative and use it as the terminal step, not the start of a manual verification chain
