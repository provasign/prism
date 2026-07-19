# Prism, Grove, and Mason: Frontier Open Source Assessment

Date: 2026-07-18

## Scope and method

This assessment reviews the current local `main` branches of Prism, Grove, and
Mason, plus the separate research repository used to support their public
claims. It considers the code, architecture, tests, CI, releases, installation,
documentation, benchmark design, security posture, public project readiness,
positioning, and current competitors.

The central question is not whether the software is interesting. It is. The
question is whether these repositories can become trusted, widely adopted open
source projects, and whether Prism can become a default tool for developers.

"Frontier open source" is used here to mean all of the following:

- A capability that is materially better or meaningfully different from the
  current alternatives.
- Evidence that is independently reproducible and broader than a demo.
- Product quality that survives ordinary installation, upgrades, failures,
  large repositories, and untrusted repositories.
- Public APIs and compatibility promises that other projects can depend on.
- Security, release, governance, and contribution practices that earn trust.
- A real community and distribution footprint, not only a technically strong
  repository.

The review used Prism itself for repository-wide context delivery. It also ran
the available test suites and inspected the independent benchmark harness. That
process exposed product issues that are included below.

## Executive verdict

### The short answer

**Prism has the potential to become a default semantic safety layer for AI-assisted
development. It does not currently have a credible path to becoming a default
tool for all developers under the positioning "token-optimized context delivery."
That positioning is both too narrow and too easy for larger platforms to absorb.**

The actual product is broader and more defensible than its headline. Prism and
Grove can deterministically answer complete change questions that agents and text
search handle unreliably: override-aware impact, interface closure, rename edits,
test selection, and untested surface. That is the wedge.

The recommended category is:

> **Semantic change intelligence for coding agents**

The recommended product promise is:

> **Prism gives any coding agent the complete, evidence-backed change set before
> it edits code.**

This moves the value from "save tokens" to "prevent incomplete changes." Token
and turn reduction remain supporting proof, not the product category.

### Portfolio decision

| Project | Recommended role | Frontier potential | Current decision |
|---|---|---:|---|
| **Prism** | User-facing semantic safety layer for every coding agent and CI workflow | High | Make this the primary product and adoption funnel |
| **Grove** | Measured local code-intelligence engine and embeddable SDK | High | Make this the technical foundation and credibility project |
| **Mason** | Reference agent proving harness-enforced use of Prism/Grove | Medium | Keep second-line until general coding lift is reproducible |

Do not market three equal top-level products. That splits attention, search
authority, contributors, docs, and trust. Market one stack:

```text
Prism              default agent integration and semantic change safety
  powered by Grove measured local code graph and SDK
  proven in Mason  reference harness and experimental coding agent
```

Grove should still be an excellent independent open source project for engine
integrators. It should not compete with Prism for the same end user.

### Current readiness score

The following scores are directional, not vanity metrics. A 10 means credible
for a category-leading public project today.

| Dimension | Prism | Grove | Mason |
|---|---:|---:|---:|
| Technical differentiation | 9 | 8 | 7 |
| Core implementation maturity | 7 | 7 | 7 |
| Evidence quality | 7 | 8 | 4 |
| End-user product clarity | 5 | 4 | 6 |
| Reliability and operability | 5 | 6 | 6 |
| Public API stability | 4 | 5 | 3 |
| Security and supply chain | 3 | 3 | 3 |
| Open source governance | 2 | 2 | 2 |
| Distribution and discoverability | 3 | 2 | 3 |
| Demonstrated external adoption | 1 | 1 | 1 |

**Conclusion:** Prism and Grove are frontier technical candidates, not yet
frontier open source projects. Mason is a serious prototype/reference agent,
but its broad agent claim is not yet supported at the same standard as the
change-impact engine claim.

## What is genuinely differentiated

### 1. Task-altitude graph operations

The strongest idea in the portfolio is not a code graph. Code graphs, semantic
navigation, repository maps, and context retrieval already exist. The strongest
idea is exposing a graph as deterministic, whole-task operations whose output
can be independently scored:

- `change_impact`
- `rename_plan`
- `missing_implementations`
- `untested_surface`
- `affected`
- `dead_code`

This is better than asking an agent to orchestrate `find references`, `find
implementations`, search, and file reads. It also makes cheap-model performance
less dependent on tool-use skill.

The research repository supports this narrow claim unusually well. The
change-impact studies use independent language oracles, preserve raw runs, score
the engine without an LLM, disclose contaminated experiments, and document
negative results. That rigor is a real asset.

### 2. Correctness as an engine property

Prism's best promise is that completeness can be owned by deterministic code,
not hoped for from a model. Mason strengthens this by keeping large graph
payloads away from the model and applying rename plans in the harness. This is a
coherent architectural thesis:

```text
engine computes -> harness enforces -> model reasons -> compiler/tests verify
```

This is much stronger than "better retrieval."

### 3. Grove's measurement discipline

Grove contains a serious evaluation system:

- Pinned real repositories.
- Typed-toolchain or runtime oracles appropriate to each language.
- Precision, recall, F1, and universe-match floors.
- False-positive attribution by evidence source and resolver reason.
- Separate test-edge and impact evaluation.
- Honest publication of uneven language quality.

