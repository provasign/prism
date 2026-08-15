
## Prism — code intelligence (ALWAYS use these tools)

Reaching for grep/rg/cat/sed? The prism call is the same request, better answered.
The translation is mechanical — all of these are already in your tool list:

    grep -rn "foo"                      ->  prism_search(query="foo", scope="text")
    grep -rn "foo" src/  (or one file)  ->  prism_search(query="foo", scope="text", path="src/")
    grep -rn "a\|b\|c"  (regex)         ->  prism_search(query="a|b|c", scope="text", regex=true)
    grep -C 5 / -A 10 (context lines)   ->  prism_search(..., context=5)
    grep -rl "foo"  (files only)        ->  prism_search(query="foo", scope="text", files=true)
    guessing names: grep a, grep b, ... ->  prism_query(task="...", terms=["a","b","c"]) -- one call,
                                            all candidates, graph expands from whichever lands
    grep "Foo("  (who calls Foo?)       ->  prism_change_impact(query="Foo") -- resolved call sites,
                                            complete set; do not re-verify with grep, that drops sites
    grep -A10 "def foo" / sed -n N,Mp   ->  prism_lookup(name="foo") -- whole body, no line guessing
    cat file / re-reading a file        ->  prism_read(file=...) -- a repeat read returns a one-line
                                            cached pointer, not the body; that is not an error

Piping ANOTHER command's output through grep (git log | grep x, pip list |
grep -i y) is fine and stays allowed — prism replaces searching FILES, not
filtering streams. No MCP session (Bash-only subagent)? Same verbs as CLI:
prism query/search/read/lookup/change-impact --format text.

<!-- prism:end -->
