package mcp

import (
	"strings"
	"testing"
)

// editSites lists every occurrence the change touches in grep's shape:
// the declaration's and overrides' name lines and each call position, with
// the line text, so a text edit needs no second search.
func TestChangeImpactEditSites(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"pkg/core.py": "class Group:\n    def get_command(self, ctx, name):\n        \"\"\"Look up get_command.\"\"\"\n        return None\n\n\nclass Sub(Group):\n    def get_command(self, ctx, name):\n        return super().get_command(ctx, name)\n\n\ndef resolve(g: Group):\n    return g.get_command(None, 'x')\n",
	})
	raw, err := h.Invoke("prism_change_impact", map[string]any{"query": "Group.get_command"})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	rows := toStrings(anySlice(out["editSites"]))
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"pkg/core.py:2:9: def get_command(self, ctx, name):",
		"pkg/core.py:8:9: def get_command(self, ctx, name):",
		"pkg/core.py:13:",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in editSites:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "pkg/core.py:3:") {
		t.Errorf("a docstring is no edit site:\n%s", joined)
	}
	rendered, ok := renderChangeImpactAsText(out)
	if !ok || !strings.Contains(rendered, "editSites (") {
		t.Fatalf("editSites not rendered:\n%s", rendered)
	}
}

func toStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strings.TrimSpace(v.(string)))
	}
	return out
}
