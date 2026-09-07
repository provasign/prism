# Prism research handoff

Updated: 2026-09-07

## Start here

Work directly on `main`. This project uses trunk-based development; do not create feature branches or worktrees for routine work. Preserve unrelated working-tree changes and stage files explicitly.

The current published Prism release is `v0.72.0`. It is installed at:

```sh
/Users/tapabratapal/bin/prism
```

The installer verified the release checksum. Prism, Grove, astkit, and Mason were initialized with that binary. Restart the coding client before the next session so it respawns MCP servers from the updated configuration.

Verify the environment at the beginning of the next session:

```sh
cd /Users/tapabratapal/Projects/provasign/prism
git status --short --branch
/Users/tapabratapal/bin/prism version
/Users/tapabratapal/bin/prism doctor .
```

Expected version: `prism v0.72.0`.

## Repository state

All completed work is on `main` and pushed. The documentation cleanup landed as:

| Repository | Main commit | Change |
|---|---|---|
| Prism | `81352df` | Consolidated public documentation and removed obsolete internal reports |
| Prism | `ccfb1a1` | Shale session evidence follow-up |
| Grove | `919d01e6` | Refreshed public documentation and removed obsolete implementation plans |
| Research | `3d0f882` | Made current results canonical and removed superseded study plans |
| provasign.github.io | `d9ffb05` | Refreshed product and benchmark pages |

The `v0.72.0` project setup was then committed and pushed on `main`:

| Repository | Commit | Setup change |
|---|---|---|
| Prism | `9b944e2` | Updated generated steering and MCP paths; added this handoff; updated the release pin |
| Prism | `f0cf95b` | Shale session evidence follow-up |
| Grove | `41ac4664` | Committed the current Claude MCP permission file and removed legacy search denial |
| astkit | `d3c9b7f` | Added generated Prism project configuration and steering |
| astkit | `3163ed8` | Ignored the local `.grove/` index |
| Mason | `ebfc5b7` | Removed legacy Claude search denial while preserving the existing graph-orientation work |
| provasign.github.io | `4158a34` | Updated the public Prism release pin to `v0.72.0` |

The public Pages build passed and the current material is live at:

- https://provasign.dev/prism/
- https://provasign.dev/grove/
- https://provasign.dev/benchmarks/

The maintained result summary is:

- `/Users/tapabratapal/Projects/provasign/research/RESULTS.md`
- https://github.com/provasign/research/blob/main/RESULTS.md

## Current evidence

The latest paired Sonnet release-gate sample contains one fresh native and one fresh Prism run for each of nine change-impact tasks across Go, Java, TypeScript, and Python.

| Nine-task aggregate | Native tools | Prism |
|---|---:|---:|
| Mean recall | 0.683 | 0.998 |
| Mean precision | 0.770 | 0.955 |
| Total estimated cost | $3.23 | $1.28 |
| Relative cost | 1.00× | 0.40× |

Prism improved recall in eight tasks, tied at full recall in one, and cost less in all nine. This table is broad but has one run per cell; treat it as a release-gate snapshot rather than a variance estimate.

Repeated evidence already collected:

- Jackson `JsonNode.get`, three paired trials: native mean recall 0.625, 30 turns, $0.404; Prism mean recall 1.000, 8.3 turns, $0.205. Precision was 1.000 in both arms.
- Grafana `QueryData`, three fresh Prism trials: recall 1.000 in all trials, precision 0.933–0.959, mean 4 turns, mean cost $0.172.
- Grafana `CheckHealth`, three fresh Prism trials: recall 1.000 in all trials, precision 0.983, mean 5 turns, mean cost $0.229.
- Deterministic engine calls score recall 1.000 on the corrected 70-site `QueryData` oracle and the 59-site `CheckHealth` oracle.

The original `QueryData` oracle had 51 sites. The corrected oracle has 70 after adding omitted middleware implementations, another external-interface implementation, a production fake, and direct handler callers. Do not compare precision from the old and corrected oracles as though they used the same measurement contract.

## What has been fixed

The Grafana regression was caused by a missing graph operation and an inefficient fallback pattern. A Go interface declared in an external dependency had dozens of independently named local implementations. Agents guessed concrete type names and issued overlapping impact calls.

Current Prism/Grove behavior now provides general fixes:

- local Go methods can be matched to a compatible external interface method set
- exhaustive search overflow returns a compact, complete inventory of exact file paths and enclosing symbols
- detailed signatures and call expressions are concentrated on ambiguous or heuristic edges
- current steering falls back to exhaustive text search when an external contract is unresolved or an impact closure is too small for the task
- current steering tells the agent to reuse impact identities and avoid overlapping body reads or impact calls

These changes are structural. Prism and Grove contain no Grafana, Jackson, Guava, Django, TypeORM, task-name, oracle-answer, or nine-cell special cases. Ground truth and thresholds live only in the Research repository.

## Research priorities

### 1. Repeat the full nine-task comparison

Run at least three fresh trials per arm per task, preferably five for the highest-variance tasks. Report distributions or confidence intervals for recall, precision, turns, request tokens, elapsed time, and cost. The current one-run table should remain visible until this is complete, but it must stay labeled as a snapshot.

Randomize or counterbalance arm order. A fixed baseline-then-Prism sequence can mix temporal provider variance with the treatment effect.

