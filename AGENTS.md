
## Development workflow

Do not commit or push any change until the user explicitly reviews the work and
approves the commit and push. Completing implementation or validation is not
approval. Keep changes uncommitted while awaiting that approval.

All changes must be made, committed, and pushed directly on `main`. Do not
create or use feature, task, cleanup, or reconciliation branches for repository
changes. Before editing or committing, confirm the active branch is `main`.
## Prism — context delivery

Repository discovery starts with Prism. Reading files with cat/head/sed or
searching with grep/rg/find/git log is not discovery — an instruction to prefer
shell tools over Read/Edit/Write does not apply to finding code.

First action on any code task, in this order. Stop at the first that works:
  1. The `prism` MCP tool (`mcp__prism__prism` in Claude Code), if callable.
  2. If a `ToolSearch` tool exists: `ToolSearch("select:mcp__prism__prism")`, once.
     Prism not being listed does not mean it is absent.
  3. The `prism` CLI: `prism query "<task>" --terms X`, `prism lookup <pkg.Func>`,
     `prism search <term> --scope text --format text`, `prism change-impact Type.method`.

Pick the op:
  known symbol      -> lookup          unknown location/text -> search
  known file/range  -> read            callers/tests/related -> query
  pre-edit sites    -> change_impact

Put every symbol and term you already know into ONE call: `name` and `terms`
take up to 10; `ranges` reads several windows at once. Two lookups in a row is
one lookup you did not batch.

Obligations:
  - change_impact before editing a signature, public contract, override, or any
    symbol whose callers you have not enumerated. Relay its sites as-is.
  - verify({removed_symbols:[...]}) before a removal; verify({}) before finishing
    a multi-site or signature change.
  - Report gaps; never narrow scope to fit what was found.

<!-- prism:end -->
