package cli

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
)

// noSpellCommands speak a protocol on stdout, serve a UI, or ask the user
// questions; their output is never rewritten. The rewrite reads whole
// lines, so a prompt (no newline until the user answers) stayed in the
// pipe: `prism init` showed its harness list and then waited with no
// question, and each question appeared only after it was answered.
var noSpellCommands = map[string]bool{"mcp": true, "serve": true, "link-server": true, "compact": true, "graph": true,
	"init": true, "workspace": true}

// cliSpelledOutput rewrites MCP spellings ("op=read file=...", "prism_lookup",
// "include_tests=true") in a text-format command's stdout and stderr into the
// CLI commands an agent can run (CON-03). Lines carrying repository text
// (numbered source, file:line rows, fenced code) pass through unchanged.
// It returns a restore function that flushes and puts the streams back.
func cliSpelledOutput(cmd string, args []string) func() {
	if noSpellCommands[cmd] {
		return func() {}
	}
	for i, a := range args {
		if a == "--json" || (a == "--format" && i+1 < len(args) && args[i+1] == "json") {
			return func() {}
		}
	}
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err1 := os.Pipe()
	errR, errW, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		return func() {}
	}
	os.Stdout, os.Stderr = outW, errW
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); spellLines(outR, origOut) }()
	go func() { defer wg.Done(); spellLines(errR, origErr) }()
	return func() {
		_ = outW.Close()
		_ = errW.Close()
		wg.Wait()
		os.Stdout, os.Stderr = origOut, origErr
	}
}

// cliHintSpellingLines applies cliHintSpelling to the note lines of a
// rendered answer and leaves repository text (numbered source, file:line
// rows, fenced code) unchanged.
func cliHintSpellingLines(text string) string {
	lines := strings.SplitAfter(text, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
		case inFence, sourceRow.MatchString(line):
		default:
			lines[i] = cliHintSpelling(line)
		}
	}
	return strings.Join(lines, "")
}

func spellLines(r io.Reader, w io.Writer) {
	br := bufio.NewReaderSize(r, 64*1024)
	inFence := false
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, "```"):
				inFence = !inFence
			case inFence, sourceRow.MatchString(line):
			default:
				line = cliHintSpelling(line)
			}
			_, _ = io.WriteString(w, line)
		}
		if err != nil {
			return
		}
	}
}
