package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Flag-value parsing shared by every subcommand parser. A flag whose value is
// missing, malformed or out of bounds used to be skipped in silence, so the
// command answered a different request with exit 0: `read --from 10 --to 5`
// returned 10..end, `read --from banana` the file head, `search --path` the
// whole repository (stack audit 2026-10-20, finding 8). Each helper reports a
// usage error instead; parsers return usageError(err), exit 2 like an unknown
// flag, before any index is opened.

// flagValue returns the value after the flag at args[i]. A missing value, an
// empty one, or the next --flag in its place is an error.
func flagValue(cmd string, args []string, i int) (string, error) {
	if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
		return "", fmt.Errorf("%s: %s requires a value", cmd, args[i])
	}
	return args[i+1], nil
}

// positiveIntFlag returns the value after args[i] as an integer >= 1.
func positiveIntFlag(cmd string, args []string, i int) (int, error) {
	v, err := flagValue(cmd, args, i)
	if err != nil {
		return 0, fmt.Errorf("%s: %s requires a positive integer", cmd, args[i])
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: %s requires a positive integer, got %q", cmd, args[i], v)
	}
	return n, nil
}

// listFlag returns the comma-separated values after args[i]; at least one
// non-empty item is required.
func listFlag(cmd string, args []string, i int) ([]string, error) {
	v, err := flagValue(cmd, args, i)
	if err != nil {
		return nil, fmt.Errorf("%s: %s requires a comma-separated list", cmd, args[i])
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: %s requires a comma-separated list, got %q", cmd, args[i], v)
	}
	return out, nil
}

// choiceFlag returns the value after args[i] when it is one of choices.
func choiceFlag(cmd string, args []string, i int, choices ...string) (string, error) {
	want := strings.Join(choices, "|")
	v, err := flagValue(cmd, args, i)
	if err != nil {
		return "", fmt.Errorf("%s: %s requires %s", cmd, args[i], want)
	}
	for _, c := range choices {
		if v == c {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: %s wants %s, got %q", cmd, args[i], want, v)
}

// formatFlag parses --format text|lean|json.
func formatFlag(cmd string, args []string, i int) (outputFormat, error) {
	v, err := choiceFlag(cmd, args, i, string(formatText), string(formatLean), string(formatJSON))
	return outputFormat(v), err
}

// usageError prints a flag-value error and returns the usage exit code.
func usageError(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 2
}

// positionalDir records the one positional directory a command takes; a
// second positional used to replace the first in silence.
func positionalDir(cmd string, dir *string, set *bool, a string) error {
	if *set {
		return fmt.Errorf("%s: takes one directory, got %q and %q", cmd, *dir, a)
	}
	*dir, *set = a, true
	return nil
}

// dirOnlyArgs parses the arguments of a command that takes at most one
// directory and the given boolean flags. dirArg skipped anything starting
// with "-", so a mistyped flag ran the command with its defaults.
func dirOnlyArgs(cmd string, args []string, flags ...string) (dir string, set map[string]bool, rc int) {
	dir, set = ".", map[string]bool{}
	positional := 0
outer:
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				set[f] = true
				continue outer
			}
		}
		if strings.HasPrefix(a, "-") {
			return "", nil, rejectUnknownFlag(cmd, a)
		}
		positional++
		if positional > 1 {
			return "", nil, usageError(fmt.Errorf("%s: takes one directory, got %q and %q", cmd, dir, a))
		}
		dir = a
	}
	return dir, set, 0
}
