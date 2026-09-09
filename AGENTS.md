
## Development workflow

All changes must be made, committed, and pushed directly on `main`. Do not
create or use feature, task, cleanup, or reconciliation branches for repository
changes. Before editing or committing, confirm the active branch is `main`.

## Prism — context delivery

Use Prism's call/type graph for code discovery.

**Before code discovery**, use the first available Prism route:
- Prism MCP tools visible? Call one directly.
- `ToolSearch` available? Run once (tools may be deferred):

       ToolSearch("select:mcp__prism__prism_search,mcp__prism__prism_query,mcp__prism__prism_change_impact,mcp__prism__prism_lookup")

- Otherwise use the Bash CLI. Never abandon Prism because `ToolSearch` is absent.

Choose the first call by need:
- Affected sites or signature change: `prism_change_impact` directly.
- Known symbol bodies: `prism_lookup(name=["A","B"])`.
- Known file/range: `prism_read`.
- Unknown code/text location: `prism_search`.
- Related implementations, callers, and tests: `prism_query` with explicit anchors.
Known symbol: use impact/lookup directly, not search.
Search only for external/unresolved contracts or wider scope.
Read-only inspection alone needs no impact.

Workflow rules:

- Before editing an existing symbol: `prism_change_impact(query="Type.method")`.
  **Relay that set as-is**; do not filter it through grep/sed.
- Before declaring a multi-site change done: `prism_verify`.
- External/unresolved interface or undersized closure for a wide task?
  Use batched `prism_search(scope="text", exhaustive=true)` for declarations, calls,
  and interface refs. Inspect signatures/receivers; text matches do not prove implementation.
  Use file-qualified identities, not guessed type names; report unresolved coverage.
- After impact, reuse signatures and call expressions for site enumeration;
  do not fetch bodies by default. Follow up for behavior, omitted evidence, ambiguous receivers, stale/incomplete
  scope, or required non-code references. Do not rescan to reproduce the site list.
  Preserve reported sites while resolving uncertainty; if gaps remain,
  report them instead of claiming completeness.
- Removing symbols? Before editing, run `prism_verify(removed_symbols=["A","B"])`.
  Re-run per edit round; plain `prism_verify` still gates the finish. Check
  non-compiling surfaces too: comments, docs, config, and other languages.
- Several names to find? ONE call: `prism_search(query=["A","B","C"])`.
  Add `context=3` for surrounding lines. Use `scope="text"` for pure grep.
- Wide removal/refactor ("remove X everywhere")? Open with
  `prism_search(query="<concept>", scope="text", exhaustive=true, files_only=true)` —
  inspect partial-result warnings before treating the inventory as complete.
- Need a file or exact line range? `prism_read`. Whole named bodies? Batch
  `prism_lookup`, not guessed search context. Reuse delivered unchanged source.

Bash-only (subagents, CI) — same verbs, add `--format text`:

    prism search <term>... [--path <file-or-dir>] [--exhaustive] --scope text
    prism query "<task>" --terms X
    prism change-impact 'Type.method'
    prism lookup <pkg.Func>   |   prism read <file>   |   prism verify --base <ref>

<!-- prism:end -->
