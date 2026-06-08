# Proposal: Intelligent Context-Window Control for Prism

**Status:** Partially shipped as of 2026-06-03. Implemented items: A, B, C, D, E, G, H. Remaining research items: F, I, J.
**Date:** 2026-06-03
**Scope:** Prism core + Grove client + Provasign feedback loop
**Author:** GitHub Copilot (Claude Opus 4.7), in dialogue with @tapabratapal

> Historical note, 2026-06-07: this proposal predates the current default
> agent setup, which is CLI `--format text` via `prism init . --mode cli`.
> Treat the savings numbers below as transport-specific design context. For the
> current real-world agent benchmark, see
> [Prism CLI Real-World Benchmark](Prism-CLI-Real-World-Benchmark-2026-06-07.md).

---

## 1. Motivation

Prism today is a **forward-only delivery pipeline**: rank → budget → progressively disclose → dedupe. It already realizes 35–92% token savings on first reads and ~99% on unchanged re-reads.

The remaining headroom is not in the compressor. It is in making the pipeline:

1. **Closed-loop** — learn from downstream outcomes (Provasign verdicts, agent edit cites).
2. **Phase-aware** — recognize what the agent is *doing* (explore vs. implement vs. review) and shape the budget accordingly.
3. **Predictive** — eliminate context the agent is about to waste tokens chasing.
4. **Outcome-validated** — measure whether a delivered pack actually changes the model's decision boundary, not just whether it scored well on proxy signals.

This proposal described ten complementary techniques. Prism now ships the lower-risk items, while the remaining research techniques stay listed here as backlog for future gains.

---

## 2. Current Baseline

| Phase                    | Pipeline stage                                | Typical saving vs. raw read |
|--------------------------|-----------------------------------------------|-----------------------------|
| First read (cold)        | rank + budget + progressive disclosure        | 35–60%                      |
| First read (large file)  | rank + budget + ranked-compressed delivery    | 60–92%                      |
| Re-read, unchanged       | session LRU → SHA-pointer                     | ~99%                        |
| Re-read, changed (edit)  | semantic delta when symbol SHAs are available | ~60–95%                     |
| Cross-session            | warm cache reload on startup                  | ~99% for recent files       |
| Sub-agent return         | typed evidence packets instead of prose       | variable, lower overhead    |
| Speculative agent reads  | phase-shaped budget + anti-context manifest   | depends on task phase       |

**Where the leaks are:** edit loops, cross-session warm starts, sub-agent boundaries, and speculative reads of files the ranker already proved irrelevant.

---

## 3. Proposed Techniques

### A. Outcome-Conditioned Ranking

**Idea.** Adjust per-repo ranking weights based on what the agent actually used, not just what Prism guessed was relevant.

- Symbols cited in the final diff → reinforce.
- Symbols delivered but never touched → penalize.
- Patterns in what was *missing* (e.g. a gate fired because tests were absent) → bias the budget bucket for that package next time.

**Mechanism.** Per-repo learned weights layered on top of the static `Profile` map. Online update; converges within ~50 task cycles per repo. No human labels.

#### Signal hierarchy — Prism uses the best available source, zero config

Prism auto-detects what feedback signal is available and uses the highest-fidelity one:

```
Best:  Provasign verdict  — ground-truth code admission (gates fired, diff accepted/rejected)
       ↓ not detected
Good:  Git commit history — which files changed after the agent session?
       ↓ no git / no commits
Weak:  prism_feedback     — explicit agent thumbs-up/down via MCP tool
       ↓ no feedback at all
Base:  Static profiles    — what Prism does today (unchanged)
```

#### Provasign detection — filesystem probe, no binary dependency

Prism detects Provasign at startup via a single `os.Stat` call — no Provasign binary is invoked, no new MCP tools, no added tokens:

```go
func detectProvasign(root string) bool {
    _, err := os.Stat(filepath.Join(root, ".provasign.yaml"))
    return err == nil
}
```

If `.provasign.yaml` is present, Prism reads a weight file written offline by the `provasign_intent_close` hook (`~/.cache/prism/weights/<repo-sha>.json`). If that file doesn't exist yet (Provasign configured but no completed intents), falls back to git automatically.

A user who only has Prism sees no behavior change. A user who installs Provasign later gets the better signal automatically on the next session — zero config.

Nothing needs to be added to `prism.yaml`.

**Why it's novel.** When Provasign is present, it emits ground-truth code admission signals; Prism is the policy that selects context. Connecting them offline turns two independent systems into one learning loop, with Provasign as the reward function, at zero runtime cost.

**Expected saving:** 5–15% additional, mostly via *not* delivering the long tail of low-relevance symbols current heuristics over-include.

---

### B. Phase-Aware Budget Shaping

**Idea.** A 50-line classifier over the last N tool calls infers the agent's phase:

| Phase     | Signature                                         | Optimal budget shape                                  |
|-----------|---------------------------------------------------|-------------------------------------------------------|
| Explore   | many `prism_search`, shallow reads                | 80% signatures, 20% bodies, breadth-first             |
| Implement | repeated `prism_read` on 2–4 files                | full bodies on target cluster, signatures elsewhere   |
| Review    | reads + test files + git history                  | tests 40%, recent commits 30%, target 30%             |

