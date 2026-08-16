
## Prism — context delivery

Prism indexes this repo's call and type graph. grep finds the same lines —
prism tells you which of them matter (file-level precision 0.51 -> 0.91 on
change tasks) and answers in one call where search-then-read takes several.

Route by the question. One call, and treat its result as final:

| Question | Call |
|---|---|
| where is X? | `prism_search(query="X")` — `scope="text"` is a plain grep, the cheapest option. Several things at once: `query=["X","Y","Z"]`. Already know where to look: `path="pkg/file.go"`, `glob="*.py"`, `files_only=true` |
| read one function, or one file | `prism_lookup(name="pkg.Func")` / `prism_read` |
| give me the code for X, ready to edit | `prism_query(task="<label>", terms=["X"])` — keys on `terms`; the wording changes nothing |
| who breaks if I change X? | `prism_change_impact(query="Type.method")` |
| is my diff complete? | `prism_verify` |

Before editing an existing symbol, run `prism_change_impact`: declarations,
every override and implementation, all resolved callers, type-resolved in one
call. **Relay that set as-is** — re-verifying or filtering it through
grep/sed/scripts measurably drops real sites.

Bash-only (subagents, CI) — same verbs, add `--format text`:

    prism search <term>... [--path <file-or-dir>] --scope text
    prism query "<task>" --terms X
    prism change-impact 'Type.method'
    prism lookup <pkg.Func>   |   prism read <file>   |   prism verify --base <ref>

No prism_* in your tool list? They are deferred, not absent — load them once
with `ToolSearch("select:prism_search,prism_query,prism_change_impact")`.
If you can already see them, skip this: calling ToolSearch for tools you
already have costs a turn and returns nothing new.

<!-- prism:end -->
