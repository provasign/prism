package mcp

import (
	"strings"
	"testing"
)

// click Group.get_command: the base method and its subclass override share a
// name in one file; change_impact must answer with the family, not an error.
func TestChangeImpactResolvesSameFileOverrideFamily(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"pkg/core.py": "class Group:\n    def get_thing(self, name):\n        return None\n\n\n" +
			"class Collection(Group):\n    def get_thing(self, name):\n        return super().get_thing(name)\n\n\n" +
			"def use(g: Group):\n    return g.get_thing(\"x\")\n",
		"examples/alias.py": "from pkg.core import Group\n\n\nclass Alias(Group):\n    def get_thing(self, name):\n        return None\n",
	})
	out := callCompact(t, srv, "change_impact", map[string]any{"name": "get_thing", "symbol_file": "pkg/core.py"})
	if strings.Contains(out, "is ambiguous") {
		t.Fatalf("same-file override family still answered with an ambiguity error:\n%s", out)
	}
	for _, want := range []string{"one override family", "Group.get_thing", "examples/alias.py"} {
		if !strings.Contains(out, want) {
			t.Fatalf("family answer missing %q:\n%s", want, out)
		}
	}
}
