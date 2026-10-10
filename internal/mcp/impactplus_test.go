package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func newImpactPlusHandler(t *testing.T, files map[string]string) *Handler {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dir, name, body)
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return h
}

func invokeCompactText(t *testing.T, h *Handler, op string, args map[string]any) string {
	t.Helper()
	name, legacy, err := expandCompactCall(map[string]any{"op": op, "args": args})
	if err != nil {
		t.Fatalf("expand %s: %v", op, err)
	}
	out, err := h.Invoke(name, legacy)
	if err != nil {
		t.Fatalf("%s %v: %v", op, args, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T", op, out)
	}
	var text string
	switch op {
	case "change_impact":
		text, ok = renderChangeImpactAsText(m)
	case "lookup":
		text, ok = renderLookupAsText(m)
	}
	if !ok {
		t.Fatalf("%s result did not render as text (unknown key?): %v", op, m)
	}
	return text
}

func TestSignatureParamTypes(t *testing.T) {
	for _, c := range []struct {
		sig, lang string
		want      []string
	}{
		{"TreeTraversingParser(JsonNode n, ObjectReadContext readContext, TokenStreamContext parentContext)", "java",
			[]string{"JsonNode", "ObjectReadContext", "TokenStreamContext"}},
		{"public <T> void put(final Map<String, T> m, @Nullable int[] xs)", "java", []string{"Map<String, T>", "int[]"}},
		{"func (r *Router) Handle(ctx context.Context, a, b string) error", "go", []string{"context.Context", "string", "string"}},
		{"Handle(context.Context, string)", "go", []string{"context.Context", "string"}},
		{"gte(value: util.Numeric, params?: string = 'x')", "typescript", []string{"util.Numeric", "string"}},
		{"run()", "java", []string{}},
	} {
		got, ok := signatureParamTypes(c.sig, c.lang)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("signatureParamTypes(%q) = %v, %v; want %v", c.sig, got, ok, c.want)
		}
	}
	if _, ok := signatureParamTypes("no params here", "java"); ok {
		t.Error("a signature without a parameter list must not parse")
	}
}

// jackson-databind pr6052: the 3-arg constructor added by a partial commit
// is called only by tests; production still constructs through the 2-arg
// overload. Both lookup and change_impact must say so, and `signature` must
// select the overload instead of merging all three.
var treeParserFixture = map[string]string{
	"src/main/java/p/TreeParser.java": `package p;

public class TreeParser {
    public TreeParser(Object n) { this(n, null); }

    public TreeParser(Object n, Context c) {
        this(n, c, null);
    }

    public TreeParser(Object n, Context c, Context parent) {
    }

    public void next() {}
}
`,
	"src/main/java/p/Context.java": `package p;

public class Context {
    public TreeParser treeAsTokens(Object n) {
        return new TreeParser(n, this);
    }
}
`,
	"src/test/java/p/TreeParserTest.java": `package p;

public class TreeParserTest {
    public void parentViaConstructor() {
        TreeParser p = new TreeParser("x", new Context(), new Context());
        p.next();
    }

    public void oneArg() {
        new TreeParser("y");
    }
}
`,
}

func TestChangeImpactFlagsTestOnlyOverloadAndSignatureSelectsIt(t *testing.T) {
	h := newImpactPlusHandler(t, treeParserFixture)

	merged := invokeCompactText(t, h, "change_impact", map[string]any{"name": "TreeParser.TreeParser"})
	if !strings.Contains(merged, "TEST-ONLY API") ||
		!strings.Contains(merged, "TreeParser.TreeParser line 10: 0 production callers (1 test caller); only its own overload(s) at line 6 delegate to it") ||
		!strings.Contains(merged, "overload line 6 is called by 1 production site: Context.treeAsTokens (src/main/java/p/Context.java:4)") {
		t.Fatalf("merged impact lacks the test-only signal:\n%s", merged)
	}

	selected := invokeCompactText(t, h, "change_impact", map[string]any{
		"name":      "TreeParser.TreeParser",
		"signature": "TreeParser(Object n, Context c, Context parent)",
	})
	if !strings.Contains(selected, "signature selected 1 of 3 overloads: TreeParser.TreeParser line 10") {
		t.Fatalf("signature did not select the overload:\n%s", selected)
	}
	if strings.Contains(selected, "Context.treeAsTokens  ") || strings.Contains(selected, "src/main/java/p/Context.java:4:treeAsTokens") {
		t.Fatalf("callers of the other overloads leaked into the selected result:\n%s", selected)
	}

	looked := invokeCompactText(t, h, "lookup", map[string]any{
		"name":      "TreeParser.TreeParser",
		"signature": "TreeParser(Object n, Context c, Context parent)",
	})
	if !strings.Contains(looked, "signature selected 1 of 3 overloads (line 10)") || !strings.Contains(looked, "10→public TreeParser(Object n, Context c, Context parent)") {
		t.Fatalf("lookup signature did not select the overload:\n%s", looked)
	}
	if strings.Contains(looked, "more overload(s)") {
		t.Fatalf("lookup still merged overloads:\n%s", looked)
	}
	if !strings.Contains(looked, "TreeParser.TreeParser line 10: 0 production callers") {
		t.Fatalf("lookup lacks the test-only signal:\n%s", looked)
	}

	outline := invokeCompactText(t, h, "lookup", map[string]any{"name": "TreeParser"})
	if !strings.Contains(outline, "TreeParser.TreeParser line 10: 0 production callers") {
		t.Fatalf("class lookup lacks the test-only overload signal:\n%s", outline)
	}
	if strings.Contains(outline, "next line") {
		t.Fatalf("non-overloaded members must stay quiet:\n%s", outline)
	}
}

