package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// A name-matched seed can still contain a deep text match. If its source
// window is budget-clamped, the matched line must survive as bounded evidence.
func TestQueryPreservesDeepCommentOutsideNamedSeedWindow(t *testing.T) {
	root := t.TempDir()
	var source strings.Builder
	source.WriteString("# Comment retrieval probe\ndef orchid_deep_only(value):\n")
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&source, "    value += %d\n", i)
	}
	source.WriteString("    # ORCHID_DEEP_ONLY: retain the rounding convention.\n") // line 103
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&source, "    value -= %d\n", i)
	}
	source.WriteString("    return value\n")
	if err := os.WriteFile(filepath.Join(root, "large.py"), []byte(source.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), root, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_query", map[string]any{
		"task": "inspect rounding convention", "terms": []string{"ORCHID_DEEP_ONLY"}, "budget": 150,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := out.(map[string]any)
	content, _ := response["content"].(string)
	if strings.Contains(content, "103\t    # ORCHID_DEEP_ONLY") {
		t.Fatalf("fixture did not clamp the deep comment from the source window:\n%s", content)
	}
	evidence := fmt.Sprint(response["textMatches"])
	for _, want := range []string{"ORCHID_DEEP_ONLY: retain", "value += 100", "value -= 1"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("missing bounded match evidence %q: %s", want, evidence)
		}
	}
	// A truncated first result must not mark the whole file cached. A roomy
	// query should deliver the deep line as source, without repeating it in
	// the separate text-match section.
	out, err = h.Invoke("prism_query", map[string]any{
		"task": "inspect rounding convention", "terms": []string{"ORCHID_DEEP_ONLY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = out.(map[string]any)
	content, _ = response["content"].(string)
	if !strings.Contains(content, "103\t    # ORCHID_DEEP_ONLY") || strings.Contains(content, "// [prism:cached] large.py") {
		t.Fatalf("second query did not deliver the previously truncated deep line:\n%s", content)
	}
	if evidence := fmt.Sprint(response["textMatches"]); strings.Contains(evidence, "ORCHID_DEEP_ONLY: retain") {
		t.Fatalf("matched line already shown in source was repeated as text evidence: %s", evidence)
	}
}
