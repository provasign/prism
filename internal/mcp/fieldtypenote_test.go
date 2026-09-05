package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func TestDeclaredTypeOf(t *testing.T) {
	cases := []struct{ sig, name, lang, want string }{
		{"private TripleConfig triple;", "triple", "java", "TripleConfig"},
		{"private final List<Foo> items = new ArrayList<>();", "items", "java", "List"},
		{"public Optional<Bar> bar", "bar", "java", "Optional"},
		{"private int count;", "count", "java", ""},
		{"private static final String NAME = \"x\";", "NAME", "java", ""},
		{"Triple *TripleConfig", "Triple", "go", "TripleConfig"},
		{"triple: TripleConfig", "triple", "typescript", "TripleConfig"},
		{"triple?: TripleConfig[]", "triple", "typescript", "TripleConfig"},
		{"triple: Optional[TripleConfig] = None", "triple", "python", "Optional"},
		{"private ?Node $node;", "node", "php", "Node"},
		{"private Foo.Bar.TripleConfig triple", "triple", "csharp", "TripleConfig"},
		{"var x = compute()", "x", "csharp", ""},
	}
	for _, c := range cases {
		if got := declaredTypeOf(c.sig, c.name, c.lang); got != c.want {
			t.Errorf("declaredTypeOf(%q, %q, %s) = %q, want %q", c.sig, c.name, c.lang, got, c.want)
		}
	}
}

// TestStructuralNote_FieldPointsAtItsType: a search term that names a field
// gets a headline about the field's TYPE and its fan-out, not silence. The
// dubbo `triple` case: field ProtocolConfig.triple of type TripleConfig,
// used across several classes.
func TestStructuralNote_FieldPointsAtItsType(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "TripleConfig.java", `package p;

public class TripleConfig {
    private int maxBodySize;
    public int getMaxBodySize() { return maxBodySize; }
}
`)
	mustWrite(t, dir, "ProtocolConfig.java", `package p;

public class ProtocolConfig {
    private TripleConfig triple;
    public TripleConfig getTriple() { return triple; }
    public void setTriple(TripleConfig triple) { this.triple = triple; }
}
`)
	mustWrite(t, dir, "Http1Channel.java", `package p;

public class Http1Channel {
    private final TripleConfig tripleConfig;
    public Http1Channel(TripleConfig tripleConfig) { this.tripleConfig = tripleConfig; }
    public int limit() { return tripleConfig.getMaxBodySize(); }
}
`)
	mustWrite(t, dir, "Http2Channel.java", `package p;

public class Http2Channel {
    public int limit(TripleConfig cfg) { return cfg.getMaxBodySize(); }
}
`)

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	n := h.structuralNote(t.Context(), "triple")
	for _, want := range []string{"triple is a field ProtocolConfig.triple", "of type TripleConfig", "prism_change_impact TripleConfig"} {
		if !strings.Contains(n, want) {
			t.Errorf("note %q should contain %q", n, want)
		}
	}
}
