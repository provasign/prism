package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/ranking"
)

func TestToolSearchDefaultCompletesSmallExactResult(t *testing.T) {
	h := newTestHandler(t)
	write := func(name string, start, count int) {
		t.Helper()
		var body strings.Builder
		body.WriteString("package fixture\n\nfunc F() {\n")
		for i := 0; i < count; i++ {
			fmt.Fprintf(&body, "\t// Phase4Needle %02d\n", start+i)
		}
		body.WriteString("}\n")
		if err := os.WriteFile(filepath.Join(h.Root, name), []byte(body.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.go", 1, 22)
	write("beta.go", 23, 20)

	out, err := h.Invoke("prism_search", map[string]any{
		"query": "Phase4Needle", "scope": "text", "context": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["countComplete"] != true || m["resultsComplete"] != true || m["truncated"] != false {
		t.Fatalf("wrong completeness fields: %#v", m)
	}
	if m["totalHits"] != 42 || m["filesMatched"] != 2 {
		t.Fatalf("count = %v in %v files, want 42 in 2", m["totalHits"], m["filesMatched"])
	}
	text, ok := RenderSearchText(m)
	if !ok {
		t.Fatal("adaptive result did not render as text")
	}
	if got := strings.Count(text, "Phase4Needle"); got != 42 {
		t.Fatalf("rendered %d matching lines, want all 42:\n%s", got, text)
	}
	if !strings.Contains(text, "COMPLETE — 42 exact matches across 2 files") {
		t.Fatalf("missing exact completion headline:\n%s", text)
	}
	if strings.Contains(text, "AT LEAST") || strings.Contains(text, "exhaustive=true") {
		t.Fatalf("complete small result still asks for a follow-up:\n%s", text)
	}
}

func TestToolSearchLargeSampleReportsExactCount(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "many.txt"),
		[]byte(strings.Repeat("LargeNeedle "+strings.Repeat("long line ", 100)+"\n", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "LargeNeedle", "scope": "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["countComplete"] != true || m["resultsComplete"] == true || m["truncated"] != true {
		t.Fatalf("wrong completeness fields: %#v", m)
	}
	if m["totalHits"] != 100 || m["filesMatched"] != 1 {
		t.Fatalf("count = %v in %v files, want 100 in 1", m["totalHits"], m["filesMatched"])
	}
	warning, _ := m["warning"].(string)
	if !strings.Contains(warning, "showing 25 of 100 matches") || strings.Contains(warning, "AT LEAST") {
		t.Fatalf("large-result warning does not carry its exact count: %q", warning)
	}
}

func TestToolSearchCompletesSmallPayloadAboveFormerCountThreshold(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "many.txt"),
		[]byte(strings.Repeat("ShortNeedle\n", 66)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_search", map[string]any{
		"query": "ShortNeedle", "scope": "text", "context": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["countComplete"] != true || m["resultsComplete"] != true || m["truncated"] != false {
		t.Fatalf("wrong completeness fields: %#v", m)
	}
	text, ok := RenderSearchText(m)
	if !ok {
		t.Fatal("adaptive result did not render as text")
	}
	if got := strings.Count(text, "ShortNeedle"); got != 66 {
		t.Fatalf("rendered %d matching lines, want all 66:\n%s", got, text)
	}
	if !strings.Contains(text, "COMPLETE — 66 exact matches") || strings.Contains(text, "exhaustive=true") {
		t.Fatalf("small payload did not render as complete:\n%s", text)
	}
}

func TestToolSearchDefaultContextCompletesOverTwoHundredShortHits(t *testing.T) {
	for _, count := range []int{210, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := newTestHandler(t)
			if err := os.WriteFile(filepath.Join(h.Root, "a"), []byte(strings.Repeat("X\n", count)), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := h.Invoke("prism_search", map[string]any{"query": "X", "scope": "text"})
			if err != nil {
				t.Fatal(err)
			}
			m := out.(map[string]any)
			if m["resultsComplete"] != true || m["totalHits"] != count || m["truncated"] == true {
				t.Fatalf("default-context search was not complete: %#v", m)
			}
			text, ok := RenderSearchText(m)
			if !ok || strings.Count(text, ": X\n") != count || strings.Contains(text, "SAMPLE") {
				t.Fatalf("complete search did not deliver all %d hits:\n%s", count, text)
			}
			if tokens := ranking.EstimateTokens(text); tokens > 4000 {
				t.Fatalf("complete search exceeded its delivery budget: %d tokens", tokens)
			} else {
				t.Logf("complete %d-hit default-context response: %d estimated tokens", count, tokens)
			}
		})
	}
}
