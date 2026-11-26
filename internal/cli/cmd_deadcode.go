package cli

import (
	"fmt"
	"os"

	"github.com/provasign/prism/internal/core"
)

func cmdDeadCode(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	var roots []string
	it := newArgIter("dead-code", args)
	for it.next() {
		switch it.Flag {
		case "--roots":
			items, err := it.list()
			if err != nil {
				return usageError(err)
			}
			roots = append(roots, items...)
		case "--format":
			f, err := it.format()
			if err != nil {
				return usageError(err)
			}
			format = f
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("dead-code", &dir, &dirSet, a); err != nil {
			return usageError(err)
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