Very few early open source code-intelligence projects publish this level of
language-by-language evidence. This can become Grove's identity.

### 4. Local, agent-neutral operation

The stack is local, works without a hosted service, embeds the graph engine, and
can reach agents through MCP, CLI, or a Go API. That is strategically useful as
coding-agent vendors increasingly own their own retrieval stacks. Prism can be
the neutral safety layer that improves all of them.

## The answer on Prism becoming a default

### Default for whom?

"All developers" is too broad. Developers who do not use an AI coding agent do
not need context delivery. Developers making a one-line local change do not need
a graph operation. Runtime-heavy systems may not be representable by a static
graph. Some teams already have compiler-accurate enterprise code intelligence.

A credible default target is:

> **Every developer or agent making a non-trivial change in a supported
> repository should have Prism available automatically.**

That is still a very large market. It also produces an objective default test:
developers should install Prism once, leave it enabled, and trust agents or CI
to invoke it only when the task shape benefits.

### What must be true for default status

Prism becomes a default when all of these are true:

1. Installation takes less than a minute and does not unexpectedly rewrite
   multiple instruction or global configuration files.
2. It starts and refreshes automatically with no daemon management.
3. It tells the user exactly which language capabilities are authoritative,
   degraded, unavailable, or heuristic in the current repository.
4. Its P95 tool latency is low enough that leaving it enabled is cheaper than
   deciding whether to use it.
5. A failed or stale index never causes a false "complete" answer.
6. The top three operations deliver consistent wins on real, held-out changes,
   not only curated signature-change tasks.
7. Every major coding agent can install it from a trusted registry or package
   manager in one action.
8. Releases are signed, reproducible or provenance-attested, and independently
   verifiable.
9. External maintainers can add language support and reproduce every published
   quality number.
10. The project has a visible community, issue process, support policy, and
    compatibility commitment.

Prism currently satisfies the differentiated-operation requirement, but not the
default-product requirements.

## Positioning assessment

### Why the current positioning is weak

"Graph-ranked code context" accurately describes one mechanism but undersells
the product. "Token-optimized context delivery" is worse as a primary category:

- Model context windows and caching improve quickly, weakening a token-only
  value proposition.
- Every agent vendor can claim smarter context and fewer tokens.
- Developers buy correctness, speed, and confidence, not compressed bytes.
- It hides deterministic change operations, which are the strongest moat.
- It invites comparison with repository maps and embedding retrieval, where
  many established tools already compete.

"Persistent long-term memory" is also the wrong headline for Grove. It is broad,
crowded, and suggests learned history rather than a measured structural graph.

### Recommended positioning

#### Prism

**H1:** Complete code-change intelligence for any coding agent.

**Subhead:** Prism finds every declaration, implementation, caller, and covering
test a change touches, then delivers the evidence through MCP or CLI. Local,
deterministic, and measured against independent language oracles.

**Primary proof:** On supported change-impact tasks, Prism reaches its engine
ceiling across model tiers while text-search agents miss sites.

**Supporting proof:** Fewer tool calls and tokens, local execution, multi-agent
support, and edit-ready context.

#### Grove

**H1:** The measured local code graph for developer tools.

**Subhead:** Embed a versioned graph of symbols, calls, type relationships, and
test evidence, with language quality scored against typed and runtime oracles.

#### Mason

**H1:** A reference coding agent with semantic safety enforced by the harness.

Do not currently claim that Mason is generally better than frontier agents. Say
that it demonstrates a specific architecture and performs strongly on the
operations Prism can solve deterministically.

### Category language to own

Use these phrases consistently:

- Semantic change intelligence
- Complete change sets
- Evidence-backed agent changes
- Deterministic code relationships
- Task-level code intelligence
- Agent-neutral semantic safety layer

Use these only as supporting language:

- Token savings
- Context compression
- Persistent memory
- Code RAG
- Graph-ranked retrieval

## Competitive reality

Prism is not entering an empty category.

| Alternative | Existing strength | Where Prism can win |
|---|---|---|
| Sourcegraph | Enterprise-scale search, SCIP navigation, cross-repository intelligence, code graph context | Local zero-service operation, task-shaped complete operations, agent-neutral distribution, open evaluation |
| Serena | Broad LSP/JetBrains semantic retrieval and refactoring, mature MCP packaging, large community | Deterministic closure operations, independent oracle benchmarks, explicit completeness contracts |
| Aider | Mature coding agent, graph-ranked repository map, broad model support, established UX | Precise impact and contract operations rather than context ranking alone |
| Cursor/Copilot/Codex/Claude Code | Owned UX, built-in indexing/search, massive distribution | Neutral layer that makes the same correctness operation available in every host |
| CodeGraph-style MCP projects | Easy local graph setup, semantic search, impact tools | Higher measured precision/recall, task altitude, deterministic edit plans, published evaluation |
| Language servers/SCIP | Compiler-grade definitions and references in supported ecosystems | Uniform cross-language task API, test relationships, agent delivery, degraded-mode behavior |

