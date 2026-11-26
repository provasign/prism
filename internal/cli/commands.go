// Package cli implements the Prism command tree (flat dispatch, no cobra
// dependency — keeps Prism a true single binary with zero runtime deps).
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/version"
)

// outputFormat controls how CLI results are printed.
type outputFormat string

const (
	formatText outputFormat = "text"
	formatLean outputFormat = "lean"
	formatJSON outputFormat = "json"
)

// helpText is `prism help`: the header, every command's block in table
// order, then the init flag section.
// Set in init: commandTable's run functions read it, so a package-level
// initializer would be a cycle.
var helpText string

func init() { helpText = buildHelpText() }

func buildHelpText() string {
	var b strings.Builder
	b.WriteString(helpHeader)
	for _, c := range commandTable {
		b.WriteString(c.help)
	}
	b.WriteString(helpFooter)
	return b.String()
}

// Run is the CLI entry point. Returns the exit code.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Print(helpText)
		return 0
	}
	cmd, rest := args[0], args[1:]
	defer cliSpelledOutput(cmd, rest)()
	// `prism <cmd> --help` used to RUN <cmd>: no handler parsed -h/--help, so
	// `prism init --help` performed a full init (writing prism.yaml, nine
	// steering files and every project MCP registration) and `prism search
	// --help` searched for the string "--help". Answer here, once, before any
	// command body can execute. Only the flag spellings count — a bare `help`
	// argument stays a search term, so `prism search help` still searches.
	if cmd != "help" && helpRequested(rest) {
		if !knownCommand(cmd) {
			return unknownCommand(cmd)
		}
		fmt.Print(commandHelp(cmd))
		return 0
	}
	if c := lookupCommand(cmd); c != nil {
		return c.run(rest)
	}
	return unknownCommand(cmd)
}

// lookupCommand finds a command by name or alias.
func lookupCommand(name string) *command {
	for _, c := range commandTable {
		if c.name == name {
			return c
		}
		for _, a := range c.aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

func cmdHelp(rest []string) int {
	if len(rest) > 0 && rest[0] == "exit-codes" {
		fmt.Print(exitCodesHelp)
		return 0
	}
	fmt.Print(helpText)
	return 0
}

func cmdVersion(rest []string) int {
	fmt.Println("prism " + version.Version)
	if len(rest) > 0 && (rest[0] == "--verbose" || rest[0] == "-v") {
		// REL-02: say which Grove engine is linked, and whether a
		// replace or go.work swapped it for a local checkout.
		if g, ok := version.LinkedGrove(); ok {
			fmt.Println("grove " + g.String())
		} else {
			fmt.Println("grove unknown (no build info)")
		}
	}
	return 0
}

func cmdCleanupGlobal([]string) int {
	if _, err := removeLegacyGlobalMCPRegistrations(); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup-global:", err)
		return 1
	}
	return 0
}

func cmdTrustCommand(rest []string) int   { return cmdTrust("trust", rest) }
func cmdUntrustCommand(rest []string) int { return cmdTrust("untrust", rest) }

// commandNames lists every command and alias Run dispatches, in table order.
var commandNames = func() []string {
	var out []string
	for _, c := range commandTable {
		out = append(out, c.name)
		for _, a := range c.aliases {
			if !strings.HasPrefix(a, "-") {
				out = append(out, a)
			}
		}
	}
	return out
}()

func knownCommand(cmd string) bool {
	for _, c := range commandNames {
		if c == cmd {
			return true
		}
	}
	return false
}

// unknownCommand prints one line naming the nearest command, never the
// full help, and returns the usage exit code.
func unknownCommand(cmd string) int {
	msg := fmt.Sprintf("prism: unknown command %q", cmd)
	if near := nearestCommand(cmd); near != "" {
		msg += fmt.Sprintf("; did you mean %q?", near)
	}
	fmt.Fprintln(os.Stderr, msg+" (prism help lists commands)")
	return 2
}

// nearestCommand returns the command within edit distance 3 of cmd (or
// one that cmd prefixes), or "".
func nearestCommand(cmd string) string {
	best, bestD := "", 4
	for _, c := range commandNames {
		d := editDistance(cmd, c)
		if len(cmd) >= 3 && strings.HasPrefix(c, cmd) && d > 1 {
			d = 1
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// helpRequested reports whether a subcommand's own arguments ask for usage.
// Deliberately narrow: the flag spellings only, so a literal `help` argument
// is still a query term for search/lookup/references.
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// commandHelp is one command's usage block from the table; init (and its
// alias install) also gets the init flag section.
func commandHelp(name string) string {
	c := lookupCommand(name)
	if c == nil {
		return helpText
	}
	if c.name == "install" {
		c = lookupCommand("init")
	}
	if c.name != "init" {
		return c.help
	}
	section := strings.TrimPrefix(helpFooter, "\n")
	if j := strings.Index(section, "\n\n"); j >= 0 {
		section = section[:j]
	}
	return c.help + "\n" + section + "\n"
}

// prefixOnce labels an error with its operation without stuttering. Grove
// already prefixes these errors ("change-impact: query must be…"), so the
// naive `Fprintln("change-impact:", err)` printed the label twice — three
// times before the MCP layer stopped re-wrapping too.
func prefixOnce(op string, err error) string {
	// Errors carry MCP-spelled hints too (the ambiguous-name answer listed
	// {"op":"change_impact",...} calls, which a CLI agent cannot run).
	msg := cliHintSpelling(err.Error())
	if strings.HasPrefix(msg, op+":") {
		return msg
	}
	return op + ": " + msg
}

// rejectUnknownFlag is the shared guard every command parser calls from its
// default case. Silently dropping an unrecognized flag is the mechanism
// behind two shipped bugs (`--format` ignored by map/cycles, `--scope`
// ignored by search): the command runs, does something other than what was
// asked, and gives no indication why. 18 of 19 parsers still did this after
// the audit; now none do.
func rejectUnknownFlag(cmd, flag string) int {
	fmt.Fprintf(os.Stderr, "%s: unknown flag %q (see prism help)\n", cmd, flag)
	return 2
}
