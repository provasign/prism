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
	if len(compact) != 1 || compact[0]["name"] != "prism" {
		t.Fatalf("compact gateway = %#v", compact)
	}
	schema := compact[0]["inputSchema"].(map[string]any)
	if _, ok := schema["oneOf"]; ok {
		t.Fatal("top-level oneOf can make older MCP hosts drop the tool")
	}
	if _, ok := schema["anyOf"]; ok {
		t.Fatal("top-level anyOf can make older MCP hosts drop the tool")
	}
	properties := schema["properties"].(map[string]any)
	ops := properties["op"].(map[string]any)["enum"].([]string)
	if want := []string{"lookup", "read", "search", "query", "change_impact", "verify"}; !reflect.DeepEqual(ops, want) {
		t.Fatalf("compact operations = %v, want %v", ops, want)
	}
	opMap := properties["op"].(map[string]any)["description"].(string)
	for _, want := range []string{"lookup: name[,symbol_file,fields]", "read: file,from,to or ranges",
		"search: terms[", "query: task,terms[", "change_impact: name[", "verify: removed_symbols",
		"Known symbol → lookup; search only when location is unknown"} {
		if !strings.Contains(opMap, want) {
			t.Errorf("op map omits %q: %q", want, opMap)
		}
	}
	argsSchema := properties["args"].(map[string]any)
	if argsSchema["additionalProperties"] != false {
		t.Fatal("compact args must reject unknown fields")
	}
	argProperties := argsSchema["properties"].(map[string]any)
	owners := map[string]string{
		"name": "lookup,change_impact", "symbol_file": "lookup,change_impact",
		"fields": "lookup", "signature": "change_impact",
		"file": "read", "from": "read", "to": "read", "ranges": "read",
		"terms": "search,query", "task": "query", "scope": "search",
		"paths": "search,query", "glob": "search,query", "regex": "search",
		"files_only": "search", "max_results": "search", "exhaustive": "search",
		"removed_symbols": "verify",
	}
	if len(argProperties) != len(owners) {
		t.Fatalf("compact fields = %d, want %d: %#v", len(argProperties), len(owners), argProperties)
	}
	for field, owner := range owners {
		property := argProperties[field].(map[string]any)
		if description := property["description"].(string); !strings.HasPrefix(description, "ops: "+owner+".") {
			t.Errorf("%s description lacks exact ops: owner prefix: %q", field, description)
		}
	}
	for _, removed := range []string{"limit", "offset", "query", "context", "budget", "delivery",
		"max_files", "rollup_only", "include", "model", "profile", "context_used", "base"} {
		if _, exists := argProperties[removed]; exists {
			t.Errorf("removed compact field %q was re-advertised", removed)
		}
	}
	ranges := argProperties["ranges"].(map[string]any)
	if ranges["maxItems"] != 10 || ranges["items"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("compact ranges are not bounded: %#v", ranges)
	}
	if argProperties["max_results"].(map[string]any)["maximum"] != exhaustiveSymbolCap {
		t.Fatal("compact search result bound changed")
	}
	if description := argProperties["max_results"].(map[string]any)["description"].(string); !strings.Contains(description, "Search-only") {
		t.Fatalf("max_results must be unmistakably search-only: %q", description)
	}
	compactJSON, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(ToolSchemas())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compact schema bytes=%d legacy bytes=%d", len(compactJSON), len(legacyJSON))
	if len(compactJSON)*2 >= len(legacyJSON) {
		t.Fatalf("compact schema is not at least 2x smaller: compact=%d legacy=%d", len(compactJSON), len(legacyJSON))
	}
}