Sourcegraph already documents mixed keyword, search, and code-graph context,
plus precise cross-repository navigation. Aider already uses a graph-ranked
repository map. Serena already offers symbol retrieval and refactoring through
MCP and had roughly 26K registry/repository users in the observed public index.
The official MCP Registry lists Serena but not Prism. "We have a graph" or "we
reduce context" will not be enough.

Prism should openly benchmark the exact operations competitors expose. The
current CodeGraph comparison is a start. Add Serena/LSP, SCIP, Aider repo-map,
and built-in agent search baselines where a fair adapter exists. Publish cases
where competitors win.

## Prism repository assessment

### Strengths

- Broad deterministic surface beyond retrieval.
- Embedded Grove eliminates a separate daemon and service token.
- CLI, MCP, HTTP, and Go facade provide multiple adoption paths.
- Strong tests around CLI integration, MCP behavior, session tracking, ranking,
  compression, and graph operations.
- The official test suite passed in this review.
- Installation supports macOS, Linux, and Windows binaries.
- Agent configuration generation covers major coding tools.
- The current README is more honest than many AI projects about wrong-tool
  cases and static-analysis limitations.

### P0 product defects observed during this review

1. **Cached reads can refer to context the current conversation does not have.**
   `prism read README.md` returned a cached pointer without delivering the file,
   even though the content was not present in this conversation. Mason's README
   read also returned a cached marker plus only a tail fragment. A persistent
   ledger cannot assume that a new CLI process, new agent turn, or new model
   context still holds a previous delivery.

2. **Repository root resolution was inconsistent.** `prism status .` reported a
   valid Grove index, while `prism query ... . --flags` repeatedly said the repo
   was not indexed and returned no result. Supplying the absolute repository path
   as the final positional argument worked. This makes the primary workflow feel
   non-deterministic.

3. **`prism --version` prints help rather than a version.** Conventional CLI
   behavior matters for package managers, support, bug reports, and scripts.

4. **The public repository metadata must be reconciled with the source.** Public
   GitHub descriptions observed during review still referred to MIT while the
   checked-in license is Apache-2.0. Verify repository descriptions, package
   metadata, website copy, and all release pages.

### Required technical changes

#### Session correctness

- Scope read deduplication to an explicit conversation/session identifier.
- Never emit a pointer unless the client proves the same live context owns the
  referenced delivery.
- Make direct CLI reads full by default; enable dedup only with `--session` or a
  persistent transport identifier.
- Include a recovery flag such as `fresh=true` / `--fresh`.
- Add end-to-end tests across process restart, agent restart, conversation
  compaction, model switch, and ledger expiry.
- Treat accidental under-delivery as a correctness failure, not only a token
  accounting issue.

#### Trust and completeness

- Define `closed` precisely as closed under a named graph policy and capability
  set, not universally complete.
- Include `language`, `analyzer`, `quality_tier`, `index_freshness`,
  `unresolved_count`, and `degraded_reasons` on every task operation.
- Refuse authoritative language for stale or degraded indexes.
- Add `prism doctor` with machine-readable output and remediation.
- Expose an index manifest: files included/excluded, native analyzers used,
  timeouts, parse errors, unsupported constructs, and graph version.
- Make source delivery cite why each file/symbol was included.

#### Retrieval quality

- Build a separate held-out evaluation for `prism_query`: Recall@k for required
  files/symbols, excess-context ratio, edit success, reread rate, and time to
  first correct patch.
- Benchmark broad natural-language tasks without hand-provided perfect anchors.
- Add framework-aware anchors for routes, dependency injection, configuration,
  database schemas, generated clients, and tests.
- Preserve `rg` as the best locator. Do not position Prism search as a universal
  replacement until evidence supports it.

#### Default experience

- Reduce the first-run flow to one command and one restart at most.
- Add a preview/dry-run before modifying agent instruction or global config
  files.
- Prefer a small generated include file over injecting large duplicated
  instruction blocks into many project files.
- Add idempotent `prism uninstall --project` and `prism uninstall --global` with
  exact ownership tracking.
- Automatically maintain freshness inside long-lived MCP sessions.
- Add a quiet status surface and actionable diagnostics instead of requiring
  users to understand Grove storage.
- Make the first successful operation a high-value demo, such as change impact
  on a real symbol in the user's repository.

#### Integration surface

- Publish Prism to the official MCP Registry.
- Ship verified one-click install recipes for Claude Code, Codex, Copilot,
  Cursor, Windsurf, Zed, and JetBrains-based agents.
- Keep the default exposed tool set small. Use task-operation grouping or lazy
  tool discovery rather than spending context on a large schema catalog.
- Provide a stable JSON schema package and versioned protocol for non-Go
  consumers.
- Add a GitHub Action for `prism affected`, change-impact review, and capability
  reporting.
- Add workspace and cross-repository federation. This is required to compete
  for enterprise and monorepo defaults.

### Documentation changes

- Cut the README to a five-minute path: problem, one demo, proof, install,
  limitations, links.
- Move the full CLI reference to versioned docs.
- Lead with missed-change prevention, not token reduction.
- Add a capability matrix per operation and language. A language being parsed
  does not mean every operation is equally accurate.
