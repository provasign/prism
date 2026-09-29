package mcp

import (
	"strings"
	"testing"
)

// jansson (2026-09-26 names sweep): lookup of json_object_get showed the
// 2-line jansson.h prototype as the primary body and called the pair
// ambiguous. The definition is the body to read; the prototype stays listed.
func TestLookupPrefersCDefinitionOverHeaderPrototype(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"src/jansson.h": "#ifndef J_H\n#define J_H\ntypedef struct json_t json_t;\nint json_object_size(const json_t *object);\n#endif\n",
		"src/value.c":   "#include \"jansson.h\"\n\nint json_object_size(const json_t *json)\n{\n    return definition_body(json);\n}\n",
	})
	value, err := h.Invoke("prism_lookup", map[string]any{"name": "json_object_size"})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	if out["ambiguous"] == true {
		t.Fatalf("a prototype and its definition are not ambiguous: %v", out["candidates"])
	}
	text, ok := renderLookupAsText(out)
	if !ok {
		t.Fatalf("lookup result did not render as text: %v", out)
	}
	if !strings.Contains(text, "definition_body") || !strings.Contains(text, "src/value.c") {
		t.Fatalf("definition body must be primary:\n%s", text)
	}
	if !strings.Contains(text, "declared: json_object_size (src/jansson.h:4-4)") {
		t.Fatalf("prototype must stay listed:\n%s", text)
	}
}
