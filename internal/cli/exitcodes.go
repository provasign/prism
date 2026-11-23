package cli

import (
	"fmt"
	"strings"
)

// Exit codes, one table for every command (CON-04). Printed by
// `prism help exit-codes`.
const (
	exitOK        = 0  // answered
	exitFinding   = 1  // a finding: verify incomplete, arch violation, rename collision
	exitUsage     = 2  // usage error
	exitDegraded  = 3  // answered, but compiler-backed analysis did not run
	exitNotFound  = 4  // the symbol, file or component has no match
	exitAmbiguous = 5  // ambiguous: a choice was returned
	exitInternal  = 70 // the tool failed
)

const exitCodesHelp = `prism exit codes
  0   answered
  1   finding: verify incomplete, arch violation, rename-plan collision
  2   usage error (bad flag, missing argument, not a git repository)
  3   degraded: answered, but compiler-backed analysis did not run (index)
  4   not found: no symbol, file or component by that name
      (lookup, node, resolve, edges, references, read, change-impact,
      missing-implementations, rename-plan, map --component)
  5   ambiguous: several symbols match; a choice was printed
  70  internal failure
search and query exit 0 with a "no matches" line when nothing matches.
`

// notFoundPhrases mark an error as "no such name" rather than a failure.
var notFoundPhrases = []string{
	"no method or function named", "no symbol named", "no type named",
	"not found", "no component ", "no indexed implementation", "no member named",
}

// exitForError maps a tool error to its exit code.
func exitForError(err error) int {
	if err == nil {
		return exitOK
	}
	msg := err.Error()
	for _, p := range []string{"directory does not exist", "is not a directory", "requires a value", "unknown flag"} {
		if strings.Contains(msg, p) {
			return exitUsage
		}
	}
	if strings.Contains(msg, "is ambiguous") {
		return exitAmbiguous
	}
	for _, p := range notFoundPhrases {
		if strings.Contains(msg, p) {
			return exitNotFound
		}
	}
	return exitInternal
}

// exitForAnswer returns exitNotFound when a lookup-style answer says the
// name or file does not exist, else exitOK. These answers used to exit 0
// (CON-04).
func exitForAnswer(cmd string, out any) int {
	m := toMap(out)
	if m == nil {
		return exitOK
	}
	switch cmd {
	case "lookup", "node", "edges":
		if matched, ok := m["matched"].(bool); ok && !matched {
			return exitNotFound
		}
		if results, ok := m["results"].([]any); ok && len(results) > 0 {
			for _, r := range results {
				rm, _ := r.(map[string]any)
				inner, _ := rm["result"].(map[string]any)
				if matched, ok := inner["matched"].(bool); !ok || matched {
					return exitOK
				}
			}
			return exitNotFound
		}
	case "resolve":
		if n, ok := m["count"].(float64); ok && n == 0 {
			return exitNotFound
		}
		if n, ok := m["count"].(int); ok && n == 0 {
			return exitNotFound
		}
	case "references":
		if fmt.Sprint(m["definitions"]) == "0" && fmt.Sprint(m["count"]) == "0" && m["resolved"] != true {
			return exitNotFound
		}
	case "read":
		if found, ok := m["found"].(bool); ok && !found {
			return exitNotFound
		}
	}
	return exitOK
}