- Put benchmark version, corpus pin, sample size, trials, confidence interval,
  and raw artifact link next to every headline claim.
- Add a comparison page that includes losses and non-goals.
- Add a privacy page explaining exactly what is stored locally and what content
  an external model provider receives.

## Grove repository assessment

### Strengths

- 32K Go source lines, 56 test files, and a coherent package split across
  parser, native analyzers, graph, storage, MCP, evaluation, and public API.
- Root tests and vet passed; observed statement coverage was 72.7%.
- Strong coverage in parser, index, MCP, embeddings, config, and certification.
- Real-world evaluation across Go, TypeScript, JavaScript, Java, Python, Rust,
  C/C++, C#, and PHP.
- Honest published quality tiers rather than a single blended accuracy number.
- A useful public Go facade in `pkg/grove`.
- Measured indexing on Prometheus, Django, and Grafana-scale repositories.
- Conservative certification behavior and explicit manual-review outcomes.

### P0 issues

1. **The standalone evaluation module is not reproducible at HEAD.** A clean
   `GOWORK=off go test ./...` in `eval/` required module changes. `go mod tidy
   -diff` showed `github.com/provasign/astkit` pinned at v0.4.17 while the Grove
   module requires v0.4.20. The accuracy workflow builds this module, so this
   undermines the project's strongest trust claim.

2. **Quality language is too uniform for uneven graph quality.** Published call
   F1 ranges from 0.94 for Go to 0.63 for PHP, and Flask test-edge evidence finds
   a truly covering test for only about 36% of covered functions. "Compiler
   grade" or universally "type resolved" must be qualified by language,
   operation, and active analyzer.

3. **Documentation has internal count inconsistencies.** The architecture says
   eight edge types while the table lists nine including `overrides`. These
   details matter in an engine API.

4. **Release metadata and license descriptions need reconciliation.** As with
   Prism, observed public descriptions mentioned MIT while current source and
   package metadata are Apache-2.0.

### Required technical changes

#### Make quality a product surface

- Version and publish a machine-readable capability manifest per release.
- Assign operation/language tiers such as `precise`, `measured`, `structural`,
  `heuristic`, and `unsupported`.
- Return edge source, confidence, resolver reason, and unresolved alternatives
  through the public API.
- Add `grove doctor` and `grove explain-edge`.
- Never silently treat a native analyzer timeout as equivalent to a full index.

#### Expand the evaluation program

- Fix and gate the standalone `eval` module before the next release.
- Run evaluation against pull requests and release candidates from a locked
  dependency graph.
- Add at least three independent corpus repositories per major language before
  calling a tier stable.
- Add held-out targets not used to tune resolver logic.
- Gate impact, override closure, interface closure, rename plans, dead code, and
  test selection separately. Calls-edge quality is necessary but not sufficient.
- Add precision/recall confidence intervals and report corpus variance.
- Add mutation tests for graph invariants and fuzzing for parsers, diffs, paths,
  and SQLite migrations.
- Invite external benchmark adapters and publish a stable ground-truth format.

#### Performance and scale

- Replace full in-memory edge rebuilds with per-file or per-component
  incremental edge maintenance.
- Publish CPU, peak RSS, database size, open time, warm query latency, and delta
  latency on Linux, macOS, and Windows.
- Add million-symbol and multi-repository stress targets.
- Support branch/commit snapshots or explain that Grove is working-tree only.
- Add corruption recovery, backup-before-migration, and deterministic reindex
  verification.

#### Extensibility

- Define a stable analyzer/plugin contract so contributors can add a language
  without editing several internal packages.
- Separate extraction, resolution, quality tests, and corpus fixtures into a
  documented language adapter kit.
- Provide SDK examples for Go and a stable CLI/JSON protocol for other
  languages.
- Publish API compatibility policy and a v1 plan.
- Add cross-repository symbol identity and dependency federation.

### Recommended Grove identity

Grove should become the project developers trust when they ask, "How good is
this code graph, exactly?" Its moat is measurement and explicit uncertainty.
Do not obscure that under generic memory language.

## Mason repository assessment

### Strengths

- A real provider abstraction across local, Anthropic, OpenAI, OpenRouter, LM
  Studio, vLLM, and compatible endpoints.
- A substantial terminal UI and session experience.
- Harness-enforced plan mode, permissions, path confinement, cost budgets,
  checkpoints, compaction, redaction, and graph routing.
- Rename-plan application avoids lossy model transcription.
- Provider, agent, TUI, credentials, LSP, MCP, and CLI tests.
- The official race-enabled test suite passed during this review.
- Honest recent research acknowledges noisy agent trials and a bug-fix result
  where Prism source delivery tied plain source rather than winning.

### Why Mason should remain second-line

The coding-agent category has the strongest incumbents, fastest model changes,
largest UX expectations, and highest ongoing integration cost. Mason's current
unique advantage is Prism/Grove enforcement. That advantage should first spread
through Prism integrations rather than requiring users to replace their agent.

Mason's strongest public evidence remains narrow:

- Excellent results on graph-shaped operations that the engine solves.
- A very small product comparison with three task types.
- Noisy general bug-fix experiments where the latest honest result is parity,
  not a consistent win.

