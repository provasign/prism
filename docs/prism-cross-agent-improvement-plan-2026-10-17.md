# Prism: changes justified by the October 17 transcripts

Date: 2026-10-17. Status: research and implementation proposal; no product changes or new paid runs.

## Recommendation

Prioritize **consistent instructions and internally consistent answers**. Today's evidence supports three product changes and two supporting changes. It does not support another broad token-trimming project.

The strongest new findings are specific: installed steering contradicts MCP steering; an impact answer sends the agent to the wrong “wider” anchor; related declarations are labelled unrelated; a direct-caller count becomes an incorrect claim about production use. These can cause extra investigation and unsafe decisions. Fix their correctness first, then measure cost.

This supplements the [earlier implementation plan](cost-reduction-implementation-plan-2026-10-17.md). Usage persistence, seeded arm ordering, startup separation, `read force:true`, and project-root disclosure are already implemented. They are not new roadmap items.

**Constraints:** Prism answers questions; it never edits, emits execution commands, or runs builds/tests. The user selects the model. Preserve the existing schema unless a change passes an explicitly approved paid call-shape check. Paid runs remain stopped; close new correctness findings before requesting another run. Keep changes on `main`, uncommitted until reviewed and approved.

## What was already tried

Earlier experiment results below come from the recorded experiment notes and the prior plan, not newly repeated runs. Dates are the notes' experiment labels; some filenames carry later dates than their recorded release dates. Pre-September-20 measurements describe implementation history, not current effectiveness.

| Lever | Recorded date / result | Decision for this plan |
|---|---|---|
| Startup steering reduction | v0.53.0: 11,834 → 1,847 characters. PR #21 / Oct 1: trim27 cost 0.996x, solves 19/27 both. | Already done; no new startup-trim project. |
| Tool-schema trimming | Oct 1: full schema 19/27 solved; two shortened versions 17/27 and 16/27; nested argument errors increased. Reverted. | Keep per-field operation guidance. |
| Deferred versus resident tools | Aug 29 decision and residency guard; host integrations differ. | Verify effective configuration; do not reopen residency as a savings claim. |
| Duplicate delivery / compaction dedup | Aug 29: query overlap ~4% of bytes, read re-delivery 3.3%; Sep 30 rereads <5%. Zero compactions in 80 long-session cells. | No broad dedup or compaction project. Keep explicit source recovery working. |
| Forced graph use / routing hooks | Sep 29–30: forced graph arm matched 18/30 native solves but cost +24%; other wording changes did not induce useful adoption. Oct 11 forced routing added blocked calls. | Do not require graph work on every edit. |
| Task-triggered impact steering | Oct 12: impact-before-edit 0/4 → 4/4 on contract changes; bug controls 0/4 in both arms; cost roughly flat. Shipped. | Preserve the successful early trigger; remove the contradictory blanket obligation. |
| Batched / larger combined impact answers | Oct 11: requests 0.865x initially, then 1.13x; rerun tokens 1.30x, cost 1.20x. | Do not combine more discovery operations speculatively. |
| Automatic neighboring code | Sep 28 replay: only 7% of later function reads covered, answer size 1.77x; capped version covered 6% at 1.32x. | No automatic neighborhood expansion. |
| Search body suppression / outlines | Oct 5–6: withholding bodies often caused targeted reads; 80-session study did not establish savings. Correctness fixes shipped, blanket suppression dropped. | Preserve the measured body policy and 36k impact budget. Tool text was measured near 2 characters/token, not 4. |
| Extra mention lists / coverage blocks | Oct 13 and later recorded coverage experiments: greps shifted or persisted; no repeatable gain. | Do not add another reassuring coverage paragraph. Correct false claims at their source. |
| Exact source occurrences in `editSites` | v0.87.27: 60/60 solved both across three repetitions; recorded cost 0.91x, tokens 0.83x. | Preserve this useful representation; repair inconsistencies around it. |
| Per-call compiler positions | Oct 12 Gson probe: requests/turn metric 16 → 8, Maven calls 7 → 2; small probe, subsequently shipped. | Keep exact overload/site evidence; do not sell it as new work. |
| Task-sentence field, graph SQL, data-flow tool | Oct 11 task sentence: ~+2% cost; graph SQL 0/30 adoption; Oct 13 trace study only ~9 relevant searches among 1,932, each answered in one search. | No new task-description field, graph API, or speculative trace operation. |
| Environment fixes | Oct 11: stale dependency seeds/missing packages caused much of ~8–9% wasted calls and were fixed. | Today's different sandbox failures justify targeted host fixes, not a claimed new Prism engine saving. |
| Smaller models | Long-session study: Haiku+Prism 34/40 at $2.40; Sonnet native 37/40 at $4.38. | Lower cost did not preserve quality. No automatic model substitution. |
| Editing, generated commands, validation inside Prism | Rejected Sep 28; edit-moment deny hooks also rejected. | Closed direction. Host execution stays outside Prism. |
| Batch API / general accounting rewrite | Batch unavailable for these CLI runs; token totals already reconciled twice. | Keep existing accounting and pricing provenance. |

