package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scopedLookup(name, file string) map[string]any {
	return map[string]any{"name": name, "file": file}
}

func lookupEntries(t *testing.T, h *Handler, args map[string]any) ([]map[string]any, map[string]any) {
	t.Helper()
	value, err := h.Invoke("prism_lookup", args)
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	return out["results"].([]map[string]any), out
}

func TestLookupScopedBatchMatchesScalarBodiesAndProjection(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"a/store.go": "package a\ntype Store struct{}\nfunc (s *Store) Get() int { return 11 }\n",
		"b/store.go": "package b\ntype Store struct{}\nfunc (s *Store) Get() int { return 22 }\n",
	})
	for _, fields := range []any{nil, []any{"signature", "body"}} {
		args := map[string]any{"name": []any{scopedLookup("Store.Get", "a/store.go"), scopedLookup("Store.Get", "b/store.go")}}
		if fields != nil {
			args["fields"] = fields
		}
		entries, out := lookupEntries(t, h, args)
		if len(entries) != 2 {
			t.Fatal(entries)
		}
		for i, file := range []string{"a/store.go", "b/store.go"} {
			scalarArgs := map[string]any{"name": "Store.Get", "file": file}
			if fields != nil {
				scalarArgs["fields"] = fields
			}
			scalar, err := h.Invoke("prism_lookup", scalarArgs)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(entries[i]["result"])
			want, _ := json.Marshal(scalar)
			if string(got) != string(want) || entries[i]["file"] != file {
				t.Fatalf("%s: batch=%s scalar=%s entry=%v", file, got, want, entries[i])
			}
		}
		text, ok := renderLookupAsText(out)
		if !ok || strings.Count(text, "return 11") != 1 || strings.Count(text, "return 22") != 1 {
			t.Fatalf("lost/duplicated bodies: %t %s", ok, text)
		}
	}
}

func TestLookupScopedBatchDoesNotWidenScopeOrGuessAnotherReceiver(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"a.go": "package p\ntype Store struct{}\nfunc (s *Store) Get() int { return 11 }\n",
		"b.go": "package p\ntype Other struct{}\nfunc (s *Other) Get() int { return 22 }\n",
	})
	entries, out := lookupEntries(t, h, map[string]any{"name": []any{
		scopedLookup("Store.Get", "b.go"), scopedLookup("Absent", "a.go"),
		scopedLookup("Store.Get", "missing.go"), scopedLookup("Store.Get", "."),
		scopedLookup("Store.Get", "../a.go"), scopedLookup("Store.Get", filepath.Join(h.Root, "a.go")),
		scopedLookup("Store.Get", "./sub/../a.go"),
	}})
	for _, entry := range entries[:2] {
		result := entry["result"].(map[string]any)
		if result["matched"] != false || result["symbol"] != nil || result["content"] != nil {
			t.Fatalf("wrong scoped symbol delivered: %v", entry)
		}
	}
	for _, entry := range entries[2:6] {
		if entry["error"] == nil || entry["result"] != nil {
			t.Fatalf("invalid scope was accepted: %v", entry)
		}
	}
	if entries[6]["error"] != nil || entries[6]["result"] == nil {
		t.Fatalf("canonical relative path failed: %v", entries[6])
	}
	text, ok := renderLookupAsText(out)
	if !ok || strings.Contains(text, "return 22") || strings.Count(text, "return 11") != 1 || !strings.Contains(text, "scope was not widened") {
		t.Fatalf("unsafe scoped rendering: %t %s", ok, text)
	}
}

