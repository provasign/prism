package assist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider replays a scripted sequence of assistant replies.
type fakeProvider struct {
	replies []Msg
	i       int
	// captured: the tool-result contents the model was shown
	seen []string
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(msgs []Msg, tools []ToolDef, force bool) (Msg, error) {
	for _, m := range msgs {
		if m.Role == "tool" {
			found := false
			for _, s := range f.seen {
				if s == m.Content {
					found = true
				}
			}
			if !found {
				f.seen = append(f.seen, m.Content)
			}
		}
	}
	if f.i >= len(f.replies) {
		return Msg{Role: "assistant", Content: "done"}, nil
	}
	r := f.replies[f.i]
	f.i++
	return r, nil
}

// The core structural guarantee: the model receives ONLY compact metadata
// (counts/flags), never the operation payload — so relay loss is impossible.
func TestRun_ModelNeverSeesPayload(t *testing.T) {
	provider := &fakeProvider{replies: []Msg{
		{Role: "assistant", Calls: []ToolCall{{ID: "1", Name: "change_impact",
			Args: map[string]any{"symbol": "DataKeyCache.GetById"}}}},
		{Role: "assistant", Calls: []ToolCall{{ID: "2", Name: "submit",
			Args: map[string]any{"summary": "11 sites, closed."}}}},
	}}
	secret := "pkg/secret/encryption/manager/oss_dek_cache.go"
	invoke := func(tool string, args map[string]any) (any, error) {
		if tool != "prism_change_impact" {
			t.Fatalf("routed to %q, want prism_change_impact", tool)
		}
		return map[string]any{
			"declarations": []any{map[string]any{"filePath": secret, "name": "GetById"}},
			"family":       []any{map[string]any{"filePath": secret, "name": "GetById"}},
			"totalSites":   float64(11),
			"completeness": "closed",
		}, nil
	}
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	summary, err := Run("add ctx to GetById", provider, invoke, Options{Out: devnull, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "11 sites, closed." {
		t.Fatalf("summary = %q", summary)
	}
	for _, s := range provider.seen {
		if strings.Contains(s, secret) {
			t.Fatalf("payload leaked into model context: %s", s)
		}
		if !strings.Contains(s, "totalSites") && !strings.Contains(s, "error") {
			t.Fatalf("model tool-result missing compact metadata: %s", s)
		}
	}
	if len(provider.seen) == 0 {
		t.Fatal("model never saw any tool result")
	}
}

// applyRenamePlan: applies confirmed edits, verifies before-lines, skips
// drifted lines, never touches ambiguous.
func TestApplyRenamePlan(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\nfunc GetById(id string) string {\n\treturn id\n}\n"
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{
		"edits": []any{
			map[string]any{"filePath": "x.go", "line": float64(3),
				"before": "func GetById(id string) string {",
				"after":  "func GetDataKeyById(id string) string {"},
			// drifted line: before doesn't match -> must SKIP, not corrupt
			map[string]any{"filePath": "x.go", "line": float64(4),
				"before": "\tSOMETHING ELSE", "after": "\tcorrupted"},
		},
		"ambiguous": []any{map[string]any{"filePath": "x.go", "line": float64(5)}},
	}
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err := applyRenamePlan(devnull, dir, plan, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "GetDataKeyById(id string)") {
		t.Fatal("confirmed edit not applied")
	}
	if strings.Contains(string(got), "corrupted") {
		t.Fatal("drifted line was overwritten — must skip")
	}
	if !strings.Contains(string(got), "\treturn id") {
		t.Fatal("unrelated line damaged")
	}
}

// Ollama content-JSON fallback: a correct decision serialized nonstandardly
// must be recovered, and unknown tool names must NOT be.
func TestParseContentToolCall(t *testing.T) {
	tools := toolDefs()
	c := parseContentToolCall(`{"name": "change_impact", "arguments": {"symbol": "A.b"}}`, tools)
	if c == nil || c.Name != "change_impact" || c.Args["symbol"] != "A.b" {
		t.Fatalf("fallback parse failed: %+v", c)
	}
	if parseContentToolCall(`{"name": "rm_rf", "arguments": {}}`, tools) != nil {
		t.Fatal("unknown tool must not parse")
	}
	if parseContentToolCall("plain prose, no JSON", tools) != nil {
		t.Fatal("prose must not parse")
	}
	fenced := "```json\n{\"name\": \"dead_code\", \"arguments\": {}}\n```"
	if c := parseContentToolCall(fenced, tools); c == nil || c.Name != "dead_code" {
		t.Fatal("fenced JSON must parse")
	}
}
