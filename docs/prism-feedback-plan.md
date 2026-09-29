# Prism Feedback: Plan of Attack

## Context

This plan consolidates experience-based feedback from recent Prism use across
several repositories. The feedback is consistent on two points:

- Prism creates genuine value for structural work: locating known symbols,
  reading exact bodies, enumerating callers, assessing signature changes, and
  estimating blast radius.
- Prism loses trust or efficiency when it presents incomplete text-search
  samples as the default workflow, treats safe file moves as removals, or uses
  the same warning language for dead code and incomplete graph evidence.

The product goal is therefore not to turn Prism into a runtime correctness
oracle or to outdo `grep` at string matching. It is to preserve Prism's
structural advantages while eliminating false alarms, ambiguous completeness,
and avoidable follow-up calls.

## Priorities

| Priority | Workstream | Desired outcome | Size |
| --- | --- | --- | --- |
| P0 | Relocation-aware `prism_verify` | Whole-file restructures do not produce false missed-site alarms | Large |
| P0 | Honest zero-caller classification | Dead-code evidence is separated from graph incompleteness | Medium |
| P0 | Deduplicated verification notes | Important findings are not buried under repeated caveats | Small |
| P1 | Adaptive, exact text search | Small searches are complete in one call | Medium |
| P1 | Compact search modes and counts | Scope discovery is cheaper and predictable | Medium |
| P2 | Stale-version detection audit | Version skew is reported once, early, and reliably | Small |
| P3 | Multi-repository workspaces | Explicit sibling repositories can be searched safely | Extra large |
| Ongoing | Positioning and evaluation | Prism is used as navigation and blast-radius evidence, not a correctness oracle | Small |

## Phase 0: Capture the Reported Failures

Before changing behavior, create regression fixtures that reproduce the
reported cases:

1. Split one source file into several new modules while keeping function bodies
   unchanged and preserving callers through re-exports.
2. Move an implementation without preserving its old import path, proving that
   relocation detection does not hide a real missed site.
3. Change internal functions that have zero callers.
4. Change exported or public functions that have zero repository-local callers.
5. Produce a verification run containing dozens of identical
   `callers-only` completeness notes.
6. Search for a term with exactly 42 matching lines, including more than 20
   matches in one file.
7. Repeat identical searches to verify deterministic ordering.
8. Replace or conflict with the running MCP binary before its first tool call.

For each fixture, record:

- False-positive and false-negative counts.
- Output tokens.
- Number of Prism calls required.
- Wall-clock time.
- Whether totals and completeness claims are exact.

These measurements become the acceptance gate for the implementation phases.

## Phase 1: Make Verification Relocation-Aware

### Current behavior

`internal/grove/client.go` performs rename pairing inside `Client.DiffFile`, but
that operation compares one file's base symbols with the current symbols from
the same path. A symbol moved to another file bypasses that pairing.

`internal/mcp/verify.go` then treats symbols from deleted files as removals and
skips new files because they have no base-side contracts. This makes a
whole-file split appear as unrelated removals and additions, causing the old
call graph to be reported as missed sites.

### Implementation

Add a repository-level reconciliation pass before verification creates its
contract-change seeds:

1. Collect unmatched base-side removals from changed and deleted files.
2. Collect current-side additions from changed and new files.
3. Compute conservative fingerprints using:
   - language and symbol kind;
   - normalized signature;
   - exact or safely normalized body content; and
   - parent or declaring-type information when applicable.
4. Accept only unambiguous one-to-one matches.
5. Classify matches as:
   - `relocated-identical`;
   - `renamed-and-relocated`; or
   - `relocation-candidate` when evidence is insufficient.

Fingerprint equality alone must not suppress verification. For an identical
relocation, unchanged callers are acceptable only when the current graph still
resolves those sites to the relocated declaration or a valid re-export. A
caller that still refers to a broken old path remains a missed site.

Ambiguous matches must remain fail-closed, but they should be reported as
relocation candidates rather than hundreds of confidently mislabeled missed
sites.

### Desired output

```text
relocations verified (14):
  core/queries.py:QueryData -> core/queries/data.py:QueryData
  body and signature unchanged; 8 existing callers still resolve
```

