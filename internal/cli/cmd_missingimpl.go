package cli

import (
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdMissingImplementations(args []string) int {
	args, rc := positionalsFirst("missing-implementations", "a symbol", "prism missing-implementations Type.method [dir]", 1, args, "--format", "--file", "--path", "--signature")
	if rc != 0 {
		return rc
	}
	query := args[0]
	dir, dirSet := ".", false
	format := formatText
	file, signature := "", ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--signature":
			v, err := flagValue("missing-implementations", args, i)
			if err != nil {
				return usageError(err)
			}
			signature = v
			i++
		case "--format":
			f, err := formatFlag("missing-implementations", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages, as
			// change-impact does: only types declared in a matching file seed.
			v, err := flagValue("missing-implementations", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("missing-implementations", a)
			}
			if err := positionalDir("missing-implementations", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.MissingImplementationsRequest{Query: query, File: file, Signature: signature})
	if err != nil {
		return reportToolError("missing-implementations", err, format)
	}
	printOutput(out, format)
	return 0
}