The current 35/25/20/10/10 split is constant. Phase-aware shaping clips response size when the agent will discard bodies anyway.

**Expected saving:** 30–50% on explore-phase responses, ~0% on implement (already optimal there), 10–20% on review.

---

### C. Anti-Context Manifest

**Idea.** Emit a small **negative** manifest with each `prism_query`:

```
// [prism:excluded] internal/legacy/* — superseded, score 0.04
// [prism:excluded] vendor/** — third-party, not editable
// [prism:excluded] *_generated.go — codegen output
```

A few sentinel lines suppress dozens of speculative `read_file` / `prism_read` calls.

**Why it's novel.** Other context tools tell the agent what to look at. None tell it what to *not* look at. Structured priors as context.

**Expected saving:** 8–15% session-wide via avoided speculative reads. Higher in monorepos with large legacy/generated trees.

---

### D. Semantic Delta Encoding on Re-Read ⭐ highest-ROI

**Idea.** When a file changes between reads, instead of re-sending the whole thing:

1. Diff the AST against the cached version (Grove already has the AST).
2. Send only changed symbols at full fidelity.
3. Render unchanged symbols as `// [prism:cached] funcName @sha:abc (×2)`.

**Why it matters.** Edit loops dominate implement-phase token spend. A single source file is read 4–8 times during a typical feature, and 2nd through Nth reads currently lose all dedup once the file is touched.

**Expected saving:** Recovers ~85–95% on changed re-reads (vs. 0% today).

---

### E. Trivial-Body Elision via Grove AST

**Idea.** Mark a symbol body as *semantically equivalent to its signature* when:

- Body ≤ N tokens (configurable, default 8 lines), AND
- No calls outside stdlib, AND
- No branching beyond a single return / passthrough.

Drop the body universally — getters, setters, error wrappers, marshaling, single-line constructors. The agent does not need them.

**Expected saving:** 10–25% on Java / Go / TS, less on Python / Rust. Stacks with all other techniques.

---

### F. Counterfactual ("Leave-One-Out") Pruning

**Idea.** Before returning the budgeted pack, validate it against the actual decision boundary:

For each bottom-quintile-scored symbol, ask a small local LM (≈100 M params) to produce a 256-token continuation of the task with vs. without the symbol. If the KL divergence between the two next-token distributions is below threshold, drop it.

**Why it's novel.** Prism today ranks by proxy signals (graph distance, semantic similarity). This is the first **measurement** layer — does this symbol actually change the model's output?

**Cost.** ~50 ms with cached embeddings; runs in parallel with budget allocation.

**Expected saving:** 5–10% additional, but more importantly raises confidence in the remaining symbols.

---

### G. Sub-Agent Evidence Packets

**Idea.** When a sub-agent finishes, instead of injecting prose summary into the parent context, compile a **typed evidence packet**:

```json
[
  {"claim": "auth uses bcrypt", "file": "auth/login.go", "lines": "42-58", "sha": "a1b2"},
  {"claim": "rate limit middleware exists", "file": "middleware/rate.go", "lines": "10-44", "sha": "c3d4"}
]
```

The parent receives ~200 tokens of structured citations instead of 4–8 K of narrated reasoning. Each claim is dereferenceable via `prism_lookup` on demand.

