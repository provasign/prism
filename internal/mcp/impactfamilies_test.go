package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// jackson-databind (2026-10-12): a bare serializeWithType names two unrelated
// override families. JsonSerializer<T>'s overrides each bind T differently,
// so the parameter-shape inference split them and answered with the other,
// smaller family as "one external-interface family". A wide bare name in a
// nominal language now partitions into the families the graph knows: one
// family is answered, several are listed with their sizes.
func TestWideBareNameInJavaListsEveryOverrideFamily(t *testing.T) {
	files := map[string]string{
		"p/Gen.java":  "package p;\n\npublic class Gen {}\n",
		"p/Ser.java":  "package p;\n\npublic abstract class Ser<T> {\n    public abstract void swt(T value, Gen g);\n}\n",
		"p/Able.java": "package p;\n\npublic interface Able {\n    void swt(Gen g);\n}\n",
	}
	for i, typ := range []string{"String", "Integer", "Long", "Double", "Short", "Byte"} {
		files[fmt.Sprintf("p/S%d.java", i)] = fmt.Sprintf("package p;\n\npublic class S%d extends Ser<%s> {\n    @Override\n    public void swt(%s value, Gen g) {}\n}\n", i, typ, typ)
	}
	for i := 0; i < 3; i++ {
		files[fmt.Sprintf("p/A%d.java", i)] = fmt.Sprintf("package p;\n\npublic class A%d implements Able {\n    @Override\n    public void swt(Gen g) {}\n}\n", i)
	}
	srv := compactFixture(t, files)
	got := callCompactErr(t, srv, "change_impact", map[string]any{"name": "swt"})
	for _, want := range []string{"2 separate override families", "Ser.swt", "Able.swt", "Ser.swt: 7", "Able.swt: 4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "inferred one external-interface method family") {
		t.Fatalf("nominal families must not be inferred from parameter shapes:\n%s", got)
	}
	// Each listed retry answers its own family.
	ser := callCompact(t, srv, "change_impact", map[string]any{"name": "Ser.swt", "symbol_file": "p/Ser.java"})
	if !strings.Contains(ser, "S5.swt") || strings.Contains(ser, "A0.swt") {
		t.Fatalf("Ser.swt family wrong:\n%s", ser)
	}
}

// One family behind a wide bare name is answered directly, with a note.
func TestWideBareNameWithOneJavaFamilyIsAnswered(t *testing.T) {
	files := map[string]string{
		"p/Gen.java": "package p;\n\npublic class Gen {}\n",
		"p/Ser.java": "package p;\n\npublic abstract class Ser<T> {\n    public abstract void swt(T value, Gen g);\n}\n",
	}
	for i, typ := range []string{"String", "Integer", "Long", "Double", "Short", "Byte", "Float", "Character"} {
		files[fmt.Sprintf("p/S%d.java", i)] = fmt.Sprintf("package p;\n\npublic class S%d extends Ser<%s> {\n    @Override\n    public void swt(%s value, Gen g) {}\n}\n", i, typ, typ)
	}
	srv := compactFixture(t, files)
	got := callCompact(t, srv, "change_impact", map[string]any{"name": "swt"})
	if !strings.Contains(got, "one override family rooted at Ser.swt") || !strings.Contains(got, "S7.swt") {
		t.Fatalf("one family should be answered:\n%s", got)
	}
}
