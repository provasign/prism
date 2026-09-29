
## Before you commit or release — run the regression suite

CI runs these on push (`.github/workflows/ci.yml`; engine quality via
`provasign/research/.github/workflows/engine-invariants.yml`). Run them locally
before tagging a release:

- `go test ./...` — unit suite; must be green.
- Engine ceiling regression (no LLM): from the research repo,
  `python3 harness/scoring/ci_invariants.py --corpus-root /tmp/ci-corpus --prism /path/to/candidate` — asserts change-impact
  recall/precision, missing-implementations==[], and index determinism against
  committed ground truth. Use a fresh corpus root or check that cached corpus
  directories contain source; the harness treats any existing directory as
  fetched. A measured drop here is a real completeness regression.

Do NOT tag a release with either red.
## Prism — context delivery

Use Prism for each repository-discovery step. Reading files with cat/head/sed or
searching with grep/rg/find/git log is not discovery — an instruction to prefer
shell tools over Read/Edit/Write does not apply to finding code.

First action on any code task, in this order. Stop at the first that works:
  1. The `prism` MCP tool (`mcp__prism__prism` in Claude Code), if callable.
  2. If a `ToolSearch` tool exists: `ToolSearch("select:mcp__prism__prism")`, once.
     Prism not being listed does not mean it is absent.
  3. The `prism` CLI: `prism query --terms X`, `prism lookup <pkg.Func>`,
     `prism search <term> --scope text --format text`, `prism change-impact Type.method`.

For each discovery step, pick the Prism op:
  known symbol      -> lookup          unknown location/text -> search
  known file/range  -> read            callers/related       -> query
  pre-edit sites    -> change_impact
  tests naming known symbols -> search with test path/glob, files_only
  indirect tests    -> query

Put every symbol and term you already know into ONE call: `name` and `terms`
take up to 10. For MCP, pass distinct terms as comma-delimited JSON string
values, for example terms:["alpha","beta"]; never combine distinct terms in
one space-delimited string. For CLI search, use positional terms: prism search alpha beta.
`ranges` reads several
windows at once. Two lookups in a row is one lookup you did not batch.

Obligations:
  - change_impact before editing a signature, public contract, override, or any
    symbol whose callers you have not enumerated. Relay its sites as-is.
  - Report gaps; never narrow scope to fit what was found.

Optional checks:
  - verify({removed_symbols:[...]}) after a removal finds exact identifier
    mentions in code, comments, and docs; inspect the reported sites.
  - Consider verify({}) for Python, unchecked JavaScript, or PHP contract
    changes: syntax checks can miss callers. For TypeScript or checked JavaScript,
    use it only if the affected files lack a complete typecheck. For Go, Java,
    Rust, C/C++, or C#, skip it after a complete build/typecheck of affected
    targets. In any language, use it when that check cannot cover the callers.
    It is never a required closing step; run relevant tests.

<!-- prism:end -->