This supports "reference agent that proves the architecture," not "top general
coding agent."

### P0 documentation and safety corrections

1. **`model:auto` is overclaimed.** Usage text says it performs measured
   task-tier routing, but `detectModel` selects a sticky or preferred available
   model without inspecting the task. Either implement task routing and test it
   or remove the claim.

2. **ChatGPT OAuth is marked shipped in the roadmap but remains experimental in
   the provider.** It requires `MASON_CHATGPT_BASE` and explicitly says serving
   awaits live validation. Mark it experimental everywhere.

3. **Shale is not literally baked in.** The trail silently becomes a no-op when
   the `shale` binary is absent. Say "optional Shale integration" or embed it.

4. **Review is not yet a complete branch gate.** It caps analysis at 20 touched
   symbols, ignores reindex errors, treats unclassifiable coverage as covered,
   and labels dead code in changed files as "newly unreachable" without a base
   graph comparison. Correct these semantics before promoting `--strict`.

5. **Checkpoints can preserve sensitive untracked content in Git objects.** The
   checkpoint snapshots every non-ignored file with `git add -A` into an
   unreferenced commit. Respect Mason deny patterns and known secret files,
   disclose the behavior, add retention/cleanup, and test that secrets are not
   stored.

### Required work before promotion

- Build a 50-100 task held-out benchmark across bug fixing, implementation,
  tests, refactors, debugging, and repo comprehension.
- Use at least three trials per stochastic cell and publish uncertainty.
- Compare with Aider, Claude Code, Codex, OpenCode, and another local-first
  agent under equivalent model/tool budgets.
- Measure patch success, regressions, human intervention, wall time, cost, tool
  calls, and unsafe actions.
- Add shell sandbox integrations rather than relying only on permission prompts.
- Add an explicit threat model for repository instructions, hooks, MCP servers,
  web fetch, model output, and credential handling.
- Make checkpoint and undo behavior safe for large repos, LFS, submodules,
  worktrees, ignored files, and secrets.
- Make deterministic review compare before/after graph snapshots and analyze the
  complete diff or fail closed.
- Decide whether Mason is a product or a conformance/reference harness. Do not
  let it consume Prism/Grove focus until external demand answers that question.

### Mason promotion gate

Promote Mason alongside Prism only after all are true:

- At least 50 held-out real tasks with reproducible multi-trial results.
- Statistically credible improvement over a same-model baseline on general
  coding, not only graph operations.
- No unresolved P0 checkpoint, permission, redaction, or shell-safety issue.
- A stable plugin/provider API and at least two external contributors.
- Documented compatibility with the leading local and hosted models.
- A clear user segment not already better served by established agents.

Until then, Mason is strategically valuable as Prism's dogfood environment and
as a public proof that task-level graph operations can be enforced.

## Open source readiness gaps common to all three

The repositories have licenses and CI, but they are not yet contributor-ready.
Add the following to Prism and Grove first, then Mason:

| Artifact or practice | Why it matters |
|---|---|
| `CONTRIBUTING.md` | Setup, architecture, test/eval commands, PR process, DCO/CLA decision |
| `SECURITY.md` | Private reporting, supported versions, response targets |
| `CODE_OF_CONDUCT.md` | Community expectations and enforcement |
| `GOVERNANCE.md` | Maintainers, decision process, release authority, succession |
| `SUPPORT.md` | Supported channels and response expectations |
| `CODEOWNERS` | Review ownership, especially releases and security-sensitive code |
| Issue and PR templates | Reproducible bug, language-quality, benchmark, and proposal intake |
| Public roadmap | Outcome-based priorities and non-goals |
| Compatibility policy | CLI, JSON/MCP, database schema, Go API, and deprecation rules |
| Changelog/release notes | User-visible changes, migrations, breaking changes, benchmark version |
| Architecture decisions | Why graph policies and confidence semantics exist |
| Good-first-issue path | Small external contributions that do not require full graph expertise |

### Supply-chain work

- Pin GitHub Actions by commit SHA, not only mutable major tags.
- Add dependency review, CodeQL/static analysis, secret scanning, and automated
  dependency updates.
- Generate SBOMs for every binary release.
- Sign binaries and checksum manifests with Sigstore/cosign.
- Publish SLSA provenance or GitHub artifact attestations.
- Make installers fail closed when checksums/signatures are missing. Prism's
  current installer accepts a release with no checksum manifest.
- Add release smoke tests that install each artifact on a clean OS image and run
  `version`, `doctor`, index, query, and uninstall.
- Separate release permissions from ordinary CI and use protected environments.
- Run OpenSSF Scorecard and pursue the OpenSSF Best Practices badge.

The OpenSSF criteria explicitly expect projects to explain how to obtain,
report issues for, and contribute to the software. These are not administrative
extras for a tool that reads proprietary source code and influences edits.

### Community work

- Turn on Discussions or establish one clear forum.
- Publish a monthly quality report with regressions, wins, unresolved tiers,
  and roadmap decisions.
- Create contributor guides for adding a corpus, oracle, language fixture,
  operation, and agent integration.
