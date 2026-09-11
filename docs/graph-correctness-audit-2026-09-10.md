# Grove / Prism graph-correctness audit — 2026-09-10

## Result and scope

Real graph defects were reproduced and fixed locally in both repositories. The
Click completion chain is now correct for the demonstrated typed receivers;
uncertain dynamic callers are explicitly reported as partial. These fixes do
not establish exhaustive Python resolution or a benchmark cost improvement.

Work is uncommitted on `main` in both repositories. Prism started at `6fc9272`
(two commits ahead of its remote); Grove started at `39eeb240` / v0.43.3. Existing
compact-MCP changes in `selection.go`, `selection_test.go`, `server.go`, and
`compact_test.go` were preserved. No profile/evidence-delivery experiment was
reintroduced, and no production binary was replaced.

## Separate benchmark failure from graph failure

The inspected candidate Click cells do not establish that graph retrieval caused
their failed patches:

- PR3434 received the complete `write_usage` method through lookup, then placed
  the empty-argument handling under the short-prefix branch. That was a patch
  placement error.
- PR3471 used search and source reading, not an impact/caller query. Its first
  edit normalized completion values, but a visible pre-existing test expected
  mixed-case enum names (`["Au", "al"]`). The agent ran that test and reverted
  the normalization. The scoring task's test patch instead expects normalized
  values (`["au", "al"]`). The native arm did not run that conflicting visible
  test and passed the scorer. This is a task/test interaction, not evidence that
  more turns alone caused the failure.

The inspected local evidence is under
`/private/tmp/prism-compact-candidate-native-passes-20260910/` (task JSON,
`stdout.jsonl`, and `agent.diff`). These temporary artifacts are not checked into
this report. No new agent benchmark was run during the graph fix.

An independent graph probe on the PR3471 base checkout nevertheless found real
resolution defects. Those warranted fixing independently of benchmark causation.

## Findings and fixes

| Layer | Demonstrated defect | Local fix / regression coverage |
| --- | --- | --- |
| Python type narrowing | `types.ParamType[t.Any]` lost its nominal receiver type; generic bases were skipped. | Preserve the outer nominal type and generic base class. `pygeneric_test.go` and the real-parser completion fixture cover this. |
| Python binding | Same-file candidates displaced imported typed members; member calls could target free functions, and local free-function imports could target class methods. | Narrow receivers before fanout suppression; distinguish member/free-function candidates and honor recorded from-import bindings. |
| Python import bindings | Import aliases were dropped, and a function-local import was treated as visible to sibling functions. | Persist local name and import line, assign the binding to the smallest enclosing symbol, and resolve module/member/type aliases only in that lexical owner. |
| JavaScript / TypeScript imports | Named aliases such as `actual as alias` lost function callers; namespace/default aliases were not tied to their source module, while coverage still said indexed. | Preserve named, namespace, and default bindings, including type-only aliases and multiline default exports; retain default-export identity; narrow calls and type annotations through the import target. |
| Go imports | With `aa "…/a"` and `bb "…/b"`, both `aa.Run()` and `bb.Run()` linked to both packages' `Run` functions. | Preserve Go package aliases and make qualifier resolution select only the aliased import path. |
| Evidence strength | Unknown receiver targets were emitted as high-confidence AST edges. A second Python native pass added name-only calls at 0.98 confidence. | Mark unresolved qualified targets heuristic; remove the duplicate native call resolver. |
| Persisted graph | Unchanged source files let upgrades reuse edges built by old resolver semantics. | Add a resolver-version stamp; rebuild old graphs on hydration, force a full reindex after version changes, and discard obsolete Python-native call edges. |
| Scope | Bare/free-function impact queries ignored file scope or became ambiguous because of other files. Go method scope assumed the receiver type was in the method file. | Apply scope before loose-name resolution and free-function collection; use Go containment edges to identify a receiver declared in another file. |
| Prism coverage | Family `closed` was easy to misread as exhaustive caller coverage. | Deliver `familyCompleteness` separately from `callerCoverage`; dynamic-language caller sets are partial, including cached and CLI responses. |
| Impact inventory | Superclass obligations were rendered but omitted from counts and verification. | Use a shared deduplicated inventory including supers. |
| Wider-anchor hints | A larger unrelated same-name method could be recommended as a wider contract; CLI read the wrong hint key. | Require graph-established family overlap and render the actual hint note. |
| Old-contract verification | Same-name/old-signature matching invented unrelated obligations, including the `savings` helper shape. | Reconstruct one isolated base-source graph per verify request and query scoped identities. Never recover a failed scoped query with a global namesake union. |
| Diff coordinates | Base graph sites carry old line numbers and include removed callers. | Project surviving sites into current source; omit removed sites/calls and report ambiguous mappings for review. |
| Fail-closed behavior | Partial index errors and heuristic architecture review could be lost from the final verdict. | Preserve index errors through the adapter; partial coverage/indexing and architecture-review status prevent a complete verdict. |

