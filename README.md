# Prism

> **Graph-ranked code context for AI coding agents.**
>
> Prism turns a task plus a few precise anchors into the code, callers, callees,
> tests, docs, and coverage gaps an agent needs to make a change safely.

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
`prism_lookup` directly. Subagents and CI scripts fall back to the CLI:

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
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash

# Windows PowerShell
irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex

# Pin a version
VERSION=v0.5.6 curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
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

MCP advertises six tools: `prism_query`, `prism_read`, `prism_search`,
`prism_lookup`, `prism_index`, and `prism_drift`. The auxiliary tools
(`prism_savings`, `prism_feedback`, `prism_compact`, `prism_evidence`) stay
available through the CLI and HTTP server without spending schema tokens in
every MCP session. Use MCP when the client has first-class MCP support and
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

prism query <task> [dir] \
  --terms a,b,c \
  --include graph,tests,docs,coverage_gaps \
  --depth 2 \
  --format text

prism read <file> [dir] --format text
prism lookup <name> [dir] --format text
prism search <keyword> [dir] --format text
prism savings [dir]
prism compact [dir]
prism feedback --tool <name> --rating <0-5> [dir]
prism mcp [dir]
prism serve [--port 8888] [dir]
prism version
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
