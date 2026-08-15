# Handoff — prism deployment reset, 2026-08-15

The v0.44–v0.51 deployment arc was evaluated, found not to pay for itself, and
reverted. The revert shipped as **v0.52.0**; **v0.52.1** followed with a CLI
fix, and **v0.53.0** acted on the evaluation's own recommendation by trimming
the deployment surface. Read this before touching the steering or `prism init`.

**Do not trust the older memory entries or `research/RESULTS.md` §1–§7 as a
description of current behaviour.** They describe the arc that was reverted.
§8 and §9 of RESULTS.md are still accurate and load-bearing.

---

## 1. Where things stand

`main` is pushed and tagged. On top of `v0.43.0`:

| commit | what |
|---|---|
| `2daf496` | revert: restore the v0.43.0 deployment surface in full |
| `a2a351d` | feat: project-level init by default (+ a `--global` scope bug fix) |
| `012ea9c` | chore: drop the grep/rg denial from this repo's own settings |
| `48964a9` | docs: this handoff |
| `701e099` | chore: point local MCP registrations at the brew-installed prism |
| `0f042cf` | fix: `prism <cmd> --help` prints usage instead of running the command |
| `7ea33d5` | ci: stop pretending the release publishes the Homebrew formula |
| `feff95f` | refactor(mcp): cut the agent tool surface from 14 to 6 |
| `67114cd` | docs(steering): cut the always-loaded block from 11.8k to 1.8k chars |

Tags: **v0.52.0** = the revert + project-scoped init. **v0.52.1** = the
`--help` fix. **v0.53.0** = the surface trim (§5.4), installed and verified:
`brew install provasign/shale/prism`, `tools/list` returns exactly the six.

The product tree is byte-identical to `v0.43.0` except for those feature/fix
commits and `.shale/` (session evidence, deliberately preserved — it is the
audit record, not product code). `git diff v0.43.0..HEAD -- go.mod` is empty,
and was empty across the entire arc: **no engine change was ever involved.**
Grove and astkit are untouched, so the multi-hop receiver-chain and
interface-satisfaction gains are intact. v0.43.0 already carries the
symlink-containment check in the native scanner, so nothing security-relevant
was reopened.

Gate run before each tag, both green:

- `go vet ./...` and `go test ./...` — 12 packages.
- `python3 harness/ci_invariants.py --corpus-root ~/.cache/prism-ci-corpus
  --prism <binary>` from the research repo — 13 ceiling tasks across 6 corpora
  at baseline recall/precision, missing-implementations 0 (1 documented),
  rename-plan contract declarations resolved, double-cold-index byte-identical.

**Keep the corpus root durable.** `~/.cache/prism-ci-corpus`, never `/tmp` —
macOS purges it and you will re-download six repos every run.

## 2. Why the arc was reverted

Not on argument — on a paired measurement anyone can re-run.

`research/harness/runs/swebench-live/` (`ab38-f1`, `ab38-t1`, `ab38-t2`,
`haiku38`, `sonnet38`) holds 190 paired cells: same task, prism arm vs
baseline arm, with per-cell tokens, cost, turns and full tool traces.

**Cost was a coin flip.** Median paired delta −$0.054; prism cheaper in
107/190. The sign flips per bed: ab38-t1 −$0.270, ab38-t2 −$0.294, but
ab38-f1 +$0.116 and sonnet38 +$0.101.

**The tool mix explains it:**

| tool | calls | cells |
|---|---:|---:|
| `prism_search` | 637 | 95/190 |
| `prism_read` | 166 | 53/190 |
| `prism_lookup` | 55 | 29/190 |
| `prism_query` | 38 | 35/190 |
| `prism_change_impact` | **5** | **2/190** |
| map, dead_code, rename_plan, missing_implementations, arch_check, node, index | **0** | **0** |

The deployment routed reliably — and routed to `prism_search(scope="text")`,
which is *defined* to return what ripgrep returns. So the call carrying the
measured 9.3× token win (`RESULTS.md` §9.1) was reached in 1% of cells, and
8 of 14 always-loaded tools were never called at all.

**Mechanism worth keeping in mind:** cache reads dominate (700k–3.2M tokens
per cell vs 24–62k fresh). A large tool result is paid once fresh and then on
every later turn. A 5 KB result at turn 3 of 28 is paid ~25 times. **Payload
size matters more than call count.**

## 3. What is actually true about prism

These survive the revert and should anchor any future work:

- **The graph finds nothing grep misses.** 0 graph-only resolved references
  across 127 symbols, 6 repos, 4 languages (§8.2.2). What it does is *filter*:
  file-level precision 0.51 (grep) → 0.91 (change-impact).
- **The win is concentrated.** Opus on change-impact tasks: grep reaches
  recall 1.000 in 26.8 turns / 836k tokens / $1.66; prism reaches 0.987 in
  4.2 turns / 90k / $0.27 (§9.1). On ordinary bug fixes it is parity (§8.1).
