
## Prism — context delivery

Prism indexes this repo's call and type graph. grep finds the same lines —
prism tells you which of them matter (file-level precision 0.51 -> 0.91 on
change tasks) and answers in one call where search-then-read takes several.

Route by the question. One call, and treat its result as final:

| Question | Call |
|---|---|
| where is X? | `prism_search(query="X")` — searches symbol names AND raw text. Several at once: `query=["X","Y"]`. Know where to look: `path=`, `glob=`, `files_only=true` |
| a literal string, message or config key | `prism_search(query="...", scope="text")` — pure grep, cheapest. Use it for TEXT; leave the default for code |
| EVERY site of X (rewrite them all, count them) | `prism_search(query="X", exhaustive=true)` — results are capped at 25 by default and a capped answer to a completeness question looks complete. Say `exhaustive`; add `files_only=true` to keep it cheap |
| X, plus the lines around it | `prism_search(query="X", context=N)` — one call instead of search-then-prism_read |
| read one function, or one file | `prism_lookup(name="pkg.Func")` / `prism_read` |
| give me the code for X, ready to edit | `prism_query(task="<label>", terms=["X"])` — keys on `terms`; the wording changes nothing |
| who breaks if I change X? | `prism_change_impact(query="Type.method")` |
| is my diff complete? | `prism_verify` |

Before editing an existing symbol, run `prism_change_impact`: declarations,
every override and implementation, all resolved callers, type-resolved in one
call. **Relay that set as-is** — re-verifying or filtering it through
grep/sed/scripts measurably drops real sites.

Bash-only (subagents, CI) — same verbs, add `--format text`:

    prism search <term>... [--path <file-or-dir>] [--exhaustive] --scope text
    prism query "<task>" --terms X
    prism change-impact 'Type.method'
    prism lookup <pkg.Func>   |   prism read <file>   |   prism verify --base <ref>

<!-- prism:end -->
