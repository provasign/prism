package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// testCodeKind says whether a symbol is test code: "test" for a runnable
// test, "helper" for other code that only tests use, "" otherwise. File
// paths decide for test files; elsewhere the symbol's own attributes do:
// Rust #[test] functions and #[cfg(test)] `mod tests`, Python test_*
// functions, JUnit-style @Test methods. test-plan used to find no
// candidates for a Rust inline test in src/lib.rs (BUG-15).
type testCodeClassifier struct {
	root  string
	lines map[string][]string
}

var (
	rustTestAttr = regexp.MustCompile(`^#\[(\w+::)*(test|rstest|test_case|quickcheck|proptest)\b`)
	javaTestAnno = regexp.MustCompile(`^@(Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate)\b`)
)

func (c *testCodeClassifier) kind(s grove.SymbolRecord) string {
	p := normalizePath(s.FilePath)
	if s.Kind != "function" && s.Kind != "method" && s.Kind != "constructor" {
		if isTestFilePath(p) {
			return "helper"
		}
		return ""
	}
	if isTestFilePath(p) {
		if (s.Language == "go" || strings.HasSuffix(p, ".go")) && !strings.HasPrefix(s.Name, "Test") && !strings.HasPrefix(s.Name, "Example") &&
			!strings.HasPrefix(s.Name, "Fuzz") && !strings.HasPrefix(s.Name, "Benchmark") {
			return "helper"
		}
		return "test"
	}
	switch {
	case strings.HasSuffix(p, ".rs"):
		if c.attributed(s, rustTestAttr, "#[") {
			return "test"
		}
		qn := displayQN(s)
		if strings.HasPrefix(qn, "tests.") || strings.Contains(qn, ".tests.") {
			return "helper"
		}
	case strings.HasSuffix(p, ".py"):
		if strings.HasPrefix(s.Name, "test_") || strings.HasPrefix(displayQN(s), "Test") && strings.HasPrefix(s.Name, "test") {
			return "test"
		}
	case strings.HasSuffix(p, ".java"), strings.HasSuffix(p, ".kt"):
		if c.attributed(s, javaTestAnno, "@") {
			return "test"
		}
	}
	return ""
}

// attributed reports whether one of the attribute or annotation lines
// directly above the symbol's first line matches re.
func (c *testCodeClassifier) attributed(s grove.SymbolRecord, re *regexp.Regexp, prefix string) bool {
	p := normalizePath(s.FilePath)
	lines, ok := c.lines[p]
	if !ok {
		if data, err := os.ReadFile(filepath.Join(c.root, p)); err == nil {
			lines = strings.Split(string(data), "\n")
		}
		if c.lines == nil {
			c.lines = map[string][]string{}
		}
		c.lines[p] = lines
	}
	for i := s.Span.Start - 1; i >= 0 && i < len(lines); i-- {
		t := strings.TrimSpace(lines[i])
		if i == s.Span.Start-1 {
			// The symbol's own line may start with the attribute.
			if re.MatchString(t) {
				return true
			}
			continue
		}
		switch {
		case re.MatchString(t):
			return true
		case strings.HasPrefix(t, prefix), strings.HasPrefix(t, "//"), strings.HasPrefix(t, "///"), t == "":
			continue
		default:
			return false
		}
	}
	return false
}
