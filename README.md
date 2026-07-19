# Prism

> **Semantic change intelligence for every coding agent.**
>
> Prism computes change impact, contract closure, test gaps, dead code, and
> edit-ready context as deterministic operations over your local code graph.

## What Prism is

Prism is an **agent-neutral semantic safety layer for code changes**. It indexes a
repository into a measured semantic graph (symbols, calls, overrides, implements,
test edges — via the embedded [Grove](https://github.com/provasign/grove)
engine) and exposes that graph at **task altitude**: one deterministic call
answers a whole question an agent would otherwise spend dozens of turns
approximating. For bug-fix and implement tasks it delivers the answer as
**edit-ready, line-numbered source** — verbatim windows plus each anchor's
callers and covering tests — so the model edits without a second read
(`prism_query`, phase-aware; `delivery="symbols"` for the compact list).

Prism does not claim uniform compiler completeness across every language or
runtime dispatch pattern. Run `prism doctor [dir]` to inspect the active engine,
index readiness, and capability mode. Authoritative operations report their own
completeness; stale, unsupported, or heuristic evidence must be treated as
degraded rather than silently promoted to certainty.

**The need.** Agents gather context with text search and file reads. That works
for locating things, but it fails exactly where the stakes are highest:
enumerating everything a change touches. Overridden methods, interface
implementations, overload-specific callers, and indirect call chains are
invisible to `grep` — and an agent that misses one site ships a broken build.
Measured across 4 languages and blast radii of 1–310 sites, text-search agents
top out at 0.62–0.75 recall on change-impact tasks even on frontier models
(see [provasign/research](https://github.com/provasign/research)).

**The principles.**

1. **Correctness and completeness first.** A faster or cheaper *incomplete*
   answer is a faster broken build. Every design choice is subordinate to
   returning the complete, type-resolved answer.
2. **Task altitude, not primitives.** The graph is exposed as whole-task
   operations (`change_impact`, `rename_plan`, `untested_surface`, …), not as
   node/edge primitives the agent must orchestrate. Orchestrating traversals
   is itself a frontier-model skill; a task-level call works on any model.
3. **Determinism.** The engine solves the traversal; the agent relays the
   result. Same query, same index, same answer — testable without an LLM, and
   never re-filtered through grep/sed (measured to drop real sites).
4. **Tier invariance.** Because the hard part is done by the engine, the same
   completeness holds from a free local 30B model to a frontier model —
   measured at recall 1.00 on both, where orchestration-based approaches
   collapse on cheap models.
5. **Each layer does what it's best at.** Shell tools find the first anchor
   (they win at string location — Prism does not replace `grep`). Prism
   answers relationship and whole-task questions. The model reasons and edits.
6. **Evidence-backed abstraction.** Above the task ops sits a component-level
   view (`prism map` / `prism cycles`): directories as components, dependency
   edges induced from the real call/import/type edges crossing between them,
   with weights, dependency cycles, and the evidence tier of every claim.
   Every abstract edge expands back to its concrete `file:line` sites — an
   architecture *proof surface*, not a narrative repo map. View results claim
   `complete-at-tier`, never `closed` (see
   `docs/DESIGN_LAYERED_INTELLIGENCE.md`).

**Use cases** — the questions Prism answers in one call:

| You are about to… | One call |
|---|---|
| Change or rename a method signature | `change-impact` — declaration + override family + every resolved caller |
| Apply a rename, not just find it | `rename-plan` — every edit line, before/after, review-and-apply |
| Make an interface method required | `missing-implementations` — every type that breaks |
| Refactor safely | `untested-surface` — the change-set split covered/untested |
| Delete or extract code | `dead-code` — unreachable production symbols |
| Commit / select CI tests | `affected` — every test covering the changed files |
| Read code cheaply | `read` / `lookup` — session-deduped, ~10-token repeat reads |
| Expand from a grep hit | `query` — callers, callees, tests around an anchor |

**Where Prism is the wrong tool** (honesty is a feature): locating a string or
file (`rg` wins), languages outside the supported set below, dispatch wired at
runtime through frameworks/reflection/DI (Prism's edges are static and
type-resolved — it will show you *nothing* rather than a guess), and one-line
greppable changes where any approach ties.

Prism is not a better `grep`. Use `rg`/`grep` to find the first anchor. Use
Prism to answer the follow-up questions that usually cost several file reads:

- What calls this?
- What does this call?
- Which tests define the contract?
- What else is in the blast radius?
- Which nearby exported functions have no direct test coverage?

The recommended agent mode is **both** (MCP tools as primary surface, CLI
fallback for subagents that don't inherit the MCP session):

```bash
prism init . --mode both
```

Agents with an active MCP session call `prism_query`, `prism_read`, and
`prism_lookup` directly. For bug-fix and implement tasks `prism_query`
delivers verbatim line-numbered source windows plus each anchor's callers and
covering tests (edit-ready, phase-aware; `--delivery symbols` forces the
compact list). Subagents and CI scripts fall back to the CLI:

```bash
prism query "fix direct coverage gaps" --terms buildCoverageGaps --include graph,tests,coverage_gaps --format text
prism read internal/mcp/tools.go --format text
prism lookup github.com/provasign/prism/internal/mcp.buildCoverageGaps --format text
```

`--format text` avoids the large JSON metadata wrappers that made early MCP
benchmarks look expensive. Agents see plain source-like context with short
headers, and can ask for `lean` or `json` only when automation needs it.

Grove is embedded in the Prism binary. There is no separate daemon, token, or
`grove_url` setup in current releases.

---

## Why Prism

Shell search gives pointers. Agents still have to chase those pointers by
reading files, guessing test names, and manually reconstructing call paths.

Prism precomputes the project graph and lets the agent ask for relationships:

```text
rg buildCoverageGaps internal/
  -> prism query "write tests for buildCoverageGaps" \
       --terms buildCoverageGaps \
       --include graph,tests,coverage_gaps \
       --format text
```

On this repository, five real maintenance scenarios were run both ways on
2026-06-07. Shell-only baselines used `rg` plus targeted `sed` reads; Prism used
one CLI text command per scenario.

| Scenario | Shell bytes | Prism CLI bytes | Context reduction |
|---|---:|---:|---:|
| Init `agent_mode` / CLI steering impact | 19,970 | 12,818 | 35.8% |
| `coverage_gaps` precision | 21,226 | 17,145 | 19.2% |
| CLI text/lean/json output formatting | 15,820 | 14,198 | 10.3% |
| Session cache / savings ledger | 33,134 | 19,922 | 39.9% |
| Release/version/install wiring | 21,246 | 12,157 | 42.8% |

The average reduction was **29.6%** with one Prism command instead of 5-6 shell
commands. The bigger correctness win is that Prism surfaces tests and coverage
gaps proactively; shell-only workflows often discover those after CI fails.

A controlled A/B re-run (2026-06-12, post Grove-v0.6.2 fixes) on the payflow
ground-truth project: zero coverage false positives at the tool level, total
agent-token parity with the shell baseline (the 2026-06-07 run had +27–147%
overhead), 47 vs 84 tool calls, and the baseline agent missing 3 of 12
designed coverage gaps that `coverage_gaps` reports mechanically. Repeat
reads cost 29 tokens (95% saved); a rename under the agent's feet is reported
as one breaking `renamed` entry for ~130 tokens. Full report:
[docs/AB-Test-Payflow-2026-06-12.md](docs/AB-Test-Payflow-2026-06-12.md).

More detail, including repeat-read savings: [provasign.dev/prism](https://provasign.dev/prism/).

---

## How It Works

```text
Task + anchor terms
      |
      v
Embedded Grove index
  - symbols
  - call edges
  - dependency edges
  - test edges
      |
      v
Prism ranking
  - graph distance
  - semantic similarity
  - recency
  - test relevance
  - edit frequency / learned weights
      |
      v
Budgeted text context
  - target symbols
  - callers/callees
  - tests
  - docs
  - coverage_gaps
```

Prism supports two distinct saving mechanisms:

1. **Context gathering reduction**: one graph-aware query replaces multiple
   shell searches and file reads. This is what CLI text-mode benchmarks measure.
2. **Session deduplication**: in persistent MCP transports, repeated reads of
   unchanged files can become a short SHA pointer. This is where the ~99%
   repeated-read savings come from.

Direct CLI invocations are process-per-command, so they should be evaluated on
context gathering and output wrapper size, not same-session re-read dedupe.

---

## Installation

```bash
# Homebrew (macOS / Linux)
brew install provasign/shale/prism

# macOS / Linux script
curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash

# Windows PowerShell
irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex

# Pin a version
VERSION=v0.7.0 curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
```

The installer writes `prism` to `~/bin` by default. Set
`INSTALL_DIR=/usr/local/bin` or another directory to override.

Build from source:

```bash
make build
make test
make install
```

---

## Quick Start: Agent CLI Text Mode

Run this once at the project root:

```bash
prism init . --mode both
prism index .
```

This writes:

- `prism.yaml` with `agent_mode: "both"`
- `.mcp.json` wiring the MCP server for MCP-capable clients
- steering files such as `AGENTS.md`, `CLAUDE.md`, `.cursorrules`,
  `.windsurfrules`, `.github/copilot-instructions.md`, and others
- compatible tool config files where detected

The generated agent instructions tell agents to use commands like:

```bash
prism query "trace the payment refund flow" --terms RefundPayment --include graph,tests --format text
prism query "find direct coverage gaps" --terms UpdatePayment,RequireScope --include graph,coverage_gaps --format text
prism read internal/payment/service.go --format text
prism lookup github.com/example/payflow/internal/payment.(*Service).RefundPayment --format text
```

Recommended agent workflow:

1. Locate the first anchor with `rg`, `grep`, or `find`.
2. Run `prism query` with the same anchor terms.
3. Use `prism read` for whole files only when needed.
4. Use `prism lookup` for one known function or method.
5. Treat `coverage_gaps` as a terminal structured output, not the start of
   manual cross-referencing.

---

## Other Modes

`prism init` supports three modes:

```bash
prism init . --mode both  # recommended: MCP primary + CLI fallback for subagents
prism init . --mode mcp   # MCP tools only: prism_query, prism_read, ...
prism init . --mode cli   # CLI only: for environments without MCP support
```

### MCP

MCP advertises fifteen tools: the context surface (`prism_query`,
`prism_read`, `prism_search`, `prism_lookup`, `prism_references`,
`prism_resolve`, `prism_edges`), the task-shaped graph operations
(`prism_change_impact`, `prism_missing_implementations`,
`prism_untested_surface`, `prism_dead_code`, `prism_rename_plan`,
`prism_affected`), and session upkeep
(`prism_index`, `prism_drift`). The auxiliary tools (`prism_savings`,
`prism_feedback`, `prism_compact`, `prism_evidence`) stay available through
the CLI and HTTP server without spending schema tokens in every MCP session. Use MCP when the client has first-class MCP support and
you want persistent session deduplication.

### HTTP Server

`prism serve` is optional. Use it for custom automation that wants HTTP instead
of CLI or MCP:

```bash
prism serve --port 8888 /path/to/project
```

It binds to `127.0.0.1`.

---

## CLI Reference

```bash
prism init [--global] [--mode cli|mcp|both] [dir]
prism index [dir]
prism status [dir]
prism doctor [dir]

prism map [dir] [--depth N] [--component X] [--expand 'from->to'] [--json]
prism cycles [dir] [--depth N] [--json]

prism query <task> [dir] \
  --terms a,b,c \
  --include graph,tests,docs,coverage_gaps \
  --delivery source|symbols \
  --max-files 5 \
  --depth 2 \
  --format text

prism read <file> [dir] --format text
prism lookup <name> [dir] --format text
prism search <keyword> [dir] --format text
prism references <name> [dir] --format text

# Task-shaped graph operations — one deterministic call each
prism change-impact 'Type.method(ParamType, ...)' [dir]   # declaration + override family + all resolved callers
prism rename-plan 'Type.method' NewName [dir]              # every concrete edit line, review-and-apply
prism missing-implementations 'Type.method' [dir]         # types claiming the contract that do not implement it
prism untested-surface 'Type.method' [dir]                # the change-set split covered/untested by test evidence
prism dead-code [dir] [--roots a,b]                       # unreachable production symbols (precision-first)
prism affected <file> [file ...] [dir]                    # tests covering the changed files (CI selection):
                                                          #   git diff --name-only | xargs prism affected

prism watch [dir]      # background file-watcher: delta-reindex on save, index always warm
prism drift [dir]
prism savings [dir]
prism compact [dir]
prism feedback --tool <name> --rating <0-5> [dir]
prism mcp [dir]
prism serve [--port 8888] [dir]
prism version
prism --version
```

Output formats:

| Format | Use |
|---|---|
| `text` | Default and recommended for agents |
| `lean` | Compact JSON without most metadata |
| `json` | Full metadata for tooling/debugging |

---

## Configuration

`prism.yaml` is intentionally small:

```yaml
version: 1
profile: "default"
agent_mode: "cli"
```

Optional keys:

```yaml
model: "claude-sonnet-4-6"
grove_binary: "grove"
embeddings_backend: "tfidf"
```

Environment overrides include `PRISM_MODEL`, `PRISM_PROFILE`,
`PRISM_GROVE_BINARY`, and `PRISM_EMBEDDINGS_BACKEND`.

---

## Language Support

Prism delegates parsing and graph construction to embedded Grove.

| Language | Extensions |
|---|---|
| Go | `.go` |
| TypeScript / TSX | `.ts`, `.tsx` |
| JavaScript / JSX | `.js`, `.jsx`, `.mjs`, `.cjs` |
| Python | `.py` |
| Java | `.java` |
| Rust | `.rs` |
| C / C++ | `.c`, `.h`, `.cc`, `.cpp`, `.hpp`, ... |
| C# | `.cs` |
| PHP | `.php`, `.phtml`, ... |

Markdown, YAML, JSON, shell scripts, Dockerfiles, Makefiles, SQL, GraphQL, and
other non-code files are indexed as document symbols and can be requested with
`--include docs`.

---

## Benchmarks

One task, three ways to search — same agent, same frontier model, only the
tool changes. A signature change in **jackson-databind**: find all **8 call
sites** it breaks, including callers not named after the method (invisible to
text search). Oracle-scored.

| Tool | Sites found | Turns | Tokens | Cost |
|---|---:|---:|---:|---:|
| Plain grep — the agent's default | 5 of 8 | 19 | 376K | $0.90 |
| **Prism** | **8 of 8** | **3** | **60K** | **$0.14** |

Fewer turns, fewer tokens, lower cost — and the only one that found every
site. Run the same task through **Mason** (Prism built in) on a **free local
30B model**: **all 8, at $0** (0.997 mean recall across the 7-task
change-impact benchmark). Raw runs: [provasign/research](https://github.com/provasign/research).

---

The headline numbers (context reduction per scenario, repeat-read savings by
project size, and the SHA-pointer dedup mechanism) are summarized with
methodology at [provasign.dev/prism](https://provasign.dev/prism/). The full
benchmark reports were trimmed from this repo to keep it lean; they remain
available in git history (`git log --diff-filter=D -- docs/` to locate them).

Current practical summary:

- CLI `--format text` is the recommended default for shell-capable agents.
- Prism is strongest on graph/blast-radius/test/coverage-gap questions.
- Shell tools remain best for locating exact strings or filenames.
- MCP persistent transports add repeated-read deduplication that direct CLI
  invocations do not fully exercise.

---

## Troubleshooting

**`prism query` returns nothing**: run `prism index .` from the project root.

**Agent uses wrong steering**: run `prism init . --mode both` (or your chosen mode) and verify `prism.yaml` has the correct `agent_mode`.

**Wrong Prism binary**: run `command -v prism` and `prism version`. Reinstall if
the version is old.

**macOS quarantine**:

```bash
xattr -d com.apple.quarantine "$(which prism)"
codesign -f -s - "$(which prism)"
```

**MCP client does not connect**: restart the coding tool after `prism init`, and
approve project MCP configuration if the tool prompts.
