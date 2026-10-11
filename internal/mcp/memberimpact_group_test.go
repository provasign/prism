package mcp

import (
	"fmt"
	"strings"
	"testing"
)
func TestMemberImpactGroupsLargeAccessLists(t *testing.T) {
	var acc []any
	for i := 0; i < 250; i++ {
		acc = append(acc, map[string]any{"filePath": fmt.Sprintf("f%d.ts", i%5), "line": i + 1, "access": "call", "isTest": true, "text": "x.request(y)"})
	}
	text, ok := FormatMemberImpactText(map[string]any{"memberKind": "field", "query": "Hono.request", "totalSites": 250, "accesses": acc})
	if !ok {
		t.Fatal("not rendered")
	}
	if !strings.Contains(text, "250 sites in 5 files, grouped per file") || strings.Count(text, "\n  f") != 5 {
		t.Errorf("want one line per file:\n%s", text)
	}
	if strings.Contains(text, "x.request(y)") {
		t.Error("grouped form should drop per-site evidence text")
	}
	for i := 1; i <= 250; i++ {
		if !strings.Contains(text, fmt.Sprintf(" %d", i)) {
			t.Fatalf("line %d missing from grouped form", i)
		}
	}
	// Past the budget, only per-file counts are kept.
	acc = nil
	for i := 0; i < 6000; i++ {
		acc = append(acc, map[string]any{"filePath": fmt.Sprintf("dir/file%04d.ts", i%600), "line": i + 1, "access": "call"})
	}
	text, _ = FormatMemberImpactText(map[string]any{"memberKind": "field", "query": "Hono.request", "totalSites": 6000, "accesses": acc})
	if !strings.Contains(text, "per-file counts only") || len(text) > 40000 {
		t.Errorf("counts-only form expected, got %d chars", len(text))
	}
}
