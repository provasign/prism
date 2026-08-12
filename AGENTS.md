
## Prism — code intelligence (ALWAYS use these tools)

grep, rg, and the built-in Grep tool are BLOCKED in this project — any attempt fails and wastes a turn. Do not try them, even out of habit. prism_search(scope="text") is the replacement (a real ripgrep pass); prism_query/prism_read for context. Read/Edit still work for files you already know you're editing.

prism_search/query/read/lookup/change_impact are already in your tool list --
call them directly, no lookup step. Locate a string/symbol -> prism_search(scope="text")
(a real ripgrep pass, same cost as grep). Bug/task with an anchor -> prism_query(task=...,
terms=[...]) -- terms is required, guess one keyword. Signature change, rename, or
"who breaks if I change X" -> prism_change_impact -- returns the complete site set in
one call; do not re-verify it with grep, that measurably drops real sites. A repeat
prism_read of an unchanged file returns a short cached-pointer line, not the body --
that is not an error. No MCP session (Bash-only subagent)? Same names as CLI verbs:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->
