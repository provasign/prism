package cli

import (
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdRenamePlan(args []string) int {
	args, rc := positionalsFirst("rename-plan", "a symbol and a new name", "prism rename-plan Type.method NewName [dir]", 2, args, "--format", "--file", "--path")
	if rc != 0 {
		return rc
	}
	query, newName := args[0], args[1]
	dir, dirSet := ".", false
	format := formatText
	file := ""
	allowCollision := false
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--format":
			f, err := formatFlag("rename-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages, as
			// change-impact does: only types declared in a matching file seed.
			v, err := flagValue("rename-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		case "--allow-collision":
			allowCollision = true
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("rename-plan", a)
			}
			if err := positionalDir("rename-plan", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.RenamePlanRequest{Query: query, NewName: newName, File: file, AllowCollision: allowCollision})
	if err != nil {
		return reportToolError("rename-plan", err, format)
	}
	printOutput(out, format)
	// A collision without --allow-collision returns no plan: exit 1.
	if m := toMap(out); m["status"] == "collision" && !allowCollision {
		return 1
	}
	return 0
}