- **e2e resolve rate is the wrong instrument** (§8.2.1): cost moved 46% → 2%
  and turns 31 → 3 while resolve rate moved not at all.
- **Agents decline graph tools when a text tool is present.** Measured twice,
  independently — the 190-cell mix above, and the §8.2 codegraph arm that made
  0 calls in 6 cells.
- **MCP exists to keep the index open, and that is worth 35–65×.** A CLI
  invocation is a fresh process that reopens the store every time. Measured
  2026-08-15 on grafana-122750 (1.1 GB `.grove`), same binary:

  | operation | CLI per call | MCP warm |
  |---|---:|---:|
  | `search --scope symbols` | 4.05s | 2.15s first, then **0.06s** |
  | `lookup` | 3.05s | **0.11s** |
  | `search --scope text` | 0.56s | 0.47s |

  `--scope text` is a ripgrep pass that never touches the index, so it is
  ~equal; the whole gap is `lookup` / `--scope symbols` / `change-impact` /
  `query`. On a small repo (prism itself) CLI startup is 0.03–0.09s and the
  effect is invisible — it only appears at monorepo scale. This is the
  architectural reason for MCP and it is independent of token accounting.
  It had to be rediscovered on 2026-08-15 after a full session of reasoning
  about CLI-vs-MCP purely in tokens. **Do not lose it again.**

  MCP's fixed token overhead, by contrast, is now negligible: **+92 fresh
  tokens** on a cell where prism is never called (v054-smoke-fixed). An
  earlier +19k figure came from a run carrying a stale grep-denial config
  and is wrong.

  MCP also has a session ledger the CLI structurally cannot have — a repeat
  `prism_read` of an unchanged file returns a pointer, while `prism read`
  through a shell re-delivers the body.

External work agrees and is worth citing:

- **ContextBench** (arXiv 2602.05892, 1,136 tasks): a plain-bash agent beats
  graph-based scaffolds on file-level context F1, 0.634 vs 0.403. Aggressive
  retrieval "mainly increases token consumption."
- **What Context Does a Coding Agent Actually Need?** (arXiv 2607.09691):
  structural representations null at p=0.75, N=70 — but *compression* wins
  big, 19K vs 94K tokens per resolution.
- **SWE-Explore** (arXiv 2606.07297, 848 instances): context efficiency
  correlates with repair success at r=0.95. Removing 25–50% of core evidence
  collapses resolve rate; adding redundant regions barely matters. **Missing
  evidence costs far more than noise — never let a filter drop silently.**
- **Serena / ManoMano**: the nearest comparable product is ~4× the cost and
  60% slower on simple lookup, mandatory on deep refactors. Same asymmetry.

Full write-up, with the measurements and the seven-change proposal:
https://claude.ai/code/artifact/c9409bf3-90c7-47b3-934c-d132ad0b7384

## 4. Known gaps at this base (accepted, not accidental)

- **No path-scoped search.** `prism_search` has no `path=` / `context=` /
  `files_only=`. The v0.51.0 mining found **511 of 946** real grep invocations
  were path-scoped, so scoped questions now return whole-tree results. This is
  the loss most likely to bite in daily use, and the strongest candidate for
  re-landing on its own.
- **No PreToolUse hook.** `prism hook pretooluse` does not exist at this base.
  Any stale registration pointing at it makes every Bash call shell out to an
  unknown command — two such registrations were removed from this workspace.
- **Steering is a 1.8k-char block** (was 11.8k until the 2026-08-15 trim) that
  teaches the ToolSearch deferred-tools dance, one route per question, and the
  change_impact relay rule. Nothing else. `alwaysLoad` is gone too, so
  client-side deferral is back and the two agree.
- **Tools are deferred, not resident.** `.mcp.json` has no `alwaysLoad`.
  **6 tools, ~6.2k chars of schema** (was 14 / ~17.8k), loaded on demand via
  `ToolSearch`. The eight demoted on 2026-08-15 — `references`,
  `missing_implementations`, `dead_code`, `rename_plan`, `map`, `node`,
  `arch_check`, `index` — had **zero calls across all 190 A/B cells**. They
  remain CLI commands and HTTP routes; only the agent menu narrowed. Do not
  re-advertise one without call-count evidence.
- **`prism init` writes deny rules only when asked.** It has no path that
  *removes* them; that is manual. This repo ships none.
- **`excludeDirs` is only `[".git", ".grove"]`**, so text search reads
  `.shale/`, `.claude/` and `.cursor/` — prism indexes its own session
  transcripts as source.

## 5. If the deployment layer is rebuilt

The proposal from the evaluation, in priority order. A+B+C carry almost all
the value; none requires the agent to learn anything new.

1. **Deny only what prism can answer better.** Classify the pattern; if it
   does not resolve to an indexed symbol, let the search run. 511 path-scoped
   + 289 pipe-filter invocations = **85% of real grep usage the graph cannot
   improve**. The v0.51.0 commit message has the full mined breakdown.