## Today's measured results

These are **task costs with startup excluded**. Claude values are a 5-minute-cache replay of cache-disabled runs; Codex values are the recorded list-price estimates under default caching. They are separate protocols, not interchangeable bills.

| Run | Pairs | Solved native / Prism | Model requests native / Prism | Task cost native / Prism | Prism/native |
|---|---:|---:|---:|---:|---:|
| Claude Code, seeded/wide bed | 32 | 32 / 32 | 216 / 172 | $1.99949 / $1.77758 | **0.889x** |
| Claude Code, issue bed | 41 | 23 / 23 | 248 / 243 | $2.03258 / $2.25100 | **1.107x** |
| Codex, completed mixed pairs | 13 | 11 / 11 | 209 / 211 | $1.68265 / $1.94197 | **1.154x** |
| Codex, wide subset | 6 | 6 / 6 | 101 / 111 | $0.86182 / $0.98632 | **1.144x** |
| Codex, bug subset | 7 | 5 / 5 | 108 / 100 | $0.82084 / $0.95565 | **1.164x** |

Sources: [Claude wide summary](../../research/harness/results/nocache32-2026-10-17/SUMMARY.md), [Claude issue summary](../../research/harness/results/nocache-issue42-2026-10-17/SUMMARY.md), [Codex report and protocol](../../research/harness/results/codex-mixed16-warm-2026-10-17-stopped/REPORT.md). The [audit JSON](research/prism-cross-agent-2026-10-17/audit.json) recomputes totals, records source hashes, and preserves selected tool results. Its [recomputation script](research/prism-cross-agent-2026-10-17/recompute.py) only reads existing runs.

Startup, separately: Claude wide averaged $0.06069 native / $0.06837 Prism per cell; Claude issues $0.06091 / $0.06876. Codex's observed first-input cost averaged $0.00596 / $0.00774 under its warm-cache protocol. Those Codex figures are not cold-start prices. No startup-amortization estimate is used to claim an engine improvement.

Important distinctions:

- Claude issue costs on the 22 pairs both arms solved were 1.035x. That is a diagnostic subset selected after outcomes, not a replacement for the 1.107x all-pair result. The Prism-only Jackson win was expensive. The user adjudicated Zod pr6129 as a model judgment rather than a Prism failure; raw scorer totals remain 23/41 versus 23/41, and the adjudication stays separate.
- Claude wide and the corrected Codex mixed run used the same candidate SHA256, `6a539ae62cab737a4424dfd71859a9c304938d85b66bd08177fc7542bbefc9f8`. Claude issues used a later build, recorded prefix `a1ad26325bb668f6`, with the module-path, force-read, and scope fixes.
- On the six identical wide tasks with the same Prism binary, Claude requests were 44 → 29 and task cost $0.42871 → $0.30101. Codex requests were 101 → 111 and task cost $0.86182 → $0.98632. Claude made 7 Prism calls; Codex made 36. This establishes different observed behavior; it does not isolate the effect of model, host, instructions, or cache.
- Across all 13 matched tasks, Claude made 22 Prism calls and Codex 88. Codex made eight impact calls on seven bug tasks; Claude made none on its entire 41-task issue bed. Not every bug impact call was unnecessary, but mandatory steering is a concrete suspect.
- Codex's 13-pair premium was $0.25931: $0.19894 additional fresh-input cost, $0.05054 output, and $0.00983 cached input. Thus fewer requests alone is insufficient: its bug arm used fewer requests and still cost more. These are accounting components, not causal attribution to individual Prism answers. Rates are those recorded by the runner, not a new pricing-table claim.
- Codex stopped with 13 complete pairs and one Prism-only Zod cell. The incomplete pair is excluded. Earlier Codex runs used the wrong binary and different setup; their discovery/polling figures are not mixed into this result.

