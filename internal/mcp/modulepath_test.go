package mcp

import (
	"strings"
	"testing"
)

// A module-qualified path (werkzeug.utils.redirect) answers with the symbol's
// change set instead of "no type named utils" (Codex agent, 2026-10-17).
func TestChangeImpactResolvesModuleQualifiedPath(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"src/werkzeug/utils.py":             "def redirect(location, code=302):\n    return location\n",
		"src/werkzeug/routing.py":           "from werkzeug.utils import redirect\n\n\ndef go(target):\n    return redirect(target)\n",
		"src/werkzeug/wrappers/response.py": "class Response:\n    def get_data(self, as_text=False):\n        return b''\n\n\ndef drain(r: Response):\n    return r.get_data()\n",
		"src/other/utils.py":                "def helper():\n    return 1\n",
	})
	out := callCompact(t, srv, "change_impact", map[string]any{"name": "werkzeug.utils.redirect"})
	for _, want := range []string{"routing.py", "module path", "src/werkzeug/utils.py"} {
		if !strings.Contains(out, want) {
			t.Fatalf("module path answer lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no type named") {
		t.Fatalf("module path still reported as a missing type:\n%s", out)
	}

	out = callCompact(t, srv, "change_impact", map[string]any{"name": "werkzeug.wrappers.Response.get_data"})
	if !strings.Contains(out, "drain") || !strings.Contains(out, "Response.get_data") {
		t.Fatalf("module path with a type did not resolve to Response.get_data:\n%s", out)
	}

	// A path no file matches is not guessed from the bare leaf.
	errText := callCompactErr(t, srv, "change_impact", map[string]any{"name": "flask.helpers.redirect"})
	if !strings.Contains(errText, "no type named") || strings.Contains(errText, "routing.py") {
		t.Fatalf("unmatched module path must keep the error and not guess:\n%s", errText)
	}
}

func TestModuleMatchesFile(t *testing.T) {
	for _, tc := range []struct {
		file string
		mods []string
		want bool
	}{
		{"src/werkzeug/utils.py", []string{"werkzeug", "utils"}, true},
		{"werkzeug/utils/__init__.py", []string{"werkzeug", "utils"}, true},
		{"src/werkzeug/utils.py", []string{"utils"}, true},
		{"src/other/utils.py", []string{"werkzeug", "utils"}, false},
		{"pkg/router/index.ts", []string{"pkg", "router"}, true},
		{"internal/mcp/tools.go", []string{"mcp"}, false},
	} {
		if got := moduleMatchesFile(tc.file, tc.mods); got != tc.want {
			t.Errorf("moduleMatchesFile(%q, %v) = %v, want %v", tc.file, tc.mods, got, tc.want)
		}
	}
}