- Run public "bring your hardest refactor" evaluations and preserve failures.
- Recruit maintainers for at least Go, TypeScript/JavaScript, Java, Python, and
  release/security ownership.
- Respond publicly to issues with target service levels.
- Avoid rapid version-number churn without stable compatibility commitments.

## Benchmark and evidence strategy

### Preserve what is already good

- Independent oracles.
- Engine-only ceiling measurement.
- Raw run publication.
- Contamination checks.
- Negative-result disclosure.
- Same-task, same-model, same-budget comparisons.

### Fix what is not yet sufficient

- Separate engine quality, retrieval quality, agent realization, and final patch
  quality. Do not blend them into one success claim.
- Stop generalizing signature-change performance to general coding.
- Replace 3/3 headlines with sample sizes, rates, and uncertainty.
- Use held-out corpora and freeze them before implementation work.
- Include adversarial cases: overloads, generated code, DI, reflection,
  monorepos, build tags, macros, dynamic dispatch, partial syntax, and stale
  indexes.
- Add longitudinal regression charts across releases.
- Reproduce results on Linux runners, not only one developer workstation.
- Obtain at least one external replication before using "default" or
  "frontier" language publicly.

### Benchmark suites to build

| Suite | Primary metric | Minimum scope |
|---|---|---|
| Change completeness | Site precision/recall and build success | 100 targets, 5+ languages |
| Query/context retrieval | Required-context Recall@k and excess-context ratio | 100 natural-language tasks |
| Test selection | Safe reduction, missed-failure rate | 20 real repos, mutation-backed |
| Rename application | Clean build/test after deterministic plan | 100 renames, overload and interface heavy |
| Interface evolution | Missing implementation recall | 50 contract changes |
| Dead code | Precision verified by build/tests/runtime | 20 repos |
| Agent coding | Final oracle pass, regression rate, human interventions | 50-100 held-out tasks |
| Scale/reliability | P50/P95 latency, RSS, DB size, freshness | 1K to 1M symbols |

### Claim discipline

Every public number should carry:

- Product and engine versions.
- Corpus and commit pins.
- Task count and selection method.
- Model, temperature, prompt, and tool configuration.
- Trial count.
- Mean/median plus dispersion or confidence interval.
- Exact scorer and oracle.
- Raw run link.
- Known threats to validity.

## Product roadmap

### P0: 0-30 days, make the claims trustworthy

1. Fix Prism session-boundary under-delivery and root/index resolution.
2. Fix Grove `eval` module dependency drift and require a clean eval build.
3. Correct Mason auto-routing, OAuth, Shale, review, and checkpoint claims.
4. Reconcile Apache-2.0 metadata everywhere.
5. Add `SECURITY.md`, `CONTRIBUTING.md`, governance, issue templates, and
   support policies to Prism and Grove.
6. Add `doctor` and capability/degradation output.
7. Sign releases, fail closed in installers, and add SBOM/provenance.
8. Rewrite Prism and Grove homepage copy around semantic change intelligence
   and measured graph quality.
9. Publish Prism in the official MCP Registry.
10. Freeze broad "default" and "compiler-grade" claims until tier semantics are
    exposed in output.

### P1: 31-90 days, establish the wedge

1. Release a stable Prism operation contract and Grove capability manifest.
2. Ship one-click integrations and a GitHub Action.
3. Build held-out evaluations for query retrieval, affected tests, rename, and
   interface evolution.
4. Add three corpora per priority language and an external replication guide.
5. Implement true incremental edge maintenance for large repositories.
6. Add workspace/cross-repository graph federation design and prototype.
7. Create five excellent end-to-end demos based on real OSS changes.
8. Recruit design partners and publish anonymized outcome case studies.
9. Create contribution paths for language adapters and benchmark corpora.
10. Keep Mason focused on conformance and dogfood rather than feature breadth.

### P2: 3-6 months, earn default-on behavior

1. Reach stable quality tiers for Go, TypeScript/JavaScript, Java, and Python.
2. Make agent invocation automatic based on task shape with measured false-call
   and missed-call rates.
3. Add safe cross-repo impact and test selection.
4. Release Grove v1 API candidate and Prism protocol v1.
5. Produce third-party benchmark reproduction and integration case studies.
6. Reach meaningful community health: external PRs, maintainers, issues, and
   integrations.
7. Run OpenSSF Scorecard continuously and achieve a Best Practices badge.
8. Decide Mason promotion from evidence, not portfolio symmetry.

### P3: 6-12 months, expand the category

- IDE-native impact previews and PR annotations.
- Organization graph snapshots with local/private deployment.
- Framework adapters for DI, routes, schemas, and generated APIs.
- Historical change intelligence and ownership signals.
- Standard interchange format for task-level code intelligence.
- Hosted team features only if they do not compromise the local open core.
- Mason as a promoted product only if the promotion gate is met.

## Adoption plan

### Initial users

Prioritize these groups in order:

1. Maintainers of interface-heavy Go, Java, TypeScript, and C# repositories.
2. Developers using local or lower-cost models that need deterministic help.
3. Agent/plugin authors who need an embeddable change-intelligence engine.
4. Large monorepo teams with high refactor and test-selection costs.
5. CI/platform teams seeking explainable affected-test and change-risk signals.