## 1. P0 — Make the impact policy consistent in every installation

**Confirmed defect.** [Installed steering](../internal/cli/commands.go) says to run impact before editing any other symbol whose callers have not been enumerated. [MCP instructions](../internal/mcp/server.go) explicitly make it optional for local body-only edits. The recorded Codex `AGENTS.md` contains the blanket obligation. Current `main` still contains both instructions.

The Gson pr3067 issue names the exact one-expression fix. Claude used one search, edited, and tested. Codex performed lookup, impact, search, then a read containing both Maven files and a test window: that read alone returned 17,424 bytes. In urllib3, two impact responses returned 37,860 bytes before further discovery. The latter task was genuinely more complex; do not assume all that context was dispensable.

**Implementation:**

1. Keep the already shipped task trigger: rename, signature, override, public contract, or an explicit impact question starts with impact.
2. Replace the blanket obligation with one consistent rule: local implementation edits may proceed from sufficient source; call impact when the proposed change requires reasoning about callers/contracts. Do not require enumeration merely because a body will change.
3. Generate MCP instructions and Prism-owned blocks for supported hosts from one policy definition. Host-specific tool names and formatting can differ; the obligations must agree.
4. Upgrade only Prism-owned installation blocks. Preserve user-authored rules; report conflicts with them. Add a local installation diagnostic reporting effective binary/hash, root, steering revision, and connection. Do not put this diagnostic into every model request.

**Why this is new:** it repairs a demonstrable contradiction. It preserves the successful task-trigger experiment and removes a remnant of the failed forced-graph policy. It does not introduce another routing hook or tool-residency experiment.

**Acceptance:** generated instructions agree across hosts; install/update preserves user text; local-body fixtures contain no mandatory impact obligation; contract fixtures retain it. Later paid checks must show preserved fixes and fewer unnecessary impact calls without adding compensating discovery. Do not claim a saving from deleting a sentence alone.

## 2. P0 — Derive impact claims from one consistent set of evidence

**Confirmed transcript defects, with matching mechanisms still on `main`.** In `wide__chi__Routes_Match`:

1. The `Routes.Match` answer already includes `Mux.Match` and its callers, but recommends querying `Mux.Match` as a “larger” interface/base anchor. Codex follows that instruction in its next call. [wideranchor.go](../internal/mcp/wideranchor.go) compares counts of heterogeneous sites, then describes the winning candidate as the contract anchor without establishing that role.
2. The second answer lists `Routes.Match` in `supers`, but also lists it under declarations “not in this change.” [editsites.go](../internal/mcp/editsites.go) and [othermentions.go](../internal/mcp/othermentions.go) build their declaration membership from declarations/family, omitting supers. A related contract is not evidence of an unrelated namesake.
3. That answer says `TEST-ONLY API` and “No production code calls it,” while its own caller list includes production `GetHead` and `SupressNotFound`. [impactplus.go](../internal/mcp/impactplus.go) counts direct inbound edges and same-name overloads, then makes a broader API-use claim; interface dispatch is missing from that inference.

These are sufficient reasons to repair correctness. The trace shows the first unnecessary follow-up immediately after the redirect, but no dollar saving can be assigned to it without a rerun.

