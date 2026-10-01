package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// Calls agents actually sent (Aug-Sep 2026 transcripts) that prism knew the
// meaning of and still rejected; each cost a retry turn or the op.
func TestCompactAcceptsCallsWhoseMeaningIsClear(t *testing.T) {
	for _, c := range []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"args beside op", map[string]any{"op": "change_impact", "name": "walk"}, map[string]any{"query": "walk"}},
		{"line numbers as strings", map[string]any{"op": "read", "args": map[string]any{"file": "a.go", "from": "278", "to": "300"}},
			map[string]any{"file": "a.go", "offset": 278}},
		{"path for paths", map[string]any{"op": "search", "args": map[string]any{"terms": []any{"x"}, "path": []any{"src"}}},
			map[string]any{"path": []any{"src"}}},
		{"file for symbol_file", map[string]any{"op": "lookup", "args": map[string]any{"name": "A.b", "file": "a.go"}},
			map[string]any{"file": "a.go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, legacy, err := expandCompactCall(c.in)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			for k, v := range c.want {
				got := legacy[k]
				if gs, ok := got.([]any); ok {
					if len(gs) == 0 || gs[0] != v.([]any)[0] {
						t.Fatalf("%s = %v, want %v (all: %v)", k, got, v, legacy)
					}
					continue
				}
				if got != v {
					t.Fatalf("%s = %v, want %v (all: %v)", k, got, v, legacy)
				}
			}
		})
	}
	if _, _, err := expandCompactCall(map[string]any{"op": "search", "bogus": 1}); err == nil {
		t.Fatal("an unknown top-level field must still be rejected")
	}
}

// chi walk / node.walk: two different functions. The rejection must carry
// calls the agent can send back unchanged.
func TestAmbiguousImpactListsReadyCalls(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"go.mod":  "module ex\n\ngo 1.21\n",
		"tree.go": "package ex\n\ntype node struct{}\n\nfunc (n *node) walk() {}\n\nfunc walk() {}\n\nfunc use(n *node) { n.walk(); walk() }\n",
	})
	raw := callCompactErr(t, srv, "change_impact", map[string]any{"name": "walk", "symbol_file": "tree.go"})
	if !strings.Contains(raw, `{"op":"change_impact","args":{"name":"node.walk"`) || !strings.Contains(raw, `{"op":"change_impact","args":{"name":"walk"`) {
		t.Fatalf("ambiguity answer lacks ready-to-use calls:\n%s", raw)
	}
}

func TestVerifyIgnoresPrismOwnedFiles(t *testing.T) {
	for _, f := range []string{"CLAUDE.md", ".mcp.json", "prism.yaml", ".claude/settings.json", ".grove/grove.db"} {
		if !prismOwnedFile(f) {
			t.Errorf("%s should be treated as prism's own file", f)
		}
	}
	for _, f := range []string{"tree.go", "docs/CLAUDE.md.txt", "src/claude.go"} {
		if prismOwnedFile(f) {
			t.Errorf("%s is project code", f)
		}
	}
}

// callCompactErr returns the text of a call's answer or its error.
func callCompactErr(t *testing.T, srv *Server, op string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "prism", "arguments": map[string]any{"op": op, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := srv.dispatch("tools/call", raw)
	if rpcErr != nil {
		return rpcErr.Message
	}
	var b strings.Builder
	for _, c := range result.(map[string]any)["content"].([]map[string]string) {
		b.WriteString(c["text"])
	}
	return b.String()
}