### Acceptance criteria

- An unchanged body moved across files and preserved through a re-export does
  not generate missed-site findings.
- A move that breaks the old import path still reports the unresolved callers.
- Body or signature changes are not classified as identical relocation.
- Ambiguous many-to-one or one-to-many matches remain review findings.
- Output ordering is deterministic.

## Phase 2: Separate Dead Code from Unknown Coverage

### Current behavior

The empty-impact path in `internal/mcp/verify.go` currently combines two
different situations:

- Prism resolved a repository-local symbol successfully and found no callers.
- Prism failed to establish a reliable blast radius.

The first is useful evidence; the second is an analysis limitation. Reporting
both as an unverified contract change trains users to treat the result as
noise.

### Implementation

Introduce structured classifications:

- `likely_dead_code`: an internal or private symbol resolved successfully with
  zero callers;
- `no_local_dependents`: a complete repository-local result requiring no other
  changed sites;
- `external_contract_unverified`: an exported or public surface with no local
  callers but possible external consumers; and
- `graph_incomplete`: parsing, indexing, or resolution failed.

A complete zero-caller result for an internal function should not degrade the
entire verification verdict. Public APIs should remain conservative because
their consumers may live outside the indexed repository.

### Desired output

```text
likely dead code (4):
  app.code_dependency_tree - zero repository-local callers
```

### Acceptance criteria

- Internal zero-caller symbols are informational rather than unverified
  contract failures.
- Public zero-caller symbols retain an external-contract caveat.
- Parser, index, and resolver failures remain clearly incomplete.
- Machine-readable output identifies the classification without requiring
  consumers to parse prose.

## Phase 3: Collapse Repetitive Verification Notes

### Current behavior

`renderVerifyAsText` in `internal/mcp/graphtext.go` prints each note in the
result independently. Per-symbol completeness notes therefore repeat the same
sentence dozens of times.

### Implementation

Aggregate shared coverage state in the verification result, for example:

```json
{
  "completenessSummary": [
    {
      "classification": "callers-only",
      "symbolCount": 41,
      "exceptions": []
    }
  ]
}
```

Render common state once near the top of the report, then print only
symbol-specific exceptions:

```text
coverage: 41 function changes used repository-local caller analysis
```

### Acceptance criteria

- A shared caveat appears once per verification run.
- Symbol-specific failures remain attached to the affected symbol.
- JSON and text outputs preserve the same information.
- Group and finding ordering is deterministic.

## Phase 4: Make Small Text Searches Complete in One Call

### Current behavior

The text-search engine over-fetches to improve samples, but its default
per-file cap means `TotalHits` is a lower bound. Even a modest repository-wide
result can therefore be rendered as `AT LEAST N`, forcing an exhaustive
follow-up call to establish the real inventory.

### Implementation

Add a count-first primitive for all three text-search backends:

- Ripgrep: exact per-file match counts.
- Grep: the corresponding count mode.
- Native scanner: count matches without retaining line text.

Apply an adaptive delivery policy:

- If the exact total is at or below a bounded threshold, such as 64, return
  every match automatically.
- Above the threshold, return the requested sample and the exact total when
  counting completed.
- If counting times out or reaches a safety cap, return an explicit incomplete
  count rather than implying precision.

Suggested result shapes:

```json
{
  "totalHits": 42,
  "countComplete": true,
  "resultsComplete": true,
  "textHits": []
}
```

```json
{
  "matchedAtLeast": 2000,
  "countComplete": false,
  "resultsComplete": false,
  "textHits": []
}
```

### Acceptance criteria

- The 42-hit fixture returns all 42 lines in one Prism call.
- Exact totals are labeled exact only when the count completed.
- Large searches remain bounded in time and output size.
- Timeout and rejected-scope behavior remains fail-closed.
- Backend behavior is equivalent across ripgrep, grep, and the native scanner.

## Phase 5: Clarify Compact Search Modes

### Current behavior

`scope="text"` is described as a pure grep-like search, but a truncated result
can still trigger symbol rollups and structural guidance. That enrichment is
valuable for exploration but wasteful for a simple existence, location, or
scope question.