### 2. Audit the residual errors

Start with the remaining accuracy gaps:

- `guava-forwarding-delegate`: Prism recall 0.984, so inspect the single missed site and classify it as engine, agent assembly, or oracle error.
- `jackson-serialize`: Prism precision 0.864.
- `django-quotename`: Prism precision 0.865.
- `typeorm-driver-escape`: Prism precision 0.974.

For low precision, audit the oracle before changing the engine. The Grafana correction showed that stable 0.6–0.75 precision across unrelated product changes can indicate incomplete ground truth.

### 3. Measure the v0.72.0 steering change directly

Use paired cells to test the mechanisms introduced in the generated instructions:

- named impact target goes directly to `change_impact`
- known body inspection batches `lookup`
- external or unresolved interfaces fall back once to exhaustive search
- impact identities are reused without overlapping calls
- wide removals begin with an exhaustive file inventory

Score tool calls, duplicate result bytes, request tokens, and lost sites. Transcript review should classify why each extra call occurred instead of treating total turns as an unexplained metric.

### 4. Establish the narrow-task break-even point

Earlier work repeatedly showed that native tools can be cheaper on small, distinctive, greppable tasks. Construct a controlled size/ambiguity sweep and identify where Prism becomes beneficial:

- number of true sites
- text-hit ambiguity
- inheritance or interface depth
- number of files and packages
- external versus project-local contracts

The product should route narrow location tasks to cheap search and reserve graph expansion for relational questions. Measure that policy; do not assume every task should use `change_impact`.

### 5. Broaden beyond change-impact answers

Evaluate whether better context changes actual coding outcomes: patch correctness, tests passed, missed files, repair loops, and total cost. Candidate beds include Mason's automatic graph orientation work and the existing wide-task harness. Keep deterministic retrieval quality separate from model behavior.

### 6. Generalize external-contract resolution

The external Go interface case is fixed. Look for the same class of problem in Java, C#, TypeScript, Rust traits, and framework callback contracts. Build outcome-blind tasks first, then add product work only when an independent oracle demonstrates a real gap.

### 7. Keep payload size regression-gated

For large families, measure exact response bytes as well as tokens. Require compact identities for unambiguous resolved sites and attach signatures or expressions only where they help resolve uncertainty. Guava previously exposed a roughly 42 KB impact response; the current paired run is cheaper than native, but payload ceilings should remain a deterministic engine gate.

## Fresh-session protocol

Do not resume an earlier agent conversation for a scored trial. For every cell:

1. Create a fresh isolated snapshot at the task's pinned commit.
2. Start a new model invocation and session.
3. Record arm, model, task, snapshot path, repository commit, Prism version, invocation ID, and session ID.
4. Capture the complete transcript and tool trace.
5. Score with the committed oracle and store the raw answer.
6. Archive unsuccessful attempts too; exclude them only by a written, outcome-blind rule.

Provider prompt caching can reuse identical static input prefixes. It does not share previous answers or conversation state. To audit this, confirm that every cell has a distinct invocation ID, session ID, snapshot directory, and transcript file. Also randomize run order and compare cache-read accounting between arms.

Claude transcripts live under `~/.claude/projects/`, one encoded directory per snapshot working directory. Read `session_id` from the cell's `*.attempt.json`, then locate the transcript with:

```sh
find ~/.claude/projects -name '<session_id>.jsonl'
```

The freshness audit and its six distinct session/snapshot records are under:

```text
/Users/tapabratapal/Projects/provasign/research/harness/runs/impact-oracle-reliability-2026-09-07/
```

## Useful commands

```sh
cd /Users/tapabratapal/Projects/provasign/research/harness

# Scorer tests
python3 -m unittest discover -s tests

# Deterministic engine ceiling
python3 impact_oracle.py tasks/grafana-querydata-impact.json
python3 impact_oracle.py tasks/grafana-checkhealth-impact.json

# Agent trials; verify current runner flags before launching a large panel
python3 run.py --task tasks/jackson-serialize.json --arms T Gstar --trials 3 --model sonnet
```

Before a large paid panel, run one dry or single-cell validation to confirm paths, task pins, model identity, allowed tools, scoring, and transcript capture. Do not tune product behavior against expected task answers.

## Working-tree cautions

There is pre-existing uncommitted work that must be preserved:

- Mason has an in-progress automatic graph-orientation implementation and dependency update, including `internal/agent/graphcontext.go`, related tests, and edits across the agent/provider code. Its current `go.mod` moves embedded Prism from `v0.55.11` to `v0.69.1`; upgrading that WIP to `v0.72.0` requires an intentional build/test pass.
- Research has a modified `harness/mason_bench.py` plus many untracked gate and wide-run artifacts.

Do not use broad cleanup, reset, checkout, or `git add -A` in those repositories. Stage only files belonging to the active task.

## Operational follow-up

The GitHub release is `v0.72.0`, while the Homebrew formula reported `v0.69.2` during this setup. The direct installer is current and checksum-verified. Refresh the Homebrew tap before telling users that `brew install provasign/shale/prism` yields the latest release.

After this handoff is committed, the only expected dirty files should be the pre-existing Mason and Research work described above plus any intentionally local index/config files ignored by Git.