func TestCompactGuidanceBatchesKnownPhrasesAndReusesSource(t *testing.T) {
	properties := CompactToolSchemas()[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	args := properties["args"].(map[string]any)["properties"].(map[string]any)
	terms := args["terms"].(map[string]any)["description"].(string)
	if !strings.Contains(terms, "Batch task phrases") {
		t.Fatalf("terms field does not suggest one batched search: %q", terms)
	}
	for _, want := range []string{"Batch known task phrases in one search", "Do not re-read unchanged source"} {
		if !strings.Contains(compactServerInstructions, want) {
			t.Errorf("compact initialize instructions omit %q", want)
		}
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
	cases := []struct {
		op   string
		in   map[string]any
		want map[string]any
	}{
		{"lookup", map[string]any{"name": "Thing", "symbol_file": "a.go"},
			map[string]any{"name": "Thing", "file": "a.go"}},
		{"read", map[string]any{"file": "a.go", "from": 7, "to": 9},
			map[string]any{"file": "a.go", "offset": 7, "limit": 3}},
		{"search", map[string]any{"terms": "Thing", "paths": "src", "max_results": 100},
			map[string]any{"query": "Thing", "path": "src", "limit": 100}},
		{"query", map[string]any{"task": "inspect", "terms": "Thing", "paths": "src", "glob": "*.go"},
			map[string]any{"task": "inspect", "terms": []any{"Thing"}, "paths": "src", "glob": "*.go"}},
		{"change_impact", map[string]any{"name": "Thing.Run", "symbol_file": "a.go"},
			map[string]any{"query": "Thing.Run", "file": "a.go"}},
		{"verify", map[string]any{}, map[string]any{}},
	}
	for _, tc := range cases {
		got, args, err := expandCompactCall(map[string]any{"op": tc.op, "args": tc.in})
		if err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		if got != compactOperations[tc.op] || !reflect.DeepEqual(args, tc.want) {
			t.Errorf("%s expands to %q %#v, want %#v", tc.op, got, args, tc.want)
		}
	}
	_, clamped, err := expandCompactCall(map[string]any{"op": "read", "args": map[string]any{"file": "a.go", "from": 1, "to": 560}})
	if err != nil || clamped["limit"] != compactReadLimit {
		t.Errorf("oversized read was not clamped: %#v, %v", clamped, err)
	}
	_, scopedImpact, err := expandCompactCall(map[string]any{"op": "change_impact",
		"args": map[string]any{"name": []any{map[string]any{"name": "Thing.Run", "file": "a.go"}}}})
	if err != nil || scopedImpact["query"] != "Thing.Run" || scopedImpact["file"] != "a.go" {
		t.Errorf("scoped impact name mapping = %#v, %v", scopedImpact, err)
	}
	for _, tc := range []struct {
		op   string
		args map[string]any
		want string
	}{
		{"change_impact", map[string]any{"query": "Thing.Run"}, "use name. Accepted fields: name, symbol_file, signature"},
		{"search", map[string]any{"terms": "Thing", "limit": 50}, "use max_results. Accepted fields: terms, scope, paths"},
		{"query", map[string]any{"task": "inspect", "terms": "Thing", "max_results": 20}, "Accepted fields: task, terms, paths, glob"},
		{"read", map[string]any{"file": "a.go", "offset": 1}, "use from. Accepted fields: file, from, to, ranges"},
		{"change_impact", map[string]any{"name": []any{"A", "B"}}, "exactly one symbol"},
		{"read", map[string]any{}, "requires args.file"},
		{"query", map[string]any{"task": "inspect"}, "requires args.terms"},
	} {
		_, _, err := expandCompactCall(map[string]any{"op": tc.op, "args": tc.args})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s args %#v error = %v, want %q", tc.op, tc.args, err, tc.want)
		}
	}
	for _, envelope := range []map[string]any{{}, {"op": "remove"}, {"op": "read", "args": "file=a.go"},
		{"op": "read", "file": "a.go"}} {
		if _, _, err := expandCompactCall(envelope); err == nil {
			t.Errorf("invalid envelope accepted: %#v", envelope)
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
	if !strings.Contains(instructions, "optional for a local body-only edit") || strings.Contains(instructions, "Before editing a symbol") {
		t.Fatalf("compact initialize instructions still mandate impact: %q", instructions)
	}

	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"hello.go","from":1,"to":1}}}`)
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
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "Accepted fields: file, from, to, ranges") {
		t.Fatalf("wrong-operation argument error = %#v", rpcErr)
	}
}

func TestCompactReadBatchesMultipleFiles(t *testing.T) {
	h := newTestHandler(t)
	for name, body := range map[string]string{
		"sample.txt": "one\ntwo\nthree\nfour\n",
		"other.txt":  "alpha\nbeta\ngamma\n",
	} {
		if err := os.WriteFile(filepath.Join(h.Root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewCompactServer(h)
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"ranges":[{"file":"sample.txt","from":2,"to":3},{"file":"other.txt","from":2,"to":3}]}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	for _, want := range []string{"sample.txt lines 2-3 of 4", "2\ttwo", "3\tthree",
		"other.txt lines 2-3 of 3", "2\tbeta", "3\tgamma"} {
		if !strings.Contains(content, want) {
			t.Errorf("batched read omitted %q:\n%s", want, content)
		}
	}
	if strings.Index(content, "2\ttwo") > strings.Index(content, "2\tbeta") {
		t.Errorf("batched read changed requested order:\n%s", content)
	}
}

func TestCompactReadClampsInsteadOfFailing(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "large.txt"), []byte(strings.Repeat("line\n", 800)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewCompactServer(h)
	single := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"large.txt","from":1,"to":560}}}`)
	result, rpcErr := srv.dispatch("tools/call", single)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "lines 1-240 of 800") || !strings.Contains(content, "clamped to 1-240") {
		t.Fatalf("single read did not surface clamp:\n%s", content)
	}
	whole := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"large.txt"}}}`)
	result, rpcErr = srv.dispatch("tools/call", whole)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content = result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "clamped to 1-240") {
		t.Fatalf("whole-file compact read did not surface clamp:\n%s", content)
	}
	batch := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"ranges":[{"file":"large.txt","from":1,"to":400},{"file":"large.txt","from":401,"to":800},{"file":"large.txt","from":700,"to":800},{"file":"large.txt","from":1,"to":240}]}}}`)
	result, rpcErr = srv.dispatch("tools/call", batch)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content = result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "range 1") || !strings.Contains(content, "range 4 (large.txt:1-240) clamped to 1-19") {
		t.Fatalf("batch did not surface per-range clamp:\n%s", content)
	}
}