func TestLookupQuietForProductionCalledMethod(t *testing.T) {
	h := newImpactPlusHandler(t, treeParserFixture)
	text := invokeCompactText(t, h, "lookup", map[string]any{"name": "Context.treeAsTokens"})
	if strings.Contains(text, "TEST-ONLY") {
		t.Fatalf("a method with no callers at all must not be flagged test-only:\n%s", text)
	}
}

// jackson-databind pr6018: change_impact on the override being added used to
// dead-end with "declares no method".
func TestChangeImpactResolvesNotYetDeclaredOverrideToInherited(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{
		"src/main/java/p/ValueSerializer.java": `package p;

public abstract class ValueSerializer {
    public boolean isUnwrappingSerializer() { return false; }
}
`,
		"src/main/java/p/StdSerializer.java": `package p;

public abstract class StdSerializer extends ValueSerializer {
}
`,
		"src/main/java/p/ConvertingSerializer.java": `package p;

public class ConvertingSerializer extends StdSerializer {
    public void resolve() {}
}
`,
		"src/main/java/p/UnwrappingSerializer.java": `package p;

public class UnwrappingSerializer extends StdSerializer {
    @Override
    public boolean isUnwrappingSerializer() { return true; }
}
`,
		"src/main/java/p/Writer.java": `package p;

public class Writer {
    public boolean check(ValueSerializer s) { return s.isUnwrappingSerializer(); }
}
`,
	})
	text := invokeCompactText(t, h, "change_impact", map[string]any{"name": "ConvertingSerializer.isUnwrappingSerializer"})
	for _, want := range []string{
		"ConvertingSerializer.isUnwrappingSerializer is not declared on ConvertingSerializer; it is inherited from ValueSerializer.isUnwrappingSerializer (src/main/java/p/ValueSerializer.java:4) via ConvertingSerializer -> StdSerializer -> ValueSerializer",
		"src/main/java/p/UnwrappingSerializer.java:4:isUnwrappingSerializer [method]",
		"src/main/java/p/Writer.java:4:check [method]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("inherited impact lacks %q:\n%s", want, text)
		}
	}
	// A header note, not a trailer: it changes how the whole result reads.
	if strings.Index(text, "is not declared on") > strings.Index(text, "relaySites") {
		t.Fatalf("inherited note must precede the inventory:\n%s", text)
	}
}

// jackson-databind pr6061: related co-callers are delivered, labelled, and
// kept out of relaySites.
func TestChangeImpactRendersRelatedOutsideRelaySites(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{
		"src/main/java/p/Gen.java": `package p;

public class Gen {
    private final Ctx _ctx = new Ctx();

    public Gen writePOJO(Object v) {
        _ctx.put(v);
        return this;
    }

    public Gen writeBinary(byte[] data) {
        return writePOJO(data);
    }

    public Gen writeEmbeddedObject(Object o) {
        _ctx.put(o);
        return this;
    }

    static class Ctx {
        void put(Object v) {}
    }
}
`,
	})
	text := invokeCompactText(t, h, "change_impact", map[string]any{"name": "Gen.writeBinary"})
	rel := strings.Index(text, "related (")
	if rel < 0 || !strings.Contains(text[rel:], "Gen.writeEmbeddedObject [co-caller] also calls Gen.Ctx.put (reached from writeBinary via writePOJO:6)") {
		t.Fatalf("related group missing writeEmbeddedObject:\n%s", text)
	}
	relay := text[strings.Index(text, "relaySites"):rel]
	if strings.Contains(relay, "writeEmbeddedObject") {
		t.Fatalf("related site leaked into relaySites:\n%s", text)
	}
}

// zod pr6129: `export { _gte as gte } from "../core/index.js"` is a
// reference site of _gte that no call edge reaches.
func TestChangeImpactRelaysTSReExports(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{
		"src/core/api.ts":    "export function _gte(value: number): number {\n  return value;\n}\n\nexport function _nonnegative(): number {\n  return _gte(0);\n}\n",
		"src/core/index.ts":  "export * from \"./api.js\";\n",
		"src/mini/checks.ts": "export {\n  _gte as gte,\n  _gte as minimum,\n} from \"../core/index.js\";\n",
	})
	text := invokeCompactText(t, h, "change_impact", map[string]any{"name": "_gte"})
	for _, want := range []string{
		"change-impact: 4 site(s)",
		"src/mini/checks.ts:2:gte [re-export]",
		"src/mini/checks.ts:3:minimum [re-export]",
		"src/core/api.ts:5:_nonnegative [function]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("impact lacks %q:\n%s", want, text)
		}
	}
}
