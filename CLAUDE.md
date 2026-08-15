
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

Prism indexes this repo's call and type graph. Two things it does that a text
search cannot: resolve the complete blast radius of a change, and hand back
edit-ready context in one call.

**If you do not see prism_* in your tool list, they are DEFERRED, not absent.**
Load them once, rather than concluding Prism is unavailable and grepping for
everything (measured: the most common reason Prism goes unused where it is
correctly installed):

    ToolSearch("select:prism_query,prism_search,prism_change_impact")

Route by the question. One call, and treat its result as final:

| Question | Call |
|---|---|
| where is X? | `prism_search` — `scope="text"` is a plain grep and the cheapest option |
| where are X, Y and Z? | `prism_search(query=["X","Y","Z"])` — one call, up to 10 terms, grouped by term |
| read one function | `prism_lookup(name="pkg.Func")` |
| read one file | `prism_read` |
| give me the code for X, ready to edit | `prism_query(task="<label>", terms=["X"])` — keys on `terms`; the task wording changes nothing |
| who breaks if I change X? | `prism_change_impact(query="Type.method")` |
| is my diff complete? | `prism_verify` (pre-commit gate) |

Before editing an existing symbol, run `prism_change_impact`. It returns the
declarations, every override and implementation, and all resolved callers,
type-resolved in one call. **Relay that set as-is** — re-verifying or filtering
it through grep/sed/scripts measurably drops real sites.

Anything else Prism does (map, dead-code, rename-plan, missing-implementations,
arch, index) is a CLI command; `prism --help` lists them. Indexing is
automatic — you never need to trigger it.

### Bash-only (subagents, CI)

    prism search <term> [more terms...] --scope text --format text
    prism query "<task>" --terms X --format text
    prism change-impact 'Type.method' --format text
    prism lookup <pkg.Func> --format text
    prism read <file> --format text
    prism verify --base <ref>

<!-- prism:end -->
