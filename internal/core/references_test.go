package core

import (
	"strings"
	"testing"
)

// BUG-14: Type.member resolves one symbol; a bare name lists each
// occurrence once as path:line:col: text.
func TestReferencesQualifiedAndBare(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{
		"go.mod": "module example.com/r\n\ngo 1.22\n",
		"a.go": "package r\n\ntype Client struct{}\n\nfunc (c *Client) Status() int { return 1 }\n\ntype Drift struct{ Status int }\n\n" +
			"func Use(c *Client, d Drift) int {\n\treturn c.Status() + d.Status + c.Status()\n}\n",
	})
	out, err := h.Invoke("prism_references", map[string]any{"name": "Client.Status"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	sites := strings.Join(refStrings(m["sites"]), "\n")
	if m["resolved"] != true || !strings.Contains(sites, "a.go:10:11:") || !strings.Contains(sites, "a.go:10:35:") || strings.Contains(sites, "a.go:10:24:") {
		t.Fatalf("qualified references:\n%s\n%#v", sites, m)
	}
	out, err = h.Invoke("prism_references", map[string]any{"name": "Status"})
	if err != nil {
		t.Fatal(err)
	}
	rows := refStrings(out.(map[string]any)["sites"])
	seen := map[string]bool{}
	for _, r := range rows {
		key := strings.SplitN(r, ": ", 2)[0]
		if seen[key] {
			t.Fatalf("duplicate row %q in %v", key, rows)
		}
		seen[key] = true
	}
	for _, want := range []string{"a.go:10:11", "a.go:10:24", "a.go:10:35"} {
		if !seen[want] {
			t.Fatalf("missing %s in %v", want, rows)
		}
	}
}

func refStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, s := range x {
			out = append(out, s.(string))
		}
		return out
	}
	return nil
}
