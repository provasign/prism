package mcp

import (
	"context"
	"strings"
	"testing"
)

// Validation must reject non-identifier newName BEFORE touching the engine —
// the CLI positional footgun is `prism rename-plan 'Foo.bar' ./src` (user
// forgot NewName and passed a directory).
func TestToolRenamePlanValidation(t *testing.T) {
	h := &Handler{} // engine never reached on validation errors
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"newName": "x"}, "required"},
		{map[string]any{"query": "Foo.bar"}, "required"},
		{map[string]any{"query": "Foo.bar", "newName": "./src"}, "bare identifier"},
		{map[string]any{"query": "Foo.bar", "newName": "a b"}, "bare identifier"},
		{map[string]any{"query": "Foo.bar", "newName": "1abc"}, "bare identifier"},
	}
	for _, tc := range cases {
		_, err := h.toolRenamePlan(context.Background(), tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("args %v: err %v, want containing %q", tc.args, err, tc.want)
		}
	}
}

func TestRenamePlanSchemaAndDescription(t *testing.T) {
	sch := toolSchema("prism_rename_plan")
	req, _ := sch["required"].([]string)
	if len(req) != 2 {
		t.Fatalf("required = %v, want [query newName]", sch["required"])
	}
	desc := toolDescription("prism_rename_plan")
	for _, must := range []string{"review", "ambiguous", "RELAY"} {
		if !strings.Contains(desc, must) {
			t.Errorf("description missing %q", must)
		}
	}
}