Implementation sources:

- Grove: [Python binding](../../grove/internal/graph/pycallbinding.go),
  [lexical Python imports](../../grove/internal/graph/pyimports.go),
  [JavaScript/TypeScript imports](../../grove/internal/graph/jsimports.go),
  [call edges](../../grove/internal/graph/edges.go),
  [type inference](../../grove/internal/graph/pylocaltypes.go),
  [impact scope](../../grove/internal/graph/changeimpact.go),
  [resolver version](../../grove/internal/graph/resolverversion.go),
  [index rebuild](../../grove/internal/index/indexer.go),
  [graph hydration](../../grove/pkg/grove/grove.go), and
  [base preview](../../grove/pkg/grove/preview_impact.go).
- Prism: [coverage](../internal/mcp/impactcoverage.go),
  [inventory and anchor guard](../internal/mcp/wideranchor.go),
  [verification](../internal/mcp/verify.go),
  [base-site projection](../internal/mcp/verify_projection.go), and
  [preview adapter](../internal/grove/preview.go).

## Real Click result

Using `/private/tmp/prism-graph-correctness`, linked to local Grove, against
`/private/tmp/prism-graph-audit-click3471` without deleting its existing index:

```text
Choice.shell_complete
  declarations: Choice.shell_complete         src/click/types.py:398
  supers:       ParamType.shell_complete      src/click/types.py:155
  callers:      Parameter.shell_complete      src/click/core.py:2684
  totalSites: 3
  familyCompleteness: closed
  callerCoverage: partial
  completeness: partial
  widerAnchor: ParamType.shell_complete (related base, not Command)

Parameter.shell_complete
  callers: ShellComplete.get_completions      src/click/shell_completion.py:276

shell_complete, file=src/click/shell_completion.py
  declaration: shell_complete                src/click/shell_completion.py:19
  callers: Command._main_shell_completion     src/click/core.py:1490
           test_source_uses_lf_line_endings   tests/test_shell_completion.py:375
```

The original Choice query missed `Parameter.shell_complete`, associated the
free-function test with the wrong member, and recommended unrelated Command.
The corrected free-function query no longer reports `ShellComplete.get_completions`
as its caller: that method calls an object's member, not the module function.

## Regression coverage and validation

Important tests include:

- [Real-parser Python completion chain](../../grove/internal/parser/python_dispatch_e2e_test.go):
  required typed/imported edges and forbidden same-name decoys.
- [Python aliases and lexical imports](../../grove/internal/parser/python_alias_e2e_test.go),
  [JavaScript/TypeScript aliases](../../grove/internal/parser/js_alias_e2e_test.go),
  and [Go package aliases](../../grove/internal/parser/go_alias_e2e_test.go).
- [Generic annotations](../../grove/internal/graph/pygeneric_test.go) and
  [file-scoped free functions](../../grove/internal/graph/scopedfunction_test.go).
- [Preview isolation and persisted-index upgrade](../../grove/pkg/grove/preview_impact_test.go):
  no source/live-graph mutation, no unrelated family, obsolete native edges
  removed after reopen, resolver stamp forces reindex.
- [Split-file Go receiver](../../grove/pkg/grove/preview_split_go_test.go).
- [Prism graph/verifier regressions](../internal/mcp/graphcorrectness_test.go):
  supers counted and checked, unrelated savings methods excluded, shifted lines
  mapped correctly, deleted callers satisfied, untouched shifted calls still
  reported, and partial Python coverage requires review.
