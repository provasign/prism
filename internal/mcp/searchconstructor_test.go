package mcp

import (
	"strings"
	"testing"
)

// gson pr3112 / jackson-dataformat-xml: searching a class name delivered
// its constructor (the constructor line outscores the class declaration),
// and the agent read the whole class next. The named class is delivered
// when it fits; an oversized class gets no body rather than its constructor.
func TestSearchForClassNameDeliversClassNotConstructor(t *testing.T) {
	big := "package p;\n\npublic class BigWrapper {\n    private final int a;\n\n    public BigWrapper(int a) {\n        this.a = compute(a);\n    }\n\n" +
		strings.Repeat("    int pad() { return 1; }\n", 200) + "}\n"
	srv := compactFixture(t, map[string]string{
		"Wrapper.java":    "package p;\n\npublic class Wrapper {\n    private final int a;\n\n    public Wrapper(int a) {\n        this.a = compute(a);\n    }\n\n    int get() {\n        return a;\n    }\n}\n",
		"BigWrapper.java": big,
		"Use.java":        "package p;\n\nclass Use {\n    Object w = new Wrapper(1);\n    Object b = new BigWrapper(2);\n}\n",
	})
	small := callCompact(t, srv, "search", map[string]any{"terms": []any{"Wrapper"}, "scope": "text"})
	if strings.Contains(small, "body Wrapper.Wrapper") || !strings.Contains(small, "int get()") {
		t.Fatalf("class-name search should deliver the small class, not its constructor:\n%s", small)
	}
	large := callCompact(t, srv, "search", map[string]any{"terms": []any{"BigWrapper"}, "scope": "text"})
	if strings.Contains(large, "Full enclosing body") || strings.Contains(large, "// WINDOW") {
		t.Fatalf("oversized class should give no body, not its constructor:\n%s", large)
	}
}
