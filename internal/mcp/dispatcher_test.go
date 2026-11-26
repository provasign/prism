package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
)

func dispatch(t *testing.T, s *Dispatcher, method string, params json.RawMessage) (any, *RPCError) {
	t.Helper()
	return s.Dispatch(t.Context(), method, params)
}

func TestDispatch_Initialize(t *testing.T) {
	res, e := dispatch(t, NewDispatcher(newTestHandler(t)), "initialize", nil)
	if e != nil {
		t.Fatal(e)
	}
	m, ok := res.(map[string]any)
	if !ok || m["protocolVersion"] == nil {
		t.Errorf("bad resp: %+v", res)
	}
	instructions, ok := m["instructions"].(string)
	if !ok || !strings.Contains(instructions, "Use Prism for each repository-discovery step") {
		t.Errorf("initialize must carry server routing instructions, got: %q", instructions)
	}
	for _, nativeTool := range []string{"Read", "Grep", "Glob", "find", "rg", "cat", "sed"} {
		if !strings.Contains(instructions, nativeTool) {
			t.Errorf("initialize instructions must route before native %s", nativeTool)
		}
	}
	for _, guidance := range []string{
		"Choose by the information needed now",
		"prism_search locates unknown code or text",
		"prism_query gathers related implementations, callers, and tests",
		"removed_symbols optionally checks exact identifier mentions",
		"make the smallest local edit",
	} {
		if !strings.Contains(instructions, guidance) {
			t.Errorf("initialize instructions missing cost guidance %q", guidance)
		}
	}
	for _, steering := range []string{"use at most", "STOP immediately"} {
		if strings.Contains(instructions, steering) {
			t.Errorf("initialize instructions contain workflow quota %q", steering)
		}
	}
	if len(instructions) > 2000 {
		t.Errorf("server instructions exceed Claude Code's 2 KB limit: %d bytes", len(instructions))
	}
}

func TestDispatch_ToolsList(t *testing.T) {
	res, e := dispatch(t, NewDispatcher(newTestHandler(t)), "tools/list", nil)
	if e != nil {
		t.Fatal(e)
	}
	if m, ok := res.(map[string]any); !ok || m["tools"] == nil {
		t.Errorf("bad resp")
	}
}

func TestDispatch_ToolsCall_BadJSON(t *testing.T) {
	_, e := dispatch(t, NewDispatcher(newTestHandler(t)), "tools/call", json.RawMessage("{not json"))
	if e == nil || e.Code != -32602 {
		t.Errorf("expected -32602, got %#v", e)
	}
}

func TestDispatch_ToolsCall_UnknownTool(t *testing.T) {
	_, e := dispatch(t, NewDispatcher(newTestHandler(t)), "tools/call", json.RawMessage(`{"name":"nope"}`))
	if e == nil {
		t.Error("expected err")
	}
}

func TestDispatch_ToolsCall_HiddenToolRejected(t *testing.T) {
	_, e := dispatch(t, NewDispatcher(newTestHandler(t)), "tools/call", json.RawMessage(`{"name":"prism_savings"}`))
	if e == nil || e.Code != -32601 {
		t.Fatalf("hidden tool was callable: %#v", e)
	}
}

func TestDispatch_Unknown(t *testing.T) {
	_, e := dispatch(t, NewDispatcher(newTestHandler(t)), "nope", nil)
	if e == nil || e.Code != -32601 {
		t.Error("expected -32601")
	}
}

// A compact call naming an unknown op is a malformed call: invalid params,
// not a tool result.
func TestDispatch_CompactBadOpIsInvalidParams(t *testing.T) {
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"nope","args":{}}}`)
	_, e := dispatch(t, NewCompactDispatcher(newTestHandler(t)), "tools/call", params)
	if e == nil || e.Code != -32602 {
		t.Fatalf("expected -32602, got %#v", e)
	}
}

func TestCompactServerRejectsLegacyToolName(t *testing.T) {
	srv := NewCompactDispatcher(newTestHandler(t))
	_, rpcErr := dispatch(t, srv, "tools/call", json.RawMessage(`{"name":"prism_search","arguments":{"query":"x"}}`))
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("hidden legacy tool was callable: %#v", rpcErr)
	}
}

func TestCompactServerAdvertisesAndDispatchesGateway(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "hello.go"), []byte("package hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewCompactDispatcher(h)

	listed, rpcErr := dispatch(t, srv, "tools/list", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	tools := listed.(map[string]any)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "prism" {
		t.Fatalf("compact tools/list = %#v", tools)
	}

	initialized, rpcErr := dispatch(t, srv, "initialize", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	instructions := initialized.(map[string]any)["instructions"].(string)
	if !strings.HasPrefix(instructions, compactServerInstructions+" Scope: ") || strings.Contains(instructions, "prism_search") {
		t.Fatalf("compact initialize instructions are not compact: %q", instructions)
	}
	if !strings.Contains(instructions, "optional for a local body-only edit") || strings.Contains(instructions, "Before editing a symbol") {
		t.Fatalf("compact initialize instructions still mandate impact: %q", instructions)
	}

	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"hello.go","from":1,"to":1}}}`)
	result, rpcErr := dispatch(t, srv, "tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)
	if len(content) == 0 || !strings.Contains(content[0]["text"], "package hello") {
		t.Fatalf("compact read result = %#v", content)
	}
}

// A tool failure reaches the model as an isError result, not an RPC error.
func TestDispatch_ToolFailureIsErrorContent(t *testing.T) {
	params := json.RawMessage(`{"name":"prism_read","arguments":{"file":"../outside.go"}}`)
	result, rpcErr := dispatch(t, NewDispatcher(newTestHandler(t)), "tools/call", params)
	if rpcErr != nil {
		t.Fatalf("a rejected path is an RPC error: %#v", rpcErr)
	}
	m := result.(map[string]any)
	if m["isError"] != true || len(m["content"].([]map[string]string)) == 0 {
		t.Fatalf("rejected path result = %#v", m)
	}
}

