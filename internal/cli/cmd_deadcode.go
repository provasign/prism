package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdDeadCode(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	var roots []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--roots":
			items, err := listFlag("dead-code", args, i)
			if err != nil {
				return usageError(err)
			}
			roots = append(roots, items...)
			i++
		case "--format":
			f, err := formatFlag("dead-code", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("dead-code", a)
			}
			if err := positionalDir("dead-code", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, err := invokeTool(dir, core.DeadCodeRequest{Roots: roots})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dead-code:", err)
		return exitInternal
	}
	printOutput(out, format)
	return 0
}