### Implementation

Make the modes explicit:

- `scope="text"`: grep-like lines and counts with no graph enrichment.
- `scope="both"`: text matches plus structural hints.
- `rollup_only=true`: graph grouping without raw sample lines.
- `files_only=true, count=true`: filenames and per-file counts with no sample
  lines.

Keep results ordered deterministically by file path and line number.

### Acceptance criteria

- Pure text search never performs or returns a graph rollup.
- Files-with-count output contains no sample-line payload.
- Repeated calls on an unchanged tree produce byte-for-byte stable ordering.
- Existing structural search remains available without an extra round trip.

## Phase 6: Audit Version and Installation Warnings

### Current behavior

The MCP server already checks whether its running executable was replaced on
disk and whether other local Prism installations have conflicting versions.
The reported experience suggests that the warning is not consistently visible
at the moment it is most useful.

### Implementation

Test the exact failure modes before redesigning the mechanism:

- The running executable is replaced after server startup.
- MCP configuration is pinned to another installation.
- Multiple installations expose different versions.
- An upgrade occurs before the first tool call.
- The warning is delivered once and remains prominent.

Unify these checks into a first-tool-call session health notice. Avoid a
mandatory network lookup for the latest released version: it would add latency,
privacy concerns, and an external failure mode to normal repository work.

### Acceptance criteria

- Local version skew is reported on the first relevant tool call.
- The warning names the running path and version.
- Restart instructions are specific and actionable.
- The same warning is not repeated throughout the session.

## Phase 7: Design Multi-Repository Workspaces Separately

### Rationale

The current handler, Grove client, tracker, and ledger are rooted in one
repository. Allowing arbitrary paths in search would weaken the existing path
containment and identity guarantees. Cross-repository support should therefore
be an explicit workspace model rather than an exception in individual tools.

### Proposed configuration

```yaml
version: 1
workspace:
  roots:
    - id: app
      path: .
    - id: datacollection
      path: ../datacollection
```

### Requirements

- Roots are explicit, canonical, and allowlisted.
- Results use qualified paths such as `root-id/path`.
- Index and cache state are isolated or namespaced per root.
- Prism never traverses into sibling repositories implicitly.
- Search, read, and lookup support land before cross-repository graph claims.
- Cross-repository call edges are enabled only after symbol identity and import
  resolution are proven reliable.

This work must not block the P0 and P1 improvements.

## Product Contract

Prism should describe itself consistently as a static navigation and
blast-radius tool:

- It answers where a symbol lives, who calls it, which implementations satisfy
  a contract, and which sites may need coordinated changes.
- It does not prove runtime correctness involving live data, state, timing,
  configuration, networks, databases, or external services.
- A complete static result is evidence about the indexed workspace, not a claim
  that the program is defect-free.

This distinction should appear in tool descriptions and evaluation criteria,
not as repetitive boilerplate in every result.

## Delivery Sequence

All repository work is committed and pushed directly to `main` in small,
independently testable commits:

1. Add regression fixtures and record baseline measurements.
2. Add cross-file relocation reconciliation.
3. Verify callers against relocated declarations and re-exports.
4. Add zero-caller and incomplete-graph classifications.
5. Aggregate verification completeness notes.
6. Add the exact-count search primitive.
7. Add adaptive small-result completeness.
8. Add compact text and files-with-count modes.
9. Harden version and installation warnings.
10. Produce a multi-root design and prototype after the earlier metrics pass.

Before editing an existing symbol, run `prism change-impact` for that symbol.
Before completing each multi-site milestone, run the relevant tests followed by
plain `prism verify`.

## Release-Level Success Criteria

The improvement is ready when all of the following are true:

- A whole-file split with unchanged implementations and valid re-exports
  produces no false missed sites.
- A broken move still reports every unresolved or stale caller Prism can prove.
- A 42-hit text search returns a complete, exact result in one call.
- Verification completeness caveats appear once per run.
- Prism never labels unresolved graph evidence as dead code or verified
  relocation.
- Text-search output is deterministic across repeated runs.
- The measured structural workflows retain their current token and navigation
  advantages.

