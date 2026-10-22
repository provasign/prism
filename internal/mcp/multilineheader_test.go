package mcp

import (
	"strings"
	"testing"
)

// A multiline declaration's editSites row holds only its opening line
// ("def redirect("), so change_impact must still print the signature: the
// parameters are what a signature change edits (change-impact cost audit
// 2026-10-21, werkzeug redirect).
func TestImpactKeepsMultilineSignatureBesideEditRows(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "0")
	srv := compactFixture(t, map[string]string{
		"utils.py": "def redirect(\n    location,\n    code=303,\n    Response=None,\n):\n    return location\n",
		"app.py":   "from utils import redirect\n\n\ndef view():\n    return redirect(\"/\", 302)\n",
	})
	text := callCompact(t, srv, "change_impact", map[string]any{"name": "redirect"})
	if !strings.Contains(text, "utils.py:1:5: def redirect(") {
		t.Fatalf("fixture no longer yields a partial-header edit row:\n%s", text)
	}
	if !strings.Contains(text, "Response=None") {
		t.Errorf("multiline signature dropped beside its partial edit row:\n%s", text)
	}
}
