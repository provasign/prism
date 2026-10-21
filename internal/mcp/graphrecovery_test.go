package mcp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
)

// Audit 2026-10-20 #6: five identical graph requests produced one full result
// and four pointers. Compact change_impact has no force argument, so an agent
// whose copy was compacted away could never get the body back. An identical
// repeat right after a pointer must deliver in full.
func TestGraphRepeatAfterPointerDeliversInFull(t *testing.T) {
	h := NewHandler(config.Default(), t.TempDir(), nil)
	payload := map[string]any{"query": "Target", "callers": []map[string]any{{"file": "a.go", "body": strings.Repeat("source evidence ", 500)}}}
	args := map[string]any{"query": "Target"}
	var got []bool
	for n := 1; n <= 5; n++ {
		out := h.graphDedupe("op=change_impact", args, payload).(map[string]any)
		cached, _ := out["cached"].(bool)
		got = append(got, cached)
		if cached {
			note, _ := out["note"].(string)
			if strings.Contains(note, "still in context") || !strings.Contains(note, "repeat this identical request") {
				t.Errorf("pointer must offer recovery without assuming visible context: %q", note)
			}
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] && got[i-1] {
			t.Fatalf("two pointers in a row (deliveries %d,%d): %v", i, i+1, got)
		}
	}
	if got[0] || !got[1] || got[2] {
		t.Fatalf("want full, pointer, full; got %v", got)
	}
}

// Through the public compact gateway: the recovery must work with the
// existing change_impact argument schema.
func TestCompactChangeImpactRepeatRecoversFullResult(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\nfunc Target() {}\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "func CallerNumber%03dWithALongName() { Target() }\n", i)
	}
	srv := compactFixture(t, map[string]string{"go.mod": "module p\ngo 1.22\n", "a.go": b.String()})
	args := map[string]any{"name": "Target"}
	first := callCompact(t, srv, "change_impact", args)
	if strings.Contains(first, "[prism:cached]") || !strings.Contains(first, "CallerNumber119WithALongName") {
		t.Fatalf("first delivery must be full:\n%s", first)
	}
	second := callCompact(t, srv, "change_impact", args)
	if !strings.Contains(second, "[prism:cached]") {
		t.Skipf("result below pointer threshold; nothing to recover:\n%.400s", second)
	}
	if !strings.Contains(second, "repeat this identical request") {
		t.Fatalf("pointer must say how to recover:\n%s", second)
	}
	third := callCompact(t, srv, "change_impact", args)
	if strings.Contains(third, "[prism:cached]") || !strings.Contains(third, "CallerNumber119WithALongName") {
		t.Fatalf("identical repeat after a pointer must deliver in full:\n%.600s", third)
	}
}