Do not start with "everyone who codes." Win a painful workflow first.

### The first indispensable workflow

Make this the hero:

```text
Before an agent changes a public method, Prism produces the complete change set,
shows confidence and graph capability, applies a reviewable rename plan, and
runs only the tests Grove proves are relevant.
```

One workflow should install, demo, benchmark, and explain the entire stack.

### Distribution checklist

- Official MCP Registry entry.
- Homebrew formula with verified provenance.
- Go install path where technically viable.
- Scoop/WinGet and a Linux package path.
- Container/devcontainer option for reproducible evaluation.
- GitHub Action and reusable workflow.
- Editor marketplace entries only when maintained and version-compatible.
- `prism doctor --json` for support automation.

## Metrics that matter

### Technical trust

- Operation precision/recall by language and tier.
- Stale/degraded answer rate.
- Unresolved-edge rate.
- False `closed` answer count, target zero.
- Index freshness P95.
- Query and operation P50/P95 latency.
- Crash/corruption rate.

### Product value

- Final build/test success delta with Prism enabled.
- Missed-change defects prevented.
- Tool calls and wall time saved.
- Reread rate after `prism_query`.
- Percentage of eligible tasks invoking the correct operation.
- Percentage of installs still active after 7 and 30 days.

### Open source health

- External contributors and maintainers.
- External integrations.
- Issue response and close time.
- Release adoption and upgrade success.
- Independent benchmark reproductions.
- OpenSSF Scorecard and Best Practices status.

Stars are a useful awareness signal but not a quality metric. Current public
traction appears effectively pre-launch, so the next phase should optimize for
successful retained users and external evidence, not release count.

## Alternative paths

### Path A: Semantic safety layer for every agent - recommended

Prism is the distribution, Grove is the engine, Mason is the reference client.
This uses the strongest existing evidence and avoids competing head-on with
full coding-agent vendors.

### Path B: Grove as developer-tool infrastructure

Focus on the Go SDK, CI test selection, impact APIs, graph snapshots, and
language quality. Prism becomes one adapter. This is credible if agent vendors
resist third-party context tools but infrastructure teams adopt the engine.

### Path C: Full coding-agent platform through Mason

This has the largest market and highest risk. Choose it only after broad,
held-out agent benchmarks show a repeatable advantage. Otherwise it will consume
the resources needed to make Prism/Grove irreplaceable.

### Path D: Standard or benchmark first

Turn the oracle corpora and task-operation schemas into an open standard for
change intelligence. This can create influence even before Prism dominates
distribution, and makes Grove's evidence program a community asset.

The recommended sequence is A plus D, with B as the business/SDK fallback.

## Hard risks

1. **Brand collision.** Prism, Grove, and Mason are generic names with crowded
   search results. Another `prism-mcp` already exists. Use consistent qualifiers
   such as "Prism Code Intelligence" and verify package/binary discoverability.

2. **Portfolio fragmentation.** Three public brands plus Astkit, Shale, Fuse,
   and Provasign can look like many incomplete products. Publish one architecture
   and one adoption path.

3. **Benchmark overreach.** The best result is real but narrow. Overstating it
   will damage the evidence advantage.

4. **Heuristic authority.** A result labeled complete while native analysis is
   missing or the language tier is weak is more dangerous than no result.

5. **Single-maintainer trust.** Frontier infrastructure needs review,
   succession, security response, and release separation.

6. **Incumbent absorption.** Agent vendors can add graph retrieval. Prism must
   own operation semantics, measurement, and neutrality, not merely retrieval.

7. **Supply-chain exposure.** A tool installed with `curl | bash`, attached to
   many agents, and reading proprietary source has a high trust requirement.

8. **Large-repo freshness.** Current full edge rebuild and graph rehydration
   costs can make the default experience stale or slow without a long-lived
   process.

## Go/no-go gates

### Prism is ready to call a top choice when

- No session-boundary under-delivery or stale authoritative result remains.
- Four priority languages have published operation-level quality tiers.
- Held-out real-change benchmarks show a meaningful final-outcome lift.
- Install, doctor, query, upgrade, and uninstall pass clean-room tests on three
  operating systems.
- Releases are signed and provenance-attested.
- Official registries and major agent integrations are live.
- At least three external projects or agent vendors integrate Prism.
- Governance and security response are public.

### Grove is ready to call frontier infrastructure when

- The eval system reproduces from a clean checkout.
- Quality manifests are versioned and returned through the API.
- Priority operations, not only call edges, are gated on multiple corpora.
- The public API has a compatibility policy and v1 candidate.
- External contributors can add a corpus or language adapter from documentation.
- Large-repo delta latency and resource use meet published targets.

### Mason is ready to move from second-line when

- It passes the promotion gate in the Mason section.
- Its safety properties have an explicit threat model and adversarial tests.
- General coding outcomes improve beyond same-model baselines with uncertainty
  reported.

## Immediate concrete checklist

### Prism next release

