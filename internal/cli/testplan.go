package cli

import (
	"fmt"
	"os"

	"github.com/provasign/prism/internal/core"
)

func cmdTestPlan(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	targets := false
	var req core.TestPlanRequest
	it := newArgIter("test-plan", args)
	for it.next() {
		switch it.Flag {
		case "--base":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			req.Base = v
		case "--coverage":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			req.Coverage = append(req.Coverage, v)
		case "--format":
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			switch outputFormat(v) {
			case "targets":
				// GAP-06: one runner-addressed target per line.
				targets = true
			case formatText, formatJSON:
				format = outputFormat(v)
			default:
				return usageError(fmt.Errorf("test-plan: --format must be text, json or targets"))
			}
		default:
			return it.unknown()
		}
	}
	for _, a := range it.Pos {
		if err := positionalDir("test-plan", &dir, &dirSet, a); err != nil {
			return usageError(err)
		}
	}
	out, text, rendered, err := invokeToolText(dir, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-plan:", err)
		return 2
	}
	if targets {
		for _, t := range core.TestPlanTargets(out) {
			fmt.Println(t)
		}
		return 0
	}
	printToolResult("test-plan", out, text, rendered, format)
	return 0
}
