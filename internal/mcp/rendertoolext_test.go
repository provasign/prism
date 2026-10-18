package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The CLI's --format text and the MCP server share RenderToolText, so an
// answer must carry the same evidence (edit sites) through both.
func TestRenderToolTextKeepsEditSitesForCLIAndMCP(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"pkg/core.py": "class Group:\n    def get_command(self, ctx, name):\n        return None\n\n\ndef resolve(g: Group):\n    return g.get_command(None, 'x')\n",
	})
	args := map[string]any{"query": "Group.get_command"}
	out, err := h.Invoke("prism_change_impact", args)
	if err != nil {
		t.Fatal(err)
	}
	for _, compact := range []bool{true, false} {
		text, ok := h.RenderToolText(compact, "prism_change_impact", args, out)
		if !ok || !strings.Contains(text, "editSites (") || !strings.Contains(text, "pkg/core.py:2:9:") {
			t.Errorf("compact=%v lost edit sites (rendered=%v):\n%s", compact, ok, text)
		}
	}
}

func TestCutUTF8NeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("─", 100)
	for n := 0; n <= len(s)+1; n++ {
		if got := cutUTF8(s, n); !utf8.ValidString(got) || len(got) > n && n < len(s) {
			t.Fatalf("cutUTF8(n=%d) = %q (valid=%v)", n, got, utf8.ValidString(got))
		}
	}
	if got := cutUTF8("abc", 10); got != "abc" {
		t.Fatalf("short string changed: %q", got)
	}
}
