package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestCompactToolSchemasExposeOneSmallerGateway(t *testing.T) {
	compact := CompactToolSchemas()
	if len(compact) != 1 {
		t.Fatalf("compact schemas = %d, want 1", len(compact))
	}
	if got := compact[0]["name"]; got != "prism" {
		t.Fatalf("compact tool name = %v, want prism", got)
	}

	schema := compact[0]["inputSchema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	ops := properties["op"].(map[string]any)["enum"].([]string)
	wantOps := []string{"lookup", "read", "search", "query", "change_impact", "verify"}
	if !reflect.DeepEqual(ops, wantOps) {
		t.Fatalf("compact operations = %v, want %v", ops, wantOps)
	}
	if description := properties["op"].(map[string]any)["description"].(string); !strings.Contains(description, "lookup for any known symbol") {
		t.Fatalf("compact operation guidance = %q", description)
	}
	if _, ok := schema["oneOf"]; ok {
		t.Fatal("compact schema must not use top-level oneOf; older MCP hosts drop the tool")
	}
	argsSchema := properties["args"].(map[string]any)
	if got := argsSchema["additionalProperties"]; got != false {
		t.Fatalf("compact args additionalProperties = %v, want false", got)
	}
	argProperties := argsSchema["properties"].(map[string]any)
	searchQuery := argProperties["query"].(map[string]any)
	if got := searchQuery["type"].([]string); len(got) != 2 || got[1] != "array" {
		t.Fatalf("compact search query types = %v, want string or array", got)
	}
	if got := argProperties["limit"].(map[string]any)["maximum"]; got != 240 {
		t.Fatalf("compact read limit maximum = %v, want 240", got)
	}

	compactJSON, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(ToolSchemas())
	if err != nil {
		t.Fatal(err)
	}
	if len(compactJSON)*2 >= len(legacyJSON) {
		t.Fatalf("compact schema is not at least 2x smaller: compact=%d legacy=%d", len(compactJSON), len(legacyJSON))
	}
}

func TestCompactServerRejectsLegacyToolName(t *testing.T) {
	srv := NewCompactServer(newTestHandler(t))
	_, rpcErr := srv.dispatch("tools/call", json.RawMessage(`{"name":"prism_search","arguments":{"query":"x"}}`))
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("hidden legacy tool was callable: %#v", rpcErr)
	}
}

func TestExpandCompactCall(t *testing.T) {
	validArgs := map[string]map[string]any{
		"lookup":        {"name": "Thing"},
		"read":          {"file": "thing.go"},
		"search":        {"query": "Thing"},
		"query":         {"task": "inspect Thing", "terms": []any{"Thing"}},
		"change_impact": {"query": "Thing.Run"},
		"verify":        {},
	}
	for op, want := range compactOperations {
		got, args, err := expandCompactCall(map[string]any{
			"op":   op,
			"args": validArgs[op],
		})
		if err != nil {
			t.Fatalf("op %q: %v", op, err)
		}
		if got != want || !reflect.DeepEqual(args, validArgs[op]) {
			t.Errorf("op %q expanded to %q %#v, want %q with preserved args", op, got, args, want)
		}
	}

	for name, envelope := range map[string]map[string]any{
		"missing op":    {},
		"unknown op":    {"op": "remove"},
		"non-object":    {"op": "read", "args": "file=x.go"},
		"unknown field": {"op": "read", "file": "x.go"},
		"missing arg":   {"op": "read", "args": map[string]any{}},
		"wrong op type": {"op": "change_impact", "args": map[string]any{"query": []any{"Thing.Run"}}},
	} {
		if _, _, err := expandCompactCall(envelope); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCompactServerAdvertisesAndDispatchesGateway(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "hello.go"), []byte("package hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewCompactServer(h)

	listed, rpcErr := srv.dispatch("tools/list", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	tools := listed.(map[string]any)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "prism" {
		t.Fatalf("compact tools/list = %#v", tools)
	}

	initialized, rpcErr := srv.dispatch("initialize", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	instructions := initialized.(map[string]any)["instructions"].(string)
	if instructions != compactServerInstructions || strings.Contains(instructions, "prism_search") {
		t.Fatalf("compact initialize instructions are not compact: %q", instructions)
	}

	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"hello.go","offset":1,"limit":1}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)
	if len(content) == 0 || !strings.Contains(content[0]["text"], "package hello") {
		t.Fatalf("compact read result = %#v", content)
	}
}

func TestCompactServerRewritesSearchFollowUpGuidance(t *testing.T) {
	text := searchLocatorGuidance + "\n"
	got := rewriteCompactGuidance(text)
	if strings.Contains(got, "prism_lookup") || !strings.Contains(got, "op=lookup") {
		t.Fatalf("compact search guidance = %q", got)
	}
}

func TestCompactServerRejectsArgumentsForWrongOperation(t *testing.T) {
	srv := NewCompactServer(newTestHandler(t))
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"hello.go","regex":true}}}`)
	_, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "unknown parameter") {
		t.Fatalf("wrong-operation argument error = %#v", rpcErr)
	}
}

func TestCompactSearchBodiesIncludesTightEnclosingMethod(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n\nfunc target() {\n\tprintln(\"unique compact needle\")\n}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	srv := NewCompactServer(NewHandler(config.Default(), root, gc))

	params := json.RawMessage(`{"name":"prism","arguments":{"op":"search","args":{"query":"unique compact needle"}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "Exact enclosing source") || !strings.Contains(content, "func target()") {
		t.Fatalf("compact search did not include the enclosing body:\n%s", content)
	}
}

func TestCompactSearchBodyEligibilityRejectsBroadOrExplicitlyShapedSearches(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"explicit context": {"query": "needle", "context": 0},
		"file inventory":   {"query": "needle", "files_only": true},
		"rollup":           {"query": "needle", "rollup_only": true},
		"exhaustive":       {"query": "needle", "exhaustive": true},
		"symbol scope":     {"query": "needle", "scope": "symbols"},
		"batched":          {"query": []any{"one", "two"}},
	} {
		if compactSearchCanIncludeBodies(args) {
			t.Errorf("%s search unexpectedly eligible: %#v", name, args)
		}
	}
	if !compactSearchCanIncludeBodies(map[string]any{"query": "needle"}) {
		t.Error("default single-term search should be eligible")
	}
}