- [x] Fix conversation-scoped cache correctness.
- [x] Fix relative-root query/index behavior.
- [x] Support `--version`.
- [x] Add `doctor` and capability output.
- [x] Fail closed on missing checksums.
- [ ] Correct license/repository metadata.
- [ ] Publish MCP Registry package.
- [x] Replace README headline and first screen.
- [x] Add governance/security/contribution files.
- [x] Add signed artifacts, SBOM, and provenance.

### Grove next release

- [x] Update `eval` Astkit dependency and verify `GOWORK=off` tests/build.
- [x] Fix edge-count documentation.
- [x] Publish operation/language capability tiers.
- [ ] Gate change-impact and test-selection benchmarks.
- [x] Add `doctor`/diagnostic manifest.
- [ ] Correct license/repository metadata.
- [x] Add governance/security/contribution files.
- [x] Add signed artifacts, SBOM, and provenance.

### Mason next release

- [x] Remove or implement task-aware `model:auto` routing.
- [x] Mark ChatGPT OAuth experimental consistently.
- [x] Describe Shale as optional unless embedded.
- [x] Make review fail closed on reindex error and remove incomplete "strict"
      semantics.
- [x] Stop claiming newly unreachable code until base-graph comparison exists.
- [x] Exclude sensitive paths from checkpoints and document retention.
- [x] Publish a threat model.
- [x] Keep public positioning as reference/experimental until benchmark gates
      pass.

### Implementation status after remediation

Repository-local P0 remediation was applied on 2026-07-18:

- Prism delivery-cache state is now process/conversation scoped. Standalone CLI
  and newly started MCP processes cannot inherit a SHA-only delivery from an
  unidentified prior conversation. Relative and symlinked roots canonicalize,
  `--version` works, and `prism doctor` reports engine/index/capability state.
- Prism, Grove, and Mason release workflows now generate SPDX JSON SBOMs and
  GitHub/Sigstore artifact attestations. Prism's shell and PowerShell installers
  reject a missing checksum manifest or missing artifact entry.
- Grove's standalone eval module is aligned to Astkit `v0.4.20`; `GOWORK=off go
  mod tidy -diff` is clean and `GOWORK=off go test ./...` passes. `grove doctor`
  emits a versioned machine-readable manifest with operation and language tiers.
- Mason review now fails on graph refresh errors, evaluates every touched symbol,
  treats unclassifiable coverage as a warning, and labels current dead-code
  results as diagnostic rather than newly introduced. Checkpoint creation fails
  closed when credential-bearing paths would enter persistent Git objects.
- All three repositories now include contribution, governance, conduct, support,
  security, and project-specific threat-model documents.

Remaining items are not code-only checkboxes: correct the live GitHub repository
descriptions/license labels, package Prism in a Registry-supported format and
publish it to the official MCP Registry, expand held-out benchmark gates, and
build external maintainer/community evidence. The MCP Registry currently accepts
npm, PyPI, NuGet, OCI, or MCPB packages; a raw Go release binary is not itself a
publishable package type, so publication requires an OCI/MCPB or wrapper-package
decision rather than a misleading placeholder `server.json`.

## Final recommendation

The portfolio should not change direction away from the graph work. It should
change the level at which it tells the story.

Prism is not merely a context compressor. Grove is not merely persistent
memory. Together they are a local, measured change-intelligence system that can
make correctness less dependent on model capability. That is a credible and
important thesis.

The path to a top choice is:

1. Own **complete semantic change operations**, not generic code context.
2. Make **uncertainty and capability visible** in every answer.
3. Turn the existing research rigor into **release gates and external
   reproducibility**.
4. Make Prism the **one default installation** and Grove the respected engine.
5. Keep Mason as the **proof harness** until broad agent evidence earns
   promotion.
6. Build the missing governance, security, distribution, and community layer
   with the same seriousness as the graph algorithms.

If executed well, Prism can become a default add-on for coding agents and Grove
can become a leading open source code-intelligence engine. Calling either a
frontier open source project today would be premature. The technical core is
ahead of the public project around it; the next work is to close that gap.

## External references

- [Sourcegraph code graph and context documentation](https://sourcegraph.com/docs/cody/core-concepts/context)
- [Sourcegraph precise code navigation](https://sourcegraph.com/docs/code-navigation)
- [Aider repository map](https://aider.chat/docs/repomap.html)
- [Serena semantic coding toolkit](https://github.com/oraios/serena)
- [Official MCP Registry](https://github.com/mcp)
- [MCP Registry project](https://github.com/modelcontextprotocol/registry)
- [MCP Registry supported package types](https://modelcontextprotocol.io/registry/package-types)
- [OpenSSF Scorecard](https://openssf.org/scorecard/)
- [OpenSSF Best Practices Badge](https://openssf.org/projects/best-practices-badge/)
- [OpenSSF project criteria](https://www.bestpractices.dev/en/criteria)
- [Sigstore cosign](https://github.com/sigstore/cosign)
- [Grove public Go package](https://pkg.go.dev/github.com/provasign/grove/pkg/grove)
- [Prism repository](https://github.com/provasign/prism)
- [Grove repository](https://github.com/provasign/grove)
- [Mason repository](https://github.com/provasign/mason)
- [Provasign research artifacts](https://github.com/provasign/research)
