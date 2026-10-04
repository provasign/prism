package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingReadIsMCPContentNotRPCError(t *testing.T) {
	srv := NewCompactServer(newTestHandler(t))
	call := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"absent.go","from":1,"to":5}}}`)
	result, rpcErr := srv.dispatch("tools/call", call)
	if rpcErr != nil {
		t.Fatalf("missing source became MCP error: %+v", rpcErr)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "absent.go") || !strings.Contains(content, "file not found") {
		t.Fatalf("missing-file guidance lost: %q", content)
	}
}

func TestInvalidRegexReportsPatternError(t *testing.T) {
	h := newTestHandler(t)
	out, err := h.toolSearch(t.Context(), map[string]any{
		"query": "\\.Send(", "scope": "text", "regex": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["invalidPattern"] != "\\.Send(" || !strings.Contains(result["warning"].(string), "invalid regular expression") {
		t.Fatalf("invalid regex must be reported, not searched as a literal: %#v", result)
	}
	if !searchResultPartial(result) {
		t.Fatalf("invalid regex must not claim a complete no-match result: %#v", result)
	}
}

func TestRegexBatchKeepsValidTerm(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "calls.go"), []byte("a.Send()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.toolSearch(t.Context(), map[string]any{
		"query": []string{"\\.Send(", "\\.Send\\("}, "scope": "text", "regex": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	results := out.(map[string]any)["results"].([]map[string]any)
	if len(results) != 2 || results[0]["invalidPattern"] == nil || len(anySlice(results[1]["textHits"])) == 0 {
		t.Fatalf("batch must report bad pattern and retain valid hits: %#v", results)
	}
}

func TestDisabledNativeImpactDoesNotClaimCompilerCoverage(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "false")
	h := evidenceHandler(t, map[string]string{
		"calls.go": "package p\ntype A struct{}\nfunc (A) Send() {}\nfunc Work(a A) { a.Send() }\n",
	})
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "A.Send"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["safeToClaimComplete"] == true || strings.Contains(stringArg(result, "completenessScope", ""), "compiler-backed") {
		t.Fatalf("disabled native analysis must not claim compiler coverage: %#v", result)
	}
	if result["degradedAnalysis"] == nil {
		t.Fatalf("disabled native analysis needs a visible warning: %#v", result)
	}
}

func TestPythonImpactHasNoNativeWarning(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"calls.py": "def send():\n    pass\n\ndef work():\n    send()\n",
	})
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "send"})
	if err != nil {
		t.Fatal(err)
	}
	if note := out.(map[string]any)["degradedAnalysis"]; note != nil {
		t.Fatalf("Python has no compiler-backed pass to confirm; got warning %q", note)
	}
}

func TestCompactReadAcceptsForce(t *testing.T) {
	if err := validateCompactArguments("read", map[string]any{"file": "x.go", "force": true}); err != nil {
		t.Fatal(err)
	}
}

func TestWholeFileForceReturnsSourceAfterPointer(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"x.go": "package p\nfunc X() {}\n"})
	for i := 0; i < 2; i++ {
		if _, err := h.Invoke("prism_read", map[string]any{"file": "x.go"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.Invoke("prism_read", map[string]any{"file": "x.go", "force": true})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if !strings.Contains(result["content"].(string), "func X()") {
		t.Fatalf("force read returned a pointer instead of source: %#v", result)
	}
}

func TestNativePassRequiresAffirmativeCompletion(t *testing.T) {
	for _, diagnostics := range [][]string{
		nil,
		{"native analyzers disabled"},
		{"go: skipped: no changed files in its languages (previous edges carried forward)"},
	} {
		if nativePassCompleted("go", diagnostics) {
			t.Fatalf("not confirmed: %v", diagnostics)
		}
	}
	if !nativePassCompleted("go", []string{"go: resolved 0 native call edge(s)"}) {
		t.Fatal("completed Go pass was not recognized")
	}
}
