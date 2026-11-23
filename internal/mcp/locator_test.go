package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// CON-10: lookup and node take path:line like change-impact does; a bare
// file path given as a symbol is a usage error everywhere.
func TestPathLineAddressing(t *testing.T) {
	h, _ := planFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "core.go:3"})
	if err != nil || !strings.Contains(fmt.Sprint(out), "Wrapper") {
		t.Fatalf("lookup core.go:3: %v %v", err, out)
	}
	out, err = h.Invoke("prism_node", map[string]any{"name": "core.go:2"})
	if err != nil || !strings.Contains(fmt.Sprint(out), "Value") {
		t.Fatalf("node core.go:2: %v %v", err, out)
	}
	for _, tool := range []string{"prism_lookup", "prism_change_impact", "prism_missing_implementations"} {
		key := "query"
		if tool == "prism_lookup" {
			key = "name"
		}
		_, err := h.Invoke(tool, map[string]any{key: "core.go"})
		if err == nil || !strings.Contains(err.Error(), "is a file, not a symbol") {
			t.Errorf("%s core.go: %v", tool, err)
		}
	}
	if _, err := h.Invoke("prism_lookup", map[string]any{"name": "core.go:99"}); err == nil || !strings.Contains(err.Error(), "no symbol named at that line") {
		t.Errorf("lookup core.go:99: %v", err)
	}
}