func TestLookupScopedBatchPreservesAmbiguity(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"store.go": "package p\ntype A struct{}\ntype B struct{}\nfunc (a *A) Get() int { return 1 }\nfunc (b *B) Get() int { return 2 }\n"})
	entries, out := lookupEntries(t, h, map[string]any{"name": []any{scopedLookup("Get", "store.go")}})
	result := entries[0]["result"].(map[string]any)
	if result["ambiguous"] != true || len(anySlice(result["candidates"])) != 2 {
		t.Fatalf("lost ambiguity: %v", result)
	}
	text, ok := renderLookupAsText(out)
	if !ok || !strings.Contains(text, "AMBIGUOUS") || !strings.Contains(text, "A.Get") || !strings.Contains(text, "B.Get") {
		t.Fatalf("lost rendered ambiguity: %t %s", ok, text)
	}
}

func TestLookupScopedBatchBypassesGlobalNameCap(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("p%02d/store.go", i)] = fmt.Sprintf("package p\ntype Store struct{}\nfunc (s *Store) Get() int { return %d }\n", i)
	}
	h := evidenceHandler(t, files)
	entries, _ := lookupEntries(t, h, map[string]any{"name": []any{scopedLookup("Store.Get", "p39/store.go")}})
	encoded, _ := json.Marshal(entries)
	if !strings.Contains(string(encoded), "return 39") || strings.Contains(string(encoded), "ambiguous") {
		t.Fatalf("exact file was lost behind name cap: %s", encoded)
	}
}

func TestLookupScopedBatchRejectsMalformedItemsBeforeQuerying(t *testing.T) {
	h := &Handler{}
	for _, item := range []any{
		map[string]any{"name": "Get"}, map[string]any{"file": "a.go"},
		scopedLookup("", "a.go"), scopedLookup("Get", " "),
		map[string]any{"name": 42, "file": "a.go"}, map[string]any{"name": "Get", "file": false},
		map[string]any{"name": "Get", "file": "a.go", "fields": []any{"body"}},
	} {
		_, err := h.toolLookup(context.Background(), map[string]any{"name": []any{"Valid", item}})
		if err == nil || !strings.Contains(err.Error(), "no lookups were run") {
			t.Fatalf("invalid item was not rejected before Grove: %v, %v", item, err)
		}
	}
}

func TestLookupScopedBatchKeepsLegacyHintsAndInputUnchanged(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"store.go": "package p\nfunc Get() int { return 7 }\n"})
	args := map[string]any{"name": []any{"Get", scopedLookup("Get", "store.go")}, "file": "bad-legacy-hint"}
	before, _ := json.Marshal(args)
	entries, _ := lookupEntries(t, h, args)
	if len(entries) != 2 || entries[0]["error"] != nil || entries[1]["error"] != nil {
		t.Fatal(entries)
	}
	after, _ := json.Marshal(args)
	if string(before) != string(after) {
		t.Fatal("mutated caller arguments")
	}
	scalar, err := h.Invoke("prism_lookup", map[string]any{"name": "Get", "file": "bad-legacy-hint"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(scalar)
	got, _ := json.Marshal(entries[0]["result"])
	if string(got) != string(want) {
		t.Fatal("legacy soft-hint behavior changed")
	}
}

func TestLookupScopedBatchRejectsExternalSymlink(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"store.go": "package p\nfunc Get() {}\n"})
	external := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(external, []byte("package p\nfunc Get() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(h.Root, "link.go")); err != nil {
		t.Fatal(err)
	}
	entries, _ := lookupEntries(t, h, map[string]any{"name": []any{scopedLookup("Get", "link.go")}})
	if err, _ := entries[0]["error"].(string); !strings.Contains(err, "outside workspace root") {
		t.Fatal(entries)
	}
}

