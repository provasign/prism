package cli

// prism_query --format text printed FIVE FILE PATHS while the MCP surface
// delivered the full assembled context — the files_only branch fired on the
// query payload ("files" present, "symbols" absent) and the entire "content"
// field was silently discarded (measured 2026-08-26: 21KB context reduced to
// 228 bytes of paths). Bash-only consumers — exactly who the steering's bash
// table routes here — got paths from a tool whose whole output is context.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQueryTextPrintsContentNotJustFiles(t *testing.T) {
	var m map[string]any
	json.Unmarshal([]byte(`{"content":"**Context for: x**\nline of source\n",
		"files":["a.java","b.java"],"deliveredTokens":42,"symbolCount":3,
		"delivery":"source","textBackend":"rg","textMatches":[]}`), &m)
	got := captureStdout(func() { printTextOutput(m) })
	if !strings.Contains(got, "line of source") {
		t.Fatalf("query content discarded; got %q", got)
	}
}

func TestReadTextKeepsHeader(t *testing.T) {
	var m map[string]any
	json.Unmarshal([]byte(`{"file":"a.java","strategy":"verbatim","content":"1\tpackage x;\n"}`), &m)
	got := captureStdout(func() { printTextOutput(m) })
	if !strings.Contains(got, "a.java") {
		t.Fatalf("read header lost: %q", got)
	}
	if !strings.Contains(got, "package x;") {
		t.Fatalf("read content lost: %q", got)
	}
}

func TestFilesOnlyDeliveryStillPrintsPaths(t *testing.T) {
	var m map[string]any
	json.Unmarshal([]byte(`{"files":["a.java","b.java"]}`), &m)
	got := captureStdout(func() { printTextOutput(m) })
	if !strings.Contains(got, "a.java") || !strings.Contains(got, "b.java") {
		t.Fatalf("files_only regressed: %q", got)
	}
}