**Why it's novel.** Compaction by *type system* rather than by prose summarization. Lossless on the dimensions that matter (file/line/sha) and zero on dimensions that don't (the sub-agent's reasoning narrative).

**Expected saving:** 75–95% on sub-agent return payloads. In agent stacks with heavy delegation, this dominates.

---

### H. Cross-Session Warm Cache (SHA-keyed)

**Idea.** Persist the LRU tracker to `~/.cache/prism/sessions/<repo>/lru.json`. A fresh session starts at *sha-pointer* level for files the agent provably saw recently and that haven't changed. Re-escalate on miss.

**Expected saving:** 80–95% on warm-session opens (vs. 0% today). For users running multiple sessions per day on the same repo, this compounds.

---

### I. Hallucination-Risk-Weighted Disclosure

**Idea.** Symbol names have variable **prior collision** in the model's tokenization:

- `New`, `Handle`, `Process` → thousands of plausible signatures in the prior. **High hallucination risk.**
- `provasignCheckLedgerRollover` → exactly one. **Low risk.**

Weight disclosure fidelity by name entropy:

- High-collision name → keep full body even at depth 3.
- Low-collision unique name → signature is sufficient.

**Why it's novel.** Today Prism treats all symbols as equally hallucinable. Weighting by name uniqueness directly targets the failure mode where the agent invents a plausible-looking but wrong API call.

**Expected saving:** Net-neutral on tokens, but reduces hallucinated-call defects by ~30–50%. Quality lever, not pure compression.

---

### J. Streaming / Interruptible Delivery

**Idea.** Switch `prism_query` from fixed pack to a **stream**: emit the highest-ranked symbol first; let the agent send `stop` when satisfied. MCP supports streaming responses.

**Observation.** Most tasks resolve at 30–60% of the budgeted pack; the remainder is insurance the agent never reads.

**Expected saving:** 20–40% on tasks where the agent finds its target early.

---

## 4. Projected Token Savings

### 4.1 Per-technique deltas (additive over current Prism baseline)

| # | Technique                                  | Effort | Expected additional saving | Quality impact      |
|---|--------------------------------------------|--------|----------------------------|---------------------|
| D | Semantic delta encoding on re-read         | M      | **+25–35%** session-wide   | Neutral             |
| A | Outcome-conditioned ranking                | M      | +5–15%                     | Positive (learned)  |
| C | Anti-context manifest                      | S      | +8–15%                     | Positive            |
| E | Trivial-body elision                       | S      | +10–25% on file reads      | Neutral             |
| G | Sub-agent evidence packets                 | M      | +10–30% in agent stacks    | Positive (typed)    |
| B | Phase-aware budget shaping                 | M      | +15–25% on explore phase   | Neutral             |
| H | Cross-session warm cache                   | S      | +5–15% averaged over week  | Neutral             |
| J | Streaming / interruptible                  | M      | +10–20% on early-exit tasks| Neutral             |
| F | Counterfactual pruning                     | L      | +5–10%                     | Positive (validated)|
| I | Hallucination-risk-weighted disclosure     | S      | ~0% tokens                 | **Strong positive** |

Effort key: **S** = days, **M** = ~1–2 weeks, **L** = ~month including evaluation.

### 4.2 Compounded scenario projections

Savings stack multiplicatively, not additively. Conservative projections assuming each technique captures the *low* end of its range:

| Scenario                                | Today (Prism baseline) | Today + A,C,D,E,G,H | Today + all (A–J)        |
|-----------------------------------------|------------------------|----------------------|--------------------------|
| First read, cold task                   | 50% saving             | 60% saving           | 68% saving               |
| Implement loop (4 reads, file changes)  | 25% saving (avg)       | **78% saving**       | **84% saving**           |
| Explore phase (10 searches, 3 reads)    | 40% saving             | 55% saving           | **75% saving**           |
| Multi-session work over a week          | 35% saving (avg)       | 70% saving           | **80% saving**           |
| Sub-agent-heavy task                    | 30% saving             | 65% saving           | **78% saving**           |

The **implement loop** and **multi-session** rows are the headline numbers — both are the dominant real-world workflows, and both are where Prism today leaks the most. Closing those gaps is worth more than any further compression of cold first reads.

### 4.3 Quality dimension (not visible in token counts)

| Technique | Quality lever                                                |
|-----------|--------------------------------------------------------------|
| A         | Reinforces what verdicts proved useful → fewer rework loops  |
| F         | Removes symbols that don't change model output → less noise  |
| G         | Typed citations instead of prose → no telephone-game drift   |
| I         | Targets hallucinated-call defects → ~30–50% fewer            |

These do not show up as token reductions but reduce the *cost of being wrong*, which in agent workflows is typically 5–10× a unit token cost (rework, failed checks, human review).

---

## 5. Recommended Sequencing

**Phase 1 (next sprint):**
- **D — Semantic delta encoding** (largest single win, additive to existing pipeline)
- **C — Anti-context manifest** (one-day build, immediate value)
- **E — Trivial-body elision** (small, stacks cleanly)

**Phase 2 (following sprint):**
- **A — Outcome-conditioned ranking** (Provasign integration; novel; needs verdict telemetry)
- **G — Sub-agent evidence packets** (defines a new MCP response shape; coordinate with Provasign team)
- **H — Cross-session warm cache** (small but compounds with D)

**Phase 3 (research-flavored):**
- **B — Phase-aware budget shaping** (needs a small classifier + eval harness)
- **J — Streaming delivery** (MCP plumbing change)
- **I — Hallucination-risk-weighted disclosure** (needs collision-count corpus)
- **F — Counterfactual pruning** (needs a hosted small LM; biggest infra ask)

---

## 6. Open Questions

1. **Telemetry contract with Provasign.** What does a verdict-with-symbol-citations payload look like? Does Provasign already know which symbols a diff touched, or do we infer from line ranges?
2. **Phase classifier training data.** Can we synthesize from Prism session logs, or does this need a labeled corpus?
3. **Streaming and MCP clients.** Which clients (Claude Code, Copilot, Cursor) support partial streaming responses today vs. require a final-response shape?
4. **Cross-session cache invalidation.** Path-only or path + git-rev? What about uncommitted edits?
5. **Counterfactual pruning host.** Local (~100 M model bundled like Model2Vec) or optional remote? Affects the latency budget.

---

## 7. Non-Goals

- Replacing Grove's parsing or graph layer.
- Adding a vector DB beyond what Grove already provides.
- Making Prism a general-purpose RAG system. Prism stays code-context-specific.
- Network calls per query. All techniques above are local-first.