**Implementation:**

1. Normalize evidence once into symbol/occurrence identities with explicit roles: declaration, implementation, related super-contract, caller/reference, unrelated namesake, unresolved. Reuse that classification for the edit inventory, mention partition, counts, and hints.
2. Suggest a wider anchor only when its relationship and genuinely additional relevant sites justify it. Compare deduplicated identities/positions, not raw list lengths. Never redirect an interface to an implementation merely because the implementation's result counts supers or declaring types differently. Preserve legitimate narrow-implementation → base-contract guidance.
3. Include relevant super-contract source locations in the related-contract inventory. Do not automatically say every super must be edited: the requested change can be implementation-only. Adjust “every occurrence this change touches” where it overstates what the query establishes.
4. Base a test-only API claim on the family/dispatch evidence it purports to describe. Otherwise use the narrower truthful fact “direct indexed callers are tests” or omit the claim. Preserve useful overloaded-constructor diagnostics.
5. Assert consistency before rendering: no occurrence classified both related and unrelated; no whole-API test-only claim alongside production dispatch; no completeness claim stronger than the actual coverage/freshness evidence. Extend existing degraded/stale checks rather than adding a generic coverage block.

**Acceptance:** replay both Chi queries on current `main`, then on the fix; the wrong redirect and contradictory labels disappear while every valid occurrence remains. Add fixtures for Go embedded interfaces, Java/C# overrides and overloads, TS interface/function-valued members, Python/PHP inheritance, ambiguous names, and incomplete compiler analysis. Use existing language oracles for coverage; do not infer dynamic completeness from a compiler flag. Keep the existing answer budget and useful `editSites` layout.

**Why this is new:** previous mention/coverage experiments changed what extra text was shown. This change fixes conflicting conclusions from evidence already in the answer. It does not promise savings from another “trust this answer” instruction.

## 3. P1 — Provide an exact file-inventory path and precise recovery

**Observed usability gap.** In the partial Codex Zod cell, `search(terms:[""], glob:"packages/treeshake/*.ts", files_only:true)` was correctly rejected under today's contract. The agent retried with `terms:["import"]`. That answers “files containing import,” which is not an exact inventory of files matching the glob. It can miss empty files and files without imports.

Claude also invented `op:"grep"` in Gson pr3098 and recovered after rejection. This is evidence for testing real malformed calls across hosts, not for weakening the schema generally.

**Implementation:**

1. Add an explicitly documented file-enumeration case to the existing search operation: `files_only:true` plus an explicit `glob` and no content terms. Do not add a seventh operation or silently reinterpret ordinary empty searches.
2. Enumerate within the configured project boundary using the established ignore policy. Return deterministic paths, counts, and honest truncation. Include matching empty files; do not require a graph index for filename enumeration.
3. Decide the legacy empty-string case explicitly: normalize it only under that exact inventory shape, or return the precise valid inventory call. Do not substitute an invented content term.
4. For unsupported operation names, give a valid retry only when the supplied arguments make the intended operation unambiguous. Preserve strict validation for genuinely ambiguous requests and retain the full per-field schema guidance that passed earlier tests.

**Acceptance:** offline fixtures cover empty files, non-importing files, globs, ignored paths, symlink/root boundaries, caps, and normal content-search semantics. Replay both hosts' malformed calls. This changes the public search contract even if it adds no field, so it needs the standing paid call-shape gate before shipping.

**Priority limit:** the dataset contains one such Codex inventory failure. Treat this as a small correctness/ergonomics improvement; it cannot explain or erase the overall cost premium.

## 4. P1 — Give agents a reliable execution environment outside Prism

**Owner: host integration / research harness.** Apply equally to native and Prism arms. This can reduce the cost of using either arm; it must not be credited as a Prism retrieval improvement.

Today's transcripts show specific preventable friction:

- Jackson: Maven's configured cache was outside the writable sandbox; a retry used a writable cache. Mockito then failed to attach, requiring another run with the agent loaded at JVM startup.
- Gson: repeated JShell attempts failed before a small Java check succeeded; both arms incurred this kind of work.
- Hono: `NO_COLOR` affected tests, and runtime server tests timed out/hung. Source tests passed separately. Broader testing was not automatically useless.
- Zod: node/runtime selection, dependency links, and test-project configuration drove many reads and commands after the rename.

**Implementation:**

1. Extend the existing environment preparation with checks for these actual failure modes: writable dependency caches, appropriate JVM/test-runner setup, runtime versions, environmental test switches, and allowed local networking. Use ordinary clean baseline runs; expose no hidden-test or reference-patch information.
2. Produce a short host-owned environment record containing measured facts and source paths: checkout identity, resolved runtime/package origin, available test targets, and relevant baseline failures. Keep it valid only for its checkout/configuration. Do not attach all build files to every task.
3. Have the host's process runner wait for completion or a substantive status event. Avoid model requests whose only purpose is repeatedly reading an unchanged test log. Keep final status, elapsed time, timeout, and relevant output available; preserve meaningful diagnostics and requested progress updates.
4. Verify this behavior on each supported CLI version. Do not assume Claude hooks exist in Codex, and do not add a Prism execution operation to compensate.

**Acceptance:** the same unmodified checkout can execute its selected baseline checks in both arms; changed configuration invalidates the environment record; long jobs produce a result without repeated polling-only model steps. Record validation coverage and pre-existing failures. A cheaper run that silently skips required validation fails the gate.

The earlier environment fixes already addressed missing packages and stale seeds. This work targets newly observed sandbox/runtime problems. “Polling” here means shell/process-status work, not repeated Prism queries. The older Click 21-poll story is a separate flawed run and is not used to assign today's premium.

## 5. P1 — Extend the existing gate to catch cross-agent contract failures

**Owner: Prism regression suite plus research harness.** This is an extension of the earlier three-arm gate, not a replacement accounting project.

**New audit finding:** “no cell errors” in a summary does not mean every tool call succeeded. Claude's issue transcript contains an invalid Prism `grep` operation. Its wide transcripts contain recoverable ambiguity errors. Codex emits a skills-context-budget diagnostic in all 27 started cells, although the runner did not classify those notices as cell failures. These categories must be explicit before comparing stop behavior; they are not interchangeable with a failing project test.

**Implementation and sequence:**

1. Turn today's recorded argument/result pairs into a permanent regression corpus. Deduplicate streamed records by message/tool-call ID. Cover all six operations, valid and invalid shapes, and adapters for each host's event format. Preserve raw errors, recoveries, final scorer outcome, and spend even when scoring fails.
2. Extend existing replay/fuzz tests with semantic invariants from item 2. Also exercise file movement, edits above an occurrence, broken intermediate source, compiler unavailable/restored, and multiple related roots. Compare incremental and fresh results where they should agree; explicitly test the documented last-good-analysis limit during broken builds. Existing fuzzing/oracles remain in place.
3. Verify installed configuration before spending: binary SHA, host version, exact selected model/effort, generated instructions, exposed tools, hooks, dependencies, scorer version, and cache protocol. Archive this effective configuration with the result. The wrong-binary run and the later corrected run must never be pooled.
4. Close confirmed correctness issues offline first. Then present a concrete candidate and cost ceiling for explicit per-run approval. Compare **native / current Prism / candidate Prism**, changing one hypothesis at a time. Host-environment changes get their own symmetric native/Prism comparison.
5. Report narrow bugs, wide changes, and long sessions separately. Within each host, use seeded arm order, matched tasks and environments, first-input startup separately, and total spend including recovery. Keep Codex's default-cache protocol separate until another mode is actually verified. A common-price replay is diagnostic, not the observed bill.
6. Preserve the stop rule: stop on a resolve loss, any cell error, or task-cost ratio above 1.25x after at least ten complete pairs; never stop on token ratios alone. Define and record cell failures, tool errors, expected ambiguity, host diagnostics, and ordinary test failures before a run. Do not retroactively reclassify an error to continue; pause for the user's ruling when the agreed rule is triggered or unclear.
7. Replicate a promising result within the approved budget. Require no unresolved correctness regression, complete cost accounting, and a repeatable task-cost improvement. A correctness fix may ship with neutral cost, labelled honestly. An uncertain speed/cost effect is not a saving.

