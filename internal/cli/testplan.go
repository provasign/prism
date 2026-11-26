package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdTestPlan(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	targets := false
	callArgs := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--base":
			v, err := flagValue("test-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			callArgs["base"] = v
			i++
		case "--coverage":
			v, err := flagValue("test-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			cov, _ := callArgs["coverage"].([]any)
			callArgs["coverage"] = append(cov, v)
			i++
		case "--format":
			v, err := flagValue("test-plan", args, i)
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
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("test-plan", a)
			}
			if err := positionalDir("test-plan", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, "prism_test_plan", callArgs)
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
