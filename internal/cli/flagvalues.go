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
	if outputFormat(v) == formatLean {
		// lean was larger than text (change-impact 3.6 KB vs 2.1 KB); it is
		// an alias of text now (CON-01).
		return formatText, err
	}
	return outputFormat(v), err
}

// takeFormat removes --format X and --json from args and returns the
// output format, text unless asked otherwise (CON-01).
func takeFormat(cmd string, args []string) ([]string, outputFormat, int) {
	format := formatText
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			format = formatJSON
		case "--format":
			f, err := formatFlag(cmd, args, i)
			if err != nil {
				return nil, "", usageError(err)
			}
			format = f
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, format, 0
}

// argIter walks one command's arguments for its parser: next stops at each
// flag, collecting positionals on the way, and everything after "--" is
// positional. The value methods are the helpers above applied to the current
// flag, and consume its value, so a parser is a switch on it.Flag with no
// index arithmetic (ENG-03), and the messages stay those of the helpers.
type argIter struct {
	cmd  string
	args []string
	i    int
	// Flag is the flag next stopped at.
	Flag string
	// Pos holds the positional arguments seen so far, in order.
	Pos []string
}

func newArgIter(cmd string, args []string) *argIter {
	return &argIter{cmd: cmd, args: args, i: -1}
}

// next advances to the next flag and reports whether there is one.
func (it *argIter) next() bool {
	for it.i++; it.i < len(it.args); it.i++ {
		a := it.args[it.i]
		if a == "--" {
			it.Pos = append(it.Pos, it.args[it.i+1:]...)
			it.i = len(it.args)
			return false
		}
		if strings.HasPrefix(a, "-") {
			it.Flag = a
			return true
		}
		it.Pos = append(it.Pos, a)
	}
	return false
}

// unknown reports the current flag as unknown and returns exit code 2.
func (it *argIter) unknown() int { return rejectUnknownFlag(it.cmd, it.Flag) }

// value returns the current flag's value (flagValue).
func (it *argIter) value() (string, error) {
	v, err := flagValue(it.cmd, it.args, it.i)
	it.i++
	return v, err
}

// rawValue returns the argument after the current flag as written, even
// empty or flag-shaped, and false when there is none. For parsers with their
// own message for a missing value.
func (it *argIter) rawValue() (string, bool) {
	if it.i+1 >= len(it.args) {
		it.i++
		return "", false
	}
	it.i++
	return it.args[it.i], true
}

// positiveInt returns the current flag's value as an integer >= 1.
func (it *argIter) positiveInt() (int, error) {
	n, err := positiveIntFlag(it.cmd, it.args, it.i)
	it.i++
	return n, err
}

// list returns the current flag's comma-separated values.
func (it *argIter) list() ([]string, error) {
	items, err := listFlag(it.cmd, it.args, it.i)
	it.i++
	return items, err
}

// choice returns the current flag's value when it is one of choices.
func (it *argIter) choice(choices ...string) (string, error) {
	v, err := choiceFlag(it.cmd, it.args, it.i, choices...)
	it.i++
	return v, err
}

// format parses the current --format flag's value.
func (it *argIter) format() (outputFormat, error) {
	f, err := formatFlag(it.cmd, it.args, it.i)
	it.i++
	return f, err
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

// positionalsFirst reorders args so the positional arguments come first,
// in order, followed by the flags with their values. Handlers that read
// args[0] (and args[1]) as their positionals then never take a flag, or a
// flag's value, for one: `change-impact --format text` used to query
// "--format" and index a directory named "text" (BUG-17). valueFlags are
// the flags that take a value. With fewer than need positionals it prints
// "<cmd> needs <what>" and the usage line, and returns exit code 2.
func positionalsFirst(cmd, what, usage string, need int, args []string, valueFlags ...string) ([]string, int) {
	takesValue := map[string]bool{}
	for _, f := range valueFlags {
		takesValue[f] = true
	}
	var pos, flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if takesValue[a] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	if len(pos) < need {
		fmt.Fprintf(os.Stderr, "%s needs %s\nusage: %s\n", cmd, what, usage)
		return nil, 2
	}
	return append(pos, flags...), 0
}
