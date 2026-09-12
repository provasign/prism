
## Before you commit or release — run the regression suite

CI runs these on push (`.github/workflows/ci.yml`; engine quality via
`provasign/research/.github/workflows/engine-invariants.yml`). Run them locally
before tagging a release:

- `go test ./...` — unit suite; must be green.
- Engine ceiling regression (no LLM): from the research repo,
  `python3 harness/ci_invariants.py --prism ~/bin/prism` — asserts change-impact
  recall/precision, missing-implementations==[], and index determinism against
  committed ground truth. A drop here is a real completeness regression.

Do NOT tag a release with either red.
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