- [CLI certainty and hint rendering](../internal/cli/impacttext_test.go), plus
  existing cached-response and wider-anchor tests.

Final validation passed:

- Grove: full `go test ./... -race -count=1`.
- Prism with local Grove: full `go test ./... -race -count=1`.
- Prism with pinned released Grove: full `go test ./... -count=1`.
- Corrected `prism verify --format text`: Prism is complete with no missed sites
  across 16 tracked changed files. Grove is intentionally fail-closed at
  `review` across 9 tracked changed files because `ExtractorVersion` is a
  changed constant with no call-shaped blast radius. Exhaustive text search
  confirmed its only non-declaration references are the two index upgrade and
  version-persistence checks in `internal/index/indexer.go`. Git diff
  verification does not include untracked new files; the Go suites compiled
  and exercised the new implementation/test files.
- Removed-helper reference check: no remaining source/document references at
  the time checked. Both worktrees passed `git diff --check`.

Validation runs use both ordinary released-dependency and coordinated local
builds. The temporary `/private/tmp/prism-graph-audit.go.work` includes only
Prism and Grove; `GOFLAGS=-mod=readonly` avoids the machine's stored `-mod=mod`
workspace conflict. The full Grove suite and the full coordinated Prism suite
are run with `-race -count=1`; the standalone Prism suite is also run with
`GOWORK=off` against its currently pinned dependency.

## Follow-up: comment retrieval versus delivery

The user's comment-search hypothesis was tested independently in
`/private/tmp/prism-comment-probe.zjpFXq`, using the corrected temporary binary:

- Exhaustive text search found module comments, a docstring, an inline body
  comment, and a trailing comment. Comments are not excluded from raw search.
- Query returned a module-only comment as a raw text match. A small function's
  body comment also reached the response.
- In a 207-line file, a unique comment at line 103 matched the enclosing
  function. Query returned only lines 1–11, with no matching comment or separate
  raw-text hit. Exhaustive text search returned the correct line 103 immediately.

This is a **reproduced, still-unfixed source-delivery defect**, not a graph edge
failure. The [content-only disclosure rule](../internal/mcp/delivery.go) demotes
body-only matches to a signature-sized window; the delivered result can omit
the actual retrieval evidence. It can require another agent turn. Whether it
caused a specific benchmark failure requires that transcript's actual terms
and response, not inference from this synthetic reproduction.

The narrow follow-up is to preserve a bounded window around each matched line
that is not otherwise delivered, without restoring profile-specific evidence
expansion or dumping the whole function. No product change for this separate
finding was made in response to the user's diagnostic question.

## Remaining limits and release boundary

1. Python runtime dispatch, monkey-patching, wildcard imports, conditional
   rebinding, and nested lexical scopes remain incomplete. The extractor does
   not emit nested functions as separate
   symbols; a [regression test](../../grove/internal/parser/python_lexical_dispatch_test.go)
   ensures an unresolved nested call is not assigned to an unrelated class
   member. Partial caller coverage is intentional and must not be relabeled closed.
2. Base preview uses an AST-built graph; it does not rerun language-native
   analyzers against a historical on-disk checkout. It preserves the source
   worktree and live index but is not compiler proof of historical completeness.
3. `prism_verify` checks indexed obligations and whether relevant source was
   touched, not whether the edited program is semantically correct. Compiler,
   project tests, and targeted exhaustive search remain necessary.
4. This was a targeted correctness audit plus full regression suites, not a
   proof of every resolver for every supported language. No new token-savings
   estimate follows from these changes.
5. Prism's checked-in dependency still pins Grove v0.43.3. The optional preview
   adapter reports missing base coverage as review with that version. The full
   fix requires reviewing/releasing Grove first, then updating Prism's dependency
   and releasing Prism. No absolute local replacement was added to `go.mod`.
6. Resolver stamps rebuild stored data when a corrected binary opens/indexes it;
   they cannot update code inside an already-running old MCP process. Such
   processes must still restart into the new binary through the host lifecycle.

No commit, push, tag, installation, or process termination was performed.