func TestCompactInstructionsBatchTermsAndKeepVerifyOptional(t *testing.T) {
	for _, want := range []string{"Batch known identifiers or exact substrings in one search", "Do not re-read unchanged source"} {
		if !strings.Contains(compactServerInstructions, want) {
			t.Errorf("compact initialize instructions omit %q", want)
		}
	}
	for _, want := range []string{`terms:["alpha","beta"]`, "never combine distinct terms in one space-delimited string"} {
		if !strings.Contains(compactServerInstructions, want) {
			t.Errorf("compact initialize instructions omit term-boundary guidance %q", want)
		}
	}
	for _, want := range []string{"Optional op=verify", "Python", "unchecked JavaScript", "PHP", "TypeScript", "checked JavaScript", "Go, Java, Rust, C/C++, or C#"} {
		if !strings.Contains(compactServerInstructions, want) {
			t.Errorf("compact initialize instructions omit %q: %q", want, compactServerInstructions)
		}
	}
	if strings.Contains(compactServerInstructions, "op=verify before finish") {
		t.Errorf("compact initialize instructions still mandate verify: %q", compactServerInstructions)
	}
}

// The full instructions route like the tool descriptions do (core tests the
// descriptions): no conflicting or unavailable routes, and verify optional
// per language.
func TestServerInstructionsRouteEfficiently(t *testing.T) {
	for _, conflict := range []string{
		"CALL THIS FIRST",
		"prefer one batched prism_query",
		"only for one known body",
		"prism_references",
	} {
		if strings.Contains(serverInstructions, conflict) {
			t.Errorf("server instructions contain conflicting or unavailable route %q", conflict)
		}
	}
	for _, want := range []string{"Python", "unchecked JavaScript", "PHP", "TypeScript", "checked JavaScript", "Go, Java, Rust, C/C++, and C#", "complete"} {
		if !strings.Contains(serverInstructions, want) {
			t.Errorf("verify guidance omits %q: %q", want, serverInstructions)
		}
	}
}

// Every instruction surface states the same impact rule: required for the
// triggers in ImpactRequiredFor, optional for ImpactOptionalFor, and no
// blanket obligation (2026-10-17: steering demanded impact for any symbol
// whose callers were not enumerated while these strings made it optional).
func TestImpactPolicyIsStatedTheSameWayInBothInstructionStrings(t *testing.T) {
	for name, text := range map[string]string{"full": serverInstructions, "compact": compactServerInstructions} {
		if !strings.Contains(text, core.ImpactRequiredFor) {
			t.Errorf("%s instructions omit the impact triggers %q:\n%s", name, core.ImpactRequiredFor, text)
		}
		if !strings.Contains(text, "optional for "+core.ImpactOptionalFor) {
			t.Errorf("%s instructions omit %q:\n%s", name, "optional for "+core.ImpactOptionalFor, text)
		}
		for _, blanket := range []string{"whose callers you have not enumerated", "Before editing a symbol", "before editing every symbol"} {
			if strings.Contains(text, blanket) {
				t.Errorf("%s instructions carry a blanket impact obligation %q", name, blanket)
			}
		}
	}
	if len(serverInstructions) > 2000 {
		t.Errorf("server instructions exceed the 2 KB limit: %d", len(serverInstructions))
	}
}

// test-plan is a CLI command for people, not an agent tool (core checks the
// schemas and the compact op): no server serves it or steers to it.
func TestTestPlanIsNotExposedByTheServer(t *testing.T) {
	srv := NewDispatcher(newTestHandler(t))
	if out, err := dispatch(t, srv, "tools/call", json.RawMessage(`{"name":"prism_test_plan","arguments":{}}`)); err == nil {
		t.Fatalf("MCP served prism_test_plan: %v", out)
	}
	for name, text := range map[string]string{"legacy": serverInstructions, "compact": compactServerInstructions} {
		if strings.Contains(text, "test_plan") || strings.Contains(text, "test-plan") {
			t.Errorf("%s server instructions steer agents to test-plan", name)
		}
	}
}

func TestInitializeIncludesLegacyProjectWarning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "prism.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warning := core.LegacyProjectWarning(root)
	if warning == "" {
		t.Fatal("fixture is not a legacy project")
	}
	h := core.NewHandler(config.Default(), root, nil)
	res, rpcErr := dispatch(t, NewCompactDispatcher(h), "initialize", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	instructions := res.(map[string]any)["instructions"].(string)
	if !strings.Contains(instructions, warning) || !strings.Contains(instructions, "prism init") {
		t.Fatalf("initialize instructions missing migration warning: %q", instructions)
	}
}

func TestInitializeStatesProjectScope(t *testing.T) {
	h := newTestHandler(t)
	for _, compact := range []bool{false, true} {
		srv := NewDispatcher(h)
		if compact {
			srv = NewCompactDispatcher(h)
		}
		res, rpcErr := dispatch(t, srv, "initialize", json.RawMessage(`{}`))
		if rpcErr != nil {
			t.Fatal(rpcErr.Message)
		}
		instructions := res.(map[string]any)["instructions"].(string)
		if !strings.Contains(instructions, "files under "+h.Root+" only") ||
			!strings.Contains(instructions, "run the prism CLI from that repository") {
			t.Fatalf("compact=%v: initialize instructions do not state the project scope: %q", compact, instructions)
		}
		if len(instructions) > 2000 {
			t.Fatalf("compact=%v: instructions exceed 2000 bytes: %d", compact, len(instructions))
		}
	}
}
