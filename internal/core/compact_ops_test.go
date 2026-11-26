package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// CON-05: missing_implementations is reachable through the compact gateway
// as an op value, without a new tool; test_plan is not.
func TestCompactGatewayNewOps(t *testing.T) {
	name, args, err := expandCompactCall(map[string]any{"op": "missing_implementations",
		"args": map[string]any{"name": "Shape.area", "symbol_file": "shape.go"}})
	if err != nil || name != "prism_missing_implementations" || args["query"] != "Shape.area" || args["file"] != "shape.go" {
		t.Fatalf("missing_implementations: %s %v %v", name, args, err)
	}
	if _, _, err := expandCompactCall(map[string]any{"op": "test_plan", "args": map[string]any{}}); err == nil {
		t.Fatal("test_plan must stay CLI-only")
	}
	schemas := CompactToolSchemas()
	if len(schemas) != 1 {
		t.Fatalf("compact surface has %d tools, want 1", len(schemas))
	}
	raw, _ := json.Marshal(schemas)
	if !strings.Contains(string(raw), `"missing_implementations"`) {
		t.Error("schema lacks missing_implementations")
	}
	t.Logf("compact schema: %d bytes", len(raw))
}
