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
	other := strings.Join(toStrings(anySlice(out["otherMentions"])), "\n")
	if !strings.Contains(other, "Every line naming get_command in this repository (whole word, 5): 4 in editSites above, 0 code line(s) naming something else, 1 comment/doc line(s).") {
		t.Errorf("partition wrong:\n%s", other)
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

// evidenceOnly drops the line lists (editSites and the every-line
// partition, which name other symbols' lines on purpose) so a test can
// check what the evidence sections say.
func evidenceOnly(rendered string) string {
	var b strings.Builder
	skip := false
	for _, l := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(l, "editSites (") {
			skip = true
		}
		if skip && (strings.HasPrefix(l, "familyCompleteness:") || strings.HasPrefix(l, "callerCoverage:") || strings.HasPrefix(l, "//")) {
			skip = false
		}
		if !skip {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