func TestLookupScopedBatchOmissionsRetainFileIdentity(t *testing.T) {
	body := "package p\nfunc Large() string { return `" + strings.Repeat("x", lookupBatchMaxBytes) + "` }\n"
	h := evidenceHandler(t, map[string]string{"a.go": body, "b.go": body, "small.go": "package p\nfunc Small() int { return 7 }\n"})
	entries, out := lookupEntries(t, h, map[string]any{"name": []any{
		scopedLookup("Large", "a.go"), scopedLookup("Large", "b.go"), "Small",
	}})
	omitted := out["omittedItems"].([]map[string]any)
	if len(entries) != 1 || len(omitted) != 2 || omitted[0]["file"] != "a.go" || omitted[1]["file"] != "b.go" {
		t.Fatal(out)
	}
	text, ok := renderLookupAsText(out)
	if !ok || strings.Count(text, "NOT DELIVERED:") != 2 || !strings.Contains(text, "a.go") || !strings.Contains(text, "b.go") || !strings.Contains(text, "return 7") {
		t.Fatalf("lost scoped omissions: %t %s", ok, text)
	}
}

func TestLookupScopedBatchSchemaAdvertisesExactItems(t *testing.T) {
	schema := toolSchema("prism_lookup")
	props := schema["properties"].(map[string]any)
	name := props["name"].(map[string]any)
	forms := name["oneOf"].([]map[string]any)
	if forms[0]["type"] != "string" || forms[1]["minItems"] != 1 || forms[1]["maxItems"] != 10 {
		t.Fatal(forms)
	}
	items := forms[1]["items"].(map[string]any)["oneOf"].([]map[string]any)
	object := items[1]
	if items[0]["type"] != "string" || object["type"] != "object" || object["additionalProperties"] != false {
		t.Fatal(items)
	}
	required := object["required"].([]string)
	if len(required) != 2 || required[0] != "name" || required[1] != "file" {
		t.Fatal(required)
	}
	for _, key := range required {
		property := object["properties"].(map[string]any)[key].(map[string]any)
		if property["type"] != "string" || property["minLength"] != 1 {
			t.Fatal(property)
		}
	}
	if !strings.Contains(toolDescription("prism_lookup"), "exact per-item file scope") ||
		!strings.Contains(props["file"].(map[string]any)["description"].(string), "soft") {
		t.Fatal("lookup schema must distinguish exact items from legacy hints")
	}
	var raw any
	if err := json.Unmarshal([]byte(`[{"name":"Get","file":"a.go"},"Other"]`), &raw); err != nil {
		t.Fatal(err)
	}
	requests, batch, err := lookupBatchRequests(raw)
	if err != nil || !batch || len(requests) != 2 || requests[0].file != "a.go" || requests[1].file != "" {
		t.Fatalf("JSON shape does not reach scoped parser: %v %v", requests, err)
	}
	tooMany := make([]any, 11)
	for i := range tooMany {
		tooMany[i] = scopedLookup("Get", "a.go")
	}
	if _, _, err := lookupBatchRequests(tooMany); err == nil {
		t.Fatal("scoped batches must retain the ten-item limit")
	}
}

func TestLookupScopedBatchRenderingFailsClosedOnUnknownMetadata(t *testing.T) {
	for _, out := range []map[string]any{
		{"results": []map[string]any{{"name": "Get", "file": "a.go", "result": map[string]any{}, "unknown": true}}},
		{"results": []map[string]any{{"name": "Get", "error": "failed", "result": map[string]any{}}}},
		{"results": []map[string]any{{"name": "Get", "file": 42, "error": "failed"}}},
		{"results": []map[string]any{}, "omittedItems": "Get in a.go"},
		{"results": []map[string]any{}, "omittedItems": []map[string]any{{"name": "Get", "unknown": "a.go"}}},
		{"results": []map[string]any{}, "omittedItems": []map[string]any{{"name": "Get", "file": "a.go", "unknown": true}}},
	} {
		if text, ok := renderLookupAsText(out); ok {
			t.Fatalf("unknown evidence was discarded: %v -> %s", out, text)
		}
	}
	out := map[string]any{"results": []map[string]any{{"name": "Get", "file": "a.go", "error": "index unavailable"}}}
	text, ok := renderLookupAsText(out)
	if !ok || !strings.Contains(text, `file="a.go"`) || !strings.Contains(text, "ERROR: index unavailable") {
		t.Fatalf("lost scoped error: %t %s", ok, text)
	}
}
