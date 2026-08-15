# Prism

> **The complete picture for your coding agent — in one call, at a tenth of
> the tokens.**
>
> Give Prism the task. It returns the edit-ready context and the complete,
> type-resolved change set — every override, implementation, and caller —
> as ONE deterministic call over your local code graph.

**Full benchmark harness, raw results, and every task file are public:
[provasign/research](https://github.com/provasign/research).**

**Measured** (oracle-scored, 4 languages, blast radii 8–310 sites — see
[provasign/research](https://github.com/provasign/research)):

- Engine completeness: **0.997 recall** on change-impact closure vs a
  compiler-grade oracle; same answer every run.
- Agent-level, current frontier model (Opus, 6 change-impact tasks, 8–310
  sites, 2026-08-08): **the same answer for ~1/6th the cost.** Recall 0.987
  with Prism vs 1.000 with grep and file reads — a frontier model gets there
  either way — but **6.4× fewer turns, 9.3× fewer input tokens, 6.1× cheaper,
  6.7× faster** (4.2 vs 26.8 turns; 90k vs 836k tokens; $0.27 vs $1.66;
  40s vs 271s per task).
- On weaker and cheaper models the gap is capability, not just cost: recall
  0.758 → 0.997 at the Haiku tier in the 2026-07 grid, where a text-search
  agent could not reliably reach a complete change-set at all.
- What the graph actually contributes is precision, not discovery: measured
  over 127 symbols in 6 repositories, a whole-word grep misses **no** resolved
  reference — but ~30% of its hits are not references at all (98% in one
  typeorm case: 372 hits, 1 real). Against compiler oracles, file-level
  precision goes 0.51 (grep) → 0.91 (change-impact) at comparable recall.

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
How much that costs depends on the model. A 2026-08 frontier model (Opus)
does reach a complete change-set from grep alone on 8–310-site tasks — in
26.8 turns and 836k tokens per task, against 4.2 turns and 90k with Prism.
Cheaper models do not get there at all: 0.758 recall at the Haiku tier in the
2026-07 grid. See [provasign/research](https://github.com/provasign/research)
for both.

**The principles.**

1. **Correctness and completeness first.** A faster or cheaper *incomplete*
   answer is a faster broken build. Every design choice is subordinate to
   returning the complete, type-resolved answer.
2. **Task altitude, not primitives.** The graph is exposed as whole-task
   operations (`change_impact`, `rename_plan`, `missing_implementations`, …), not as
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
7. **Declared architecture, enforced.** `prism arch` validates `arch_deny:
   "<from> -> <to>"` rules from prism.yaml against the induced view — every
   violation cites the exact `file:line` crossings, and exit 1 makes it a CI
   gate. Tier-aware by design: violations backed by structural-or-stronger
   evidence fail the build; heuristic-only evidence (e.g. interface dispatch
   attributed across a boundary — dependency inversion read backwards) is
   reported for review, not auto-failed (`--strict` escalates). Measured at
   the engine ceiling on injected Go violations: 10/10 detected, 0 false
   positives (see the injection benchmark in the test suite).
8. **Verify the diff — the completeness gate for agent-authored changes.**
   `prism verify` deterministically checks a diff: it detects contract
   changes (signature changes, renames, interface-member changes), computes
   the required change set from the **base** contract, and reports every
   dependent site the diff did not touch — line-precise. Measured on 9 real
   corpora with seeded incomplete edits (27 trials + 9 controls):
   - **Verdict is fail-closed** — 0 false "complete" across every run; an
     incomplete change is never waved through. Safe as a CI gate.
   - **Site listing catches 88%** of forgotten files (django, grafana,
     jackson-jsonnode, typeorm at 100%; guava 91%; serialize 88%), with
     **zero false accusations** — verify never flags a site the diff
     already handled. It gets there by enumerating dependents of the *old*
     contract (base-signature family + callers, generic-aware, member-level
     for interface blocks), not the post-edit graph the change severs.
   The one dimension no compiler covers in dynamic languages: a Python or
   TypeScript signature change with a forgotten caller compiles clean and
   fails at runtime — verify reports the exact line (see
   `docs/DESIGN_LAYERED_INTELLIGENCE.md`, Phase 3).

**The surface — one route per need.** There is deliberately no natural-language
front door: a v0.41.0 measurement showed NL-as-the-only-retrieval-key loses to
the agent picking a route and passing its own confirmed anchors. The agent
surface is six tools, one per question shape: `prism_query` (task + `terms=`
anchors → edit-ready source windows), the cheap reads (`prism_read`,
`prism_lookup`, `prism_search`), `prism_change_impact` (the complete change
set for a symbol), and `prism_verify` (is this diff complete?). Everything
else — `map`, `dead-code`, `rename-plan`, `missing-implementations`, `arch`,
`node`, `references`, `index` — is a CLI command and an HTTP route, but is
not advertised to agents (see [MCP](#mcp) for why).

**Use cases** — the questions Prism answers in one call:

| You are about to… | One call |
|---|---|
| Change or rename a method signature | `change-impact` — declaration + override family + every resolved caller |
| Apply a rename, not just find it | `rename-plan` — every edit line, before/after, review-and-apply |
| Make an interface method required | `missing-implementations` — every type that breaks |
| Delete or extract code | `dead-code` — unreachable production symbols |
| Commit an agent-authored diff | `verify` — missed change-impact sites, line-precise; exit 1 if incomplete |
| Read code cheaply | `read` / `lookup` — session-deduped, ~30-token repeat reads |
| Expand from a grep hit | `query` — callers, callees, tests around an anchor |

**Where Prism is the wrong tool** (honesty is a feature): languages outside
the supported set below, dispatch wired at runtime through
frameworks/reflection/DI (Prism's edges are static and type-resolved — it
will show you *nothing* rather than a guess), and one-line greppable changes
where any approach ties.

Locating strings is covered too: `prism_search` runs a real full-text
rg/grep pass alongside symbol search (`scope="text"` is a pure grep), so a
separate grep tool is never needed. Prism's distinct value is the follow-up
questions that usually cost several file reads:

- What calls this?
- What does this call?
- Which tests define the contract?
- What else is in the blast radius?

One steering template covers both surfaces (MCP tools as primary, CLI
fallback for subagents that don't inherit the MCP session):

```bash
prism init .
```

**Routing is structural, not rhetorical.** Steering alone does not route
agents — measured 12:1, an agent will acknowledge its CLAUDE.md and then run
`grep` anyway. Interactive `prism init` therefore offers (and
`--deny-builtin-search` forces) the one change that actually routes: adding
`Grep`, `Bash(grep:*)`, `Bash(rg:*)` to `permissions.deny` in the
PROJECT's `.claude/settings.json` (machine-global only with `--global`).
Nothing becomes unfindable —
`prism_search(scope="text")` is a ripgrep passthrough — and it's reversible
by deleting those lines. Claude Code only; CI and non-interactive runs are
never prompted and get no settings change.

**Setup is project-level by default.** A plain `prism init` touches only
files inside the repo (`.mcp.json`, steering files, the project's
`.claude/settings.json`). Tools whose configs are user-global — Zed, Codex
CLI, opencode — are registered only when interactive init's *"Register
user-global tools?"* question is answered yes, or with `--global`.

Agents with an active MCP session call `prism_query`, `prism_read`, and
`prism_lookup` directly. For bug-fix and implement tasks `prism_query`
delivers verbatim line-numbered source windows plus each anchor's callers and
covering tests (edit-ready, phase-aware; `--delivery symbols` forces the
compact list). Subagents and CI scripts fall back to the CLI:

```bash
prism query "why does a repeat read return a cached pointer" --terms prism_read --include graph --format text
prism read internal/mcp/tools.go --format text
prism lookup github.com/provasign/prism/internal/mcp.ToolSchemas --format text
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
prism search ToolSchemas --scope text        # a real rg pass, inside prism
  -> prism query "write tests for ToolSchemas" \
       --terms ToolSchemas \
       --include graph \
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
commands. (The `coverage_gaps` scenario refers to a since-removed feature:
heuristic test-coverage edges measured 4–12% recall against real runtime
coverage and were removed rather than shipped.)

A controlled A/B re-run (2026-06-12, post Grove-v0.6.2 fixes) on the payflow
ground-truth project: total agent-token parity with the shell baseline (the
2026-06-07 run had +27–147% overhead) and 47 vs 84 tool calls. Repeat reads
cost 29 tokens (95% saved); a rename under the agent's feet is reported as
one breaking `renamed` entry for ~130 tokens. Full report:
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
VERSION=v0.33.0 curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
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
prism init .
```

Indexing is automatic — the MCP server indexes at startup, a never-indexed
repo indexes itself on first query, and whole-repo graph ops delta-refresh
before they run. (`prism index .` still exists for warming the index
manually, e.g. in CI.)

This writes:

- `prism.yaml` (version + profile; add `arch_deny:` rules to make
  `prism arch` a CI gate)
- `.mcp.json` wiring the MCP server for MCP-capable clients
- steering files such as `AGENTS.md`, `CLAUDE.md`, `.cursorrules`,
  `.windsurfrules`, `.github/copilot-instructions.md`, and others
- compatible tool config files where detected

The generated agent instructions tell agents to use commands like:

```bash
prism query "trace the payment refund flow" --terms RefundPayment --include graph --format text
prism query "audit UpdatePayment auth" --terms UpdatePayment,RequireScope --include graph --format text
prism read internal/payment/service.go --format text
prism lookup github.com/example/payflow/internal/payment.(*Service).RefundPayment --format text
```

Recommended agent workflow:

1. Locate the first anchor with `prism search` (`--scope text` is a pure
   rg/grep pass).
2. Run `prism query` with the same anchor terms.
3. Use `prism read` for whole files only when needed.
4. Use `prism lookup` for one known function or method.
5. Treat task-op outputs as terminal structured results, not the start of
   manual cross-referencing.

---

## Other Modes

```bash
prism init .              # non-interactive; registers MCP servers and writes
                          # one steering block covering MCP tools and the CLI
                          # (--mode is accepted and ignored since v0.38.0)
```

### MCP

MCP advertises **six** tools: the context surface (`prism_query`,
`prism_read`, `prism_search`, `prism_lookup`), `prism_change_impact`, and
the `prism_verify` gate. Search runs a real full-text pass
(rg/grep/built-in) alongside symbol search, so agents never need a separate
grep tool.

It was fourteen until v0.53.0. A 190-cell paired A/B measured which ones
agents actually reach for: `search` in 95 cells, `read` 53, `query` 35,
`lookup` 29, `change_impact` 2 — and `map`, `dead_code`, `rename_plan`,
`missing_implementations`, `arch_check`, `node` and `index` at **zero calls
in all 190**. Those eight were charging ~9.4 KB of schema per session to
never be called, and a long menu measurably mis-routes the tools that are.
`change_impact` stays despite two calls because it carries the whole
concentrated win (4.2 turns / $0.27 against grep's 26.8 / $1.66).

Nothing was removed from the product: every demoted tool is still a CLI
command and still an HTTP route (`docs/HTTP_API.md`), alongside the ones
that were already CLI-only — `resolve`, `edges`, `cycles` (a field of
`map`'s result), `drift`, and the telemetry commands (`savings`,
`feedback`, `compact`). Use MCP when the client has first-class MCP support
and you want persistent session deduplication.

### HTTP Server

`prism serve` is optional. Use it for custom automation that wants HTTP instead
of CLI or MCP:

```bash
prism serve --port 8888 /path/to/project
```

It binds to `127.0.0.1` (local only — no auth, no TLS) and exposes every
dispatchable tool as `POST /<tool_name>`, plus `GET /health` and
`GET /status`. Full route, request, and status-code reference:
[docs/HTTP_API.md](docs/HTTP_API.md).

### Go library

`pkg/kit` embeds the same engine in a Go program — `kit.Open(dir)`, then
`Invoke("<tool_name>", args)` with the same argument names as MCP; used by
downstream agents like mason. Usage and API surface:
[docs/GO_KIT.md](docs/GO_KIT.md).

---

## CLI Reference

```bash
prism init [--global] [dir]     # 'prism install' is an alias
prism index [dir]
prism status [dir]
prism doctor [dir]
prism config [dir]              # show resolved configuration

prism map [dir] [--depth N] [--component X] [--expand 'from->to'] [--json]
prism cycles [dir] [--depth N] [--json]
prism arch [dir] [--deny 'from -> to'] [--strict] [--json]   # exit 1 on violation
prism verify [dir] [--base REF] [--strict] [--json]          # exit 1 if incomplete

prism query <task> [dir] \
  --terms a,b,c \
  --include graph,docs \
  --delivery source|symbols \
  --max-files 5 \
  --format text

prism read <file> [dir] --format text
prism lookup <name> [dir] --format text
prism search <keyword> [dir] [--scope text|symbols|both] [--regex] --format text
prism node <symbol-or-file> [dir] --format text
prism references <name> [dir] --format text
prism resolve <name> [dir]
prism edges <name> [dir] [--direction in|out] [--kinds calls,uses-type,...]

# Task-shaped graph operations — one deterministic call each
prism change-impact 'Type.method(ParamType, ...)' [dir]   # declaration + override family + all resolved callers
prism rename-plan 'Type.method' NewName [dir]              # every concrete edit line, review-and-apply
prism missing-implementations 'Type.method' [dir]         # types claiming the contract that do not implement it
prism dead-code [dir] [--roots a,b]                       # unreachable production symbols (precision-first)
prism assist [--model <spec>] [--apply] [--verify "<cmd>"] "<task>"   # NL task -> deterministic ops via any model

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
```

Optional keys:

```yaml
model: "claude-sonnet-5"          # sizes context budgets; NO auto-detection,
                                  # unset means a safe 200k default
arch_deny: "cli -> mcp"           # repeatable; validated by 'prism arch'
```

Environment overrides: `PRISM_MODEL`, `PRISM_PROFILE`. (`agent_mode` is
accepted and ignored for backward compatibility; `grove_binary` /
`embeddings_backend` are vestigial — Grove is embedded in-process.)

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
| Plain grep — the agent's default | 8 of 8 | 32 | 1,117K | $1.60 |
| **Prism** | **8 of 8** | **3** | **59K** | **$0.16** |

*(Re-measured 2026-08-08 on Opus + prism v0.37.0. A 2026-08 frontier model
does grep its way to a complete change-set on this task — an earlier run of
this table, on the models of 2026-07, had it finding 5 of 8. What Prism
changes now is the cost: **10× fewer turns, 19× fewer tokens, 10× cheaper**.
On cheaper models the gap is still capability.)* Run the same task through
**Mason** (Prism built in) on a **free local 30B model**: **all 8, at $0**
(0.997 mean recall across the 7-task change-impact benchmark). Raw runs:
[provasign/research](https://github.com/provasign/research).

---

The headline numbers (context reduction per scenario, repeat-read savings by
project size, and the SHA-pointer dedup mechanism) are summarized with
methodology at [provasign.dev/prism](https://provasign.dev/prism/). The full
benchmark reports were trimmed from this repo to keep it lean; they remain
available in git history (`git log --diff-filter=D -- docs/` to locate them).

Current practical summary:

- CLI `--format text` is the recommended default for shell-capable agents.
- Prism is strongest on graph/blast-radius questions.
- Shell tools remain best for locating exact strings or filenames.
- MCP persistent transports add repeated-read deduplication that direct CLI
  invocations do not fully exercise.

---

## Troubleshooting

**`prism query` returns nothing**: run `prism index .` from the project root.

**Agent uses wrong steering**: re-run `prism init .` — it rewrites the block between the `<!-- prism:start -->` markers in CLAUDE.md/AGENTS.md and leaves everything else untouched.

**Wrong Prism binary**: run `command -v prism` and `prism version`. Reinstall if
the version is old.

**macOS quarantine**:

```bash
xattr -d com.apple.quarantine "$(which prism)"
codesign -f -s - "$(which prism)"
```

**MCP client does not connect**: restart the coding tool after `prism init`, and
approve project MCP configuration if the tool prompts.
