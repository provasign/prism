package cli

import (
	"fmt"
	"os"
	"strings"
)

func cmdTestPlan(args []string) int {
	dir, dirSet := ".", false
	format := formatText
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
		case "--format":
			f, err := formatFlag("test-plan", args, i)
			if err != nil {
				return usageError(err)
			}
			if f != formatText && f != formatJSON {
				return usageError(fmt.Errorf("test-plan: --format must be text or json"))
			}
			format = f
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
		return 1
	}
	printToolResult("test-plan", out, text, rendered, format)
	return 0
}
