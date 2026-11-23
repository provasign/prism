package mcp

import (
	"strings"
	"testing"
)

// GAP-09: a name found in a language without a graph is reported, so
// "did not look" never reads as "not there".
func TestUnsupportedMentionLine(t *testing.T) {
	line := unsupportedMentionLine("Render", []mentionLine{{file: "app/view.rb", line: 3}, {file: "app/x.rb", line: 1}, {file: "a.go", line: 2}, {file: "c.vue", line: 2}})
	if !strings.HasPrefix(line, unsupportedMentionPrefix) || !strings.Contains(line, "2 Ruby file(s)") || !strings.Contains(line, "1 Vue file(s)") {
		t.Fatalf("line: %q", line)
	}
	if unsupportedMentionLine("Render", []mentionLine{{file: "a.go", line: 2}}) != "" {
		t.Fatal("a Go-only mention must not report unsupported files")
	}
}