func TestCompactReadRejectsMalformedBatchesWithoutCaching(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "sample.txt"), []byte("one\nshort\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewCompactServer(h)
	for _, args := range []string{
		`{"ranges":[]}`,
		`{"ranges":[{"from":1,"to":1}]}`,
		`{"ranges":[{"file":"sample.txt","from":0,"to":1}]}`,
		`{"ranges":[{"file":"sample.txt","from":2,"to":1}]}`,
		`{"file":"sample.txt","ranges":[{"file":"sample.txt","from":1,"to":1}]}`,
		`{"ranges":[{"file":"../outside.txt","from":1,"to":1}]}`,
	} {
		params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":` + args + `}}`)
		if _, rpcErr := srv.dispatch("tools/call", params); rpcErr == nil {
			t.Errorf("accepted invalid batch: %s", args)
		}
	}
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"file":"sample.txt","from":2,"to":2}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "short") || strings.Contains(content, "[prism:cached]") {
		t.Fatalf("rejected batch changed read cache: %s", content)
	}
}

func TestCompactReadOversizedLineDegradesWithWarning(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "long.txt"), []byte(strings.Repeat("x", 21*1024)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"read","args":{"ranges":[{"file":"long.txt","from":1,"to":1}]}}}`)
	result, rpcErr := NewCompactServer(h).dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "omitted by the compact read budget") {
		t.Fatalf("oversized line did not degrade explicitly:\n%s", content)
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

	params := json.RawMessage(`{"name":"prism","arguments":{"op":"search","args":{"terms":"unique compact needle"}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "Exact source") || !strings.Contains(content, "func target()") {
		t.Fatalf("compact search did not include the enclosing body:\n%s", content)
	}
}