The all-host goal needs two layers: deterministic answer contracts across hosts/languages, and paid behavioral checks for the hosts actually claimed. Today's Claude and Codex runs do not establish Cursor, Gemini, OpenCode, or every language's cost behavior.

## Coverage of narrow, wide, and less common work

| Use case | What should improve | What remains unproved |
|---|---|---|
| Narrow local bug | Item 1 avoids mandatory contract enumeration; item 2 removes misleading caller-use signals. | Whether each model actually asks fewer questions without missing behavioral effects. |
| Wide rename/signature/override | Item 2 retains exact occurrence lists and correct contract relationships. | Net cost on each host; do not suppress legitimate validation. |
| Config, docs, unindexed files | Item 3 supplies an exact filename inventory without requiring symbols. | A graph cannot prove references in an unsupported language. |
| Python/PHP/dynamic code | Truthful roles and incomplete coverage; targeted tests where static evidence stops. | Reflection, generated strings, and runtime dispatch remain limits. |
| Long sessions and changing code | Item 5 checks invalidation and intermediate states; existing force-read recovery remains available. | Compaction savings; none were demonstrated in earlier sessions. |
| Multiple repositories | Retain explicit root disclosure and per-project connections/CLI use; item 1 diagnoses the effective root. | One indexed repository is not automatically readable through another root's server. |
| Every supported agent | Shared semantic policy, adapter fixtures, recorded effective configuration, host-owned execution. | Universal cost improvement cannot be inferred from two agents. |

## Status

Items 1 and 2 are implemented on `main`, not yet released. Item 1: one impact policy (`mcp.ImpactRequiredFor`, `mcp.ImpactOptionalFor`) builds the installed steering block and both MCP instruction strings; refresh replaces only the Prism-owned block and keeps user text. Item 2: the three Chi defects were reproduced on the task's base commit and fixed (wider-anchor direction and identity check, supers as related contracts, test-only claim withheld when production callers reach the API through a listed super); callers and editSites are unchanged, and a chi-like fixture with a contradiction checker fails on the unfixed source. Items 3 to 5 are not started. No paid run was made.

## Order of work

1. Implement and validate items 1 and 2 first. These are the strongest findings and need no paid run to establish the defects.
2. Add the corresponding transcript fixtures and error/configuration capture from item 5.
3. Implement item 3 as a small separately gated contract change; repair the demonstrated host failures from item 4 independently.
4. Seek approval only after presenting the concrete candidate, offline results, exact protocol, and spending limit. No benchmark is started by this plan.

The working hypothesis is that Prism can cost less by **answering the necessary question correctly enough to finish the next decision**, while avoiding instructions and claims that create another question. The successful exact-site work supports that direction. Generic compression, larger context bundles, forced graph use, and cheaper substitute models do not meet the evidence or quality bar here.

## Evidence and limits of this review

- Recomputed 32 Claude wide pairs, 41 Claude issue pairs, and all 13 complete Codex mixed pairs; parsed their recorded tool calls/errors, and inspected the partial Codex Zod cell.
- Read detailed sequences for representative wins, losses, local bugs, wide changes, and validation-heavy tasks. Read the prior plan and experiment notes for the failed and successful levers above. Earlier experiments were not rerun.
- Checked the current source paths behind the policy conflict, anchor hint, edit-site membership, mention classification, test-only signal, and argument rejection. The Chi contradictions are demonstrated in the pinned run; replay on the current build is the first implementation acceptance step.
- Audit rates reproduce the recorded runner's estimates. They are not subscription invoices or a live provider-price comparison. Model requests, tool calls, visible turns, token counts, and billed dollars are different quantities.
- Only planning documents and the read-only audit artifacts were added in this review. No product code, model setting, or running benchmark was changed.
