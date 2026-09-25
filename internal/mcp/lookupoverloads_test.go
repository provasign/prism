package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// commons-lang pr1713 (2026-09-25 rerun): lookup of indexOfDifference returned
// the varargs overload's body and "AMBIGUOUS" with two identical candidate
// lines. The agent fixed only the overload it was shown; the two-arg overload
// carried the other half of the bug. Overloads must arrive with their bodies.
const overloadJava = `package p;
public class Str {
    public static int diff(final CharSequence... css) {
        return varargsBody();
    }
    public static int diff(final CharSequence a, final CharSequence b) {
        return twoArgBody();
    }
    public static int other() { return 0; }
}
`

func TestLookupDeliversSameFileOverloadBodies(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"p/Str.java": overloadJava})
	for _, name := range []string{"diff", "Str.diff"} {
		value, err := h.Invoke("prism_lookup", map[string]any{"name": name})
		if err != nil {
			t.Fatal(err)
		}
		out := value.(map[string]any)
		if out["ambiguous"] == true {
			t.Fatalf("%s: overloads in one file are not cross-symbol ambiguity: %v", name, out["candidates"])
		}
		overloads := anySlice(out["overloads"])
		if len(overloads) != 1 {
			t.Fatalf("%s: want 1 extra overload, got %v", name, out["overloads"])
		}
		text, ok := renderLookupAsText(out)
		if !ok || !strings.Contains(text, "varargsBody") || !strings.Contains(text, "twoArgBody") {
			t.Fatalf("%s: both overload bodies must render: %t\n%s", name, ok, text)
		}
		if !strings.Contains(text, "p/Str.java:6-8") {
			t.Fatalf("%s: overload must carry its own span:\n%s", name, text)
		}
	}
}

func TestLookupOverloadsHonorFieldsProjection(t *testing.T) {
	h := evidenceHandler(t, map[string]string{"p/Str.java": overloadJava})
	value, err := h.Invoke("prism_lookup", map[string]any{"name": "Str.diff", "fields": []any{"signature"}})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	overloads := anySlice(out["overloads"])
	if len(overloads) != 1 {
		t.Fatalf("want 1 overload, got %v", out)
	}
	o := overloads[0].(map[string]any)
	if o["signature"] == nil || o["body"] != nil || o["content"] != nil {
		t.Fatalf("signature-only projection leaked a body or lost the signature: %v", o)
	}
	text, _ := renderLookupAsText(out)
	if strings.Contains(text, "twoArgBody") || strings.Contains(text, "body not delivered") {
		t.Fatalf("projection must not deliver or apologize for bodies:\n%s", text)
	}
}

// A family larger than the 25-hit name search (StringUtils.join has 26) must
// still be complete, with bodies past the byte budget reduced to signature +
// span so the agent can read them by range.
func TestLookupOverloadFamilyIsCompleteAndBudgeted(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p;\npublic class Big {\n")
	pad := strings.Repeat("        int filler = 0; // padding to spend the lookup body budget\n", 8)
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "    public static int join(int a%d, String s) {\n%s        return marker%d();\n    }\n", i, pad, i)
	}
	b.WriteString("}\n")
	h := evidenceHandler(t, map[string]string{"p/Big.java": b.String()})
	value, err := h.Invoke("prism_lookup", map[string]any{"name": "Big.join"})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	overloads := anySlice(out["overloads"])
	if len(overloads) != 29 {
		t.Fatalf("want all 29 other overloads despite the name-search cap, got %d", len(overloads))
	}
	delivered, omitted := 0, 0
	for _, raw := range overloads {
		o := raw.(map[string]any)
		if o["content"] != nil {
			delivered++
		} else if o["bodyOmitted"] == true && o["signature"] != "" {
			omitted++
		}
	}
	if delivered == 0 || omitted == 0 || delivered+omitted != 29 {
		t.Fatalf("budget split wrong: delivered=%d omitted=%d", delivered, omitted)
	}
	text, _ := renderLookupAsText(out)
	if !strings.Contains(text, "body not delivered") || !strings.Contains(text, "29 more overload(s)") {
		t.Fatalf("omitted bodies must say how to fetch them:\n%s", text[:min(len(text), 600)])
	}
}