func TestCompactSymbolLocatorInlinesSmallBodies(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n\nfunc TargetOne() { println(1) }\nfunc TargetTwo() { println(2) }\nfunc TargetThree() { println(3) }\nfunc TargetFour() { println(4) }\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	srv := NewCompactServer(NewHandler(config.Default(), root, gc))
	for _, tc := range []struct {
		term, body string
		inline     bool
	}{
		{"TargetOne", "func TargetOne()", true},
		{"Target", "func TargetOne()", false},
	} {
		params := json.RawMessage(`{"name":"prism","arguments":{"op":"search","args":{"terms":"` + tc.term + `","scope":"symbols"}}}`)
		result, rpcErr := srv.dispatch("tools/call", params)
		if rpcErr != nil {
			t.Fatal(rpcErr.Message)
		}
		content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
		if got := strings.Contains(content, "Exact source"); got != tc.inline {
			t.Errorf("%s inline=%v, want %v:\n%s", tc.term, got, tc.inline, content)
		}
		if tc.inline && (strings.Contains(content, "locator result — use") || !strings.Contains(content, tc.body)) {
			t.Errorf("small symbol result failed to replace locator note:\n%s", content)
		}
	}
}

func TestCompactBatchedSearchInlinesUniqueExactFirstSymbol(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n" + strings.Repeat("// filler\n", 330) +
		"func TargetOne() { println(1) }\nfunc TargetOneHelper() {}\n" +
		"func TestTargetOne() {}\nfunc HandleTargetOne() {}\nfunc TargetTwo() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	srv := NewCompactServer(NewHandler(config.Default(), root, gc))
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"search","args":{"terms":["TargetOne","Target"],"scope":"symbols"}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "Exact source for the unique exact-name match") ||
		!strings.Contains(content, "func TargetOne()") || !strings.Contains(content, "other hits remain locators") {
		t.Fatalf("batched exact symbol body was not delivered:\n%s", content)
	}
	if strings.Count(content, "locator result") < 2 {
		t.Fatalf("batched terms lost locator guidance for remaining hits:\n%s", content)
	}
}

func TestCompactSearchDefaultsToTwoContextLines(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "sample.txt"), []byte("before-two\nbefore-one\nUniqueNeedle\nafter-one\nafter-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"search","args":{"terms":"UniqueNeedle","scope":"text"}}}`)
	result, rpcErr := NewCompactServer(h).dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	for _, want := range []string{"before-two", "before-one", "after-one", "after-two"} {
		if !strings.Contains(content, want) {
			t.Errorf("default context omits %q:\n%s", want, content)
		}
	}
}

func TestCompactQueryHonorsPathsAndGlob(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"inside.go":  "package sample\nfunc ScopedRelevant() {}\n",
		"outside.go": "package sample\nfunc OtherRelevant() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(root)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	srv := NewCompactServer(NewHandler(config.Default(), root, gc))
	params := json.RawMessage(`{"name":"prism","arguments":{"op":"query","args":{"task":"inspect relevant functions","terms":"Relevant","paths":"inside.go","glob":"*.go"}}}`)
	result, rpcErr := srv.dispatch("tools/call", params)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	content := result.(map[string]any)["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(content, "ScopedRelevant") || strings.Contains(content, "OtherRelevant") {
		t.Fatalf("scoped compact query ignored paths/glob:\n%s", content)
	}
}

func TestCompactSearchBodyEligibilityRejectsBroadOrExplicitlyShapedSearches(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"explicit context": {"query": "needle", "context": 0},
		"file inventory":   {"query": "needle", "files_only": true},
		"rollup":           {"query": "needle", "rollup_only": true},
		"exhaustive":       {"query": "needle", "exhaustive": true},
	} {
		if compactSearchCanIncludeBodies(args) {
			t.Errorf("%s search unexpectedly eligible: %#v", name, args)
		}
	}
	if !compactSearchCanIncludeBodies(map[string]any{"query": "needle"}) {
		t.Error("default single-term search should be eligible")
	}
	if !compactSearchCanIncludeBodies(map[string]any{"query": "needle", "scope": "symbols"}) {
		t.Error("small symbol-only search should be eligible for body escalation")
	}
	if !compactSearchCanIncludeBodies(map[string]any{"query": []any{"needle", "broad"}}) {
		t.Error("batched search should be eligible for a unique exact first symbol")
	}
}