2. **Answer inside the denial.** `permissionDecisionReason` is fed back to the
   model, and the hook is a prism process with the index open. Return the
   resolved site list instead of an instruction to go fetch it. Removes the
   retry turn entirely.
3. **Default payloads to locations, not bodies.** See the cache-read
   compounding note in §2.
4. ~~Defer the tools nobody calls; the measured routing-critical set is four:
   `search`, `read`, `lookup`, `query`.~~ **DONE 2026-08-15** — agent surface
   cut to those four plus `change_impact` and `verify`. See §4.
5. Exclude agent-state dirs (`.shale`, `.claude`, `.cursor`) from text search.
6. Do not re-ship the consolidation nudge. Three replications, three ignores.
7. Never let the graph narrow a result silently — state both counts.

**Gate any of this before shipping:** replay the 946 mined grep invocations
through the classifier offline (no LLM, seconds) and report token delta plus
how many get a better answer. Then `ci_invariants.py`, then `go test ./...`.

## 6. Things that will surprise you after a restart

- prism's tools will **not** be in the tool list. Load them with
  `ToolSearch("select:prism_query,prism_search,prism_change_impact")`.
- **grep and rg work.** No denial, no hook, anywhere. Verified clean in the
  project settings, `~/.claude/settings.json`, `settings.local.json` and the
  workspace settings.
- `CLAUDE.md` and the eight sibling steering files carry the long v0.43 block.
- **A running MCP server keeps serving the binary it was launched with.**
  Upgrading prism on disk does nothing for a live session — the server detects
  this and says so in its tool results. Restart the agent after an upgrade.
  The CLI is unaffected: every invocation is a fresh process.
- The parent workspace `/Users/tapabratapal/Projects/provasign/` is **not a
  git repo** — changes there have no undo. Back up before editing.

## 7. Release and tap mechanics

Tagging `vX.Y.Z` runs `.github/workflows/release.yml`: five platform builds,
checksums, SPDX SBOM, artifact attestation, install scripts.

**The Homebrew formula is not published from there, and must not be.**
`provasign/homebrew-shale` refreshes itself: `refresh-formulae.yml` runs daily
and on `workflow_dispatch`, regenerates any formula behind its repo's latest
release (prism, mason, fuse, shale), and pushes to its *own* repository with
the built-in `GITHUB_TOKEN`. That design exists precisely so no tool repo
needs a cross-repo push token — the arrangement that let the tap freeze at
0.23.0 through 15 prism releases.

Consequence: the tap lags a tag by up to a day. To publish immediately:

    gh workflow run refresh-formulae.yml -R provasign/homebrew-shale

prism's release workflow used to carry a publish step guarded on a
`HOMEBREW_TAP_TOKEN` secret that was never set. It warned "tap NOT updated" on
every single release and made the working mechanism look broken; `7ea33d5`
removed it. **Do not add it back.**

Install: `brew install provasign/shale/prism` (one family tap — never create
another). On this machine `/opt/homebrew/bin/prism` is the release build and
both `.mcp.json` files point at it; `~/bin/prism` is a local dev build that
still precedes it on `PATH`, so a bare `prism` in a shell may be the dev
binary.

## 8. Bugs

**Fixed in v0.52.1** — `prism <cmd> --help` ran the command instead of
printing usage. `prism init --help` performed a *full init*: prism.yaml, nine
steering files and every project MCP registration. `prism search --help`
searched for the string `--help`. One mechanism: nothing parsed `-h`/`--help`
before the command body. `Run()` now answers it before dispatch for every
subcommand and prints that command's own block. A bare `help` argument is
still a query term, so `prism search help` searches.

**Still real, in code that no longer ships** — recorded so they are not
rediscovered from scratch if the hook is ever rebuilt:

- `permissions.deny` **beats** a PreToolUse hook's `allow`. Verified directly.
  This made v0.51.0's pipe-filter fix inert in the shipped config: the hook
  returned `allow` for `git log | grep x` and the call was blocked anyway.
- The v0.51.0 deny reason's CLI form omitted `--scope text`, a **3.8×**
  overcharge (5,126 vs 1,348 bytes) printed by the product's own
  highest-compliance string.
- `grepCommandPattern` treats `(` as a command separator, so any Bash command
  whose *text* contains `(grep` is denied — it fired on a Python script
  manipulating a deny list. Fails closed, so a nuisance rather than a hazard.

## 9. Loose ends

- `scratchpad/steering-fixes-uncommitted.patch` (1,152 lines) — the abandoned
  intent-routing steering rewrite from earlier in the session. Written against
  the v0.51 base; salvage ideas, not the diff.
- The workspace `CLAUDE.md` has a user-authored `## MCP Tooling` section that
  predates all of this and still says to use `prism_*` for "file reads, symbol
  lookup, search". It is the user's, left alone, but it sits against §3.
- Pushes to `main` report "Bypassed rule violations … Required status check
  `test (ubuntu-latest)` is expected." CI runs after the push rather than
  gating it. Harmless given the local gate, but it means a red CI will not
  stop a tag — check it.
