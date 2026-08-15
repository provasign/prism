
## Prism — code intelligence (ALWAYS use these tools)

prism_search/query/read/lookup/change_impact are already in your tool list --
call them directly, no lookup step. Locate a string/symbol -> prism_search(scope="text")
(a real ripgrep pass, same cost as grep). Bug/task with an anchor -> prism_query(task=...,
terms=[...]) -- terms is required, guess one keyword. Signature change, rename, or
"who breaks if I change X" -> prism_change_impact -- returns the complete site set in
one call; do not re-verify it with grep, that measurably drops real sites. A repeat
prism_read of an unchanged file returns a short cached-pointer line, not the body --
that is not an error. Three concrete substitutions, not just "prefer prism":
about to grep several candidate names ("a\|b\|c", unsure which is right)? that is
prism_query(terms=[a,b,c]) in one call, not several rounds of narrowing. About to
grep a name followed by "(" to find who constructs/calls it? that is
prism_change_impact or prism_query's caller list -- resolved call sites, not a
paren-guess that also matches definitions and comments. About to grep a def line
then guess how many lines of context to print (-A10, sed -n 'N,Mp')? that is
prism_lookup -- the whole symbol, no line-count guessing, never cut off early.
No MCP session (Bash-only subagent)? Same names as CLI verbs:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->
