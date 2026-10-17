package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the public gateway: handler-only tests miss force being discarded
// during compact argument expansion, and validation rejecting ranges+force.
func TestCompactReadForceRecoversDeliveredSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{"file", `{"file":"sample.txt"}`},
		{"range", `{"file":"sample.txt","from":2,"to":2}`},
		{"batch", `{"ranges":[{"file":"sample.txt","from":2,"to":2},{"file":"other.txt","from":1,"to":1}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			for file, body := range map[string]string{
				"sample.txt": "first\nRECOVER_THIS_SOURCE\nlast\n",
				"other.txt":  "RECOVER_OTHER_SOURCE\n",
			} {
				if err := os.WriteFile(filepath.Join(h.Root, file), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			srv := NewCompactServer(h)
			call := func(force *bool) string {
				t.Helper()
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
					t.Fatal(err)
				}
				if force != nil {
					args["force"] = *force
				}
				params, err := json.Marshal(map[string]any{
					"name": "prism", "arguments": map[string]any{"op": "read", "args": args},
				})
				if err != nil {
					t.Fatal(err)
				}
				result, rpcErr := srv.dispatch("tools/call", params)
				if rpcErr != nil {
					t.Fatal(rpcErr.Message)
				}
				return result.(map[string]any)["content"].([]map[string]string)[0]["text"]
			}
			assertSource := func(text string) {
				t.Helper()
				if !strings.Contains(text, "RECOVER_THIS_SOURCE") || strings.Contains(text, "[prism:cached]") {
					t.Fatalf("read did not return requested source:\n%s", text)
				}
				if tc.name == "batch" && !strings.Contains(text, "RECOVER_OTHER_SOURCE") {
					t.Fatalf("forced batch lost its second range:\n%s", text)
				}
			}
			assertSource(call(nil))
			cached := call(nil)
			if !strings.Contains(cached, "[prism:cached]") || strings.Contains(cached, "RECOVER_THIS_SOURCE") {
				t.Fatalf("repeat did not establish a cached delivery:\n%s", cached)
			}
			if !strings.Contains(cached, "force:true") || strings.Contains(cached, "already in context") {
				t.Fatalf("cached pointer must offer recovery without assuming visible context:\n%s", cached)
			}
			force := true
			assertSource(call(&force))
			assertSource(call(&force)) // A second explicit recovery must work too.
			force = false
			if text := call(&force); !strings.Contains(text, "[prism:cached]") {
				t.Fatalf("force:false unexpectedly bypassed delivery cache:\n%s", text)
			}
		})
	}
}

func TestCompactReadForceStillRejectsMixedRangeForms(t *testing.T) {
	for _, field := range []string{"file", "from", "to"} {
		t.Run(field, func(t *testing.T) {
			args := map[string]any{
				"ranges": []any{map[string]any{"file": "sample.txt", "from": 1, "to": 2}},
				"force":  true,
			}
			if field == "file" {
				args[field] = "sample.txt"
			} else {
				args[field] = 1
			}
			_, _, err := expandCompactCall(map[string]any{"op": "read", "args": args})
			if err == nil || !strings.Contains(err.Error(), "ranges cannot be mixed") {
				t.Fatalf("mixed range forms must remain invalid: %v", err)
			}
		})
	}
}
