package cli

import (
	"strings"
	"testing"
)

func TestMCPSpellingNamesTheCLIFlag(t *testing.T) {
	msg, ok := mcpSpelling("search", []string{`\bmatch\b`, "exhaustive=true"})
	if !ok || !strings.Contains(msg, "write --exhaustive (") {
		t.Fatalf("got %q", msg)
	}
	msg, ok = mcpSpelling("search", []string{"x", "max_results=50"})
	if !ok || !strings.Contains(msg, "write --limit 50") {
		t.Fatalf("got %q", msg)
	}
	if _, ok := mcpSpelling("search", []string{"timeout=5", "plain"}); ok {
		t.Fatal("a non-MCP key=value term must stay a search term")
	}
}

func TestLooksLikeRegex(t *testing.T) {
	for _, s := range []string{`\bmatch\b`, `foo.*bar`, `\w+`} {
		if !looksLikeRegex(s) {
			t.Errorf("%q should look like a regex", s)
		}
	}
	for _, s := range []string{"match", "Type.method", "a_b"} {
		if looksLikeRegex(s) {
			t.Errorf("%q should not look like a regex", s)
		}
	}
}

func TestFitShellOutputOnlyUnderClaudeCode(t *testing.T) {
	long := strings.Repeat("line of output\n", 3000) // 45 KB
	t.Setenv("CLAUDECODE", "")
	if got := fitShellOutput("query", long); got != long {
		t.Fatal("cut outside Claude Code")
	}
	t.Setenv("CLAUDECODE", "1")
	got := fitShellOutput("query", long)
	if len(got) > claudeShellOutputBudget || !strings.Contains(got, "more bytes not shown") || !strings.HasPrefix(long, strings.SplitN(got, "\n// prism:", 2)[0]) {
		t.Fatalf("bad cut: %d bytes, tail %q", len(got), got[len(got)-200:])
	}
	short := "small answer\n"
	if fitShellOutput("query", short) != short {
		t.Fatal("short answer changed")
	}
}

func TestCLIHintSpellingUsesFlags(t *testing.T) {
	in := "// +3 more matching line(s) not shown; exhaustive=true lists every line, paths=<file> shows one file's\n" +
		"SAMPLE: showing 5 of 9 matches. exhaustive=true lists every line; paths=/glob= narrows\n" +
		`  … 4 more members not listed; op=search with scope=symbols and paths=["a/B.java"] lists them`
	got := cliHintSpelling(in)
	for _, bad := range []string{"paths=", "exhaustive=true", "op=search"} {
		if strings.Contains(got, bad) {
			t.Fatalf("MCP spelling %q left in CLI text:\n%s", bad, got)
		}
	}
	for _, want := range []string{"--path <file> shows", "--exhaustive lists", "--path/--glob narrows", "prism search --scope symbols --path a/B.java lists"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}
