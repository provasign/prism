package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestGoAddedTrailingVariadic(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		want          bool
	}{
		{"func f(x int) int", "func f(x int, opts ...string) int", true},
		{"func (h *Handler) f(x int)", "func (h *Handler) f(x int, opts ...string)", true},
		{"func f(x int)", "func f(x string, opts ...string)", false},
		{"func f(x int) int", "func f(x int, opts ...string) string", false},
		{"func f(x int)", "func f(x int, opts string)", false},
		{"func f(x int)", "func g(x int, opts ...string)", false},
		{"nonsense", "func f(x int, opts ...string)", false},
	} {
		if got := goAddedTrailingVariadic(tc.before, tc.after); got != tc.want {
			t.Errorf("goAddedTrailingVariadic(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestVerifyOptionalGoVariadicDoesNotFlagValidCallers(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	path := filepath.Join(dir, "core", "core.go")
	if err := os.WriteFile(path, []byte("package core\n\ntype S struct{}\n\nfunc (S) Do(x string, n ...int) string { return x }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "complete" || len(anySlice(m["missedSites"])) != 0 || len(anySlice(m["signatureChanges"])) != 0 {
		t.Fatalf("optional variadic parameter flagged existing callers: %v", m)
	}
}

func TestSyntheticAnonymousJavaSymbol(t *testing.T) {
	for _, tc := range []struct {
		file, name string
		want       bool
	}{
		{"src/Builder.java", "Builder.<anonymous@286:43>.match", true},
		{"src/Builder.java", "Builder.NamedMatcher.match", false},
		{"src/builder.go", "Builder.<anonymous@286:43>.match", false},
	} {
		got := isSyntheticAnonymousJavaSymbol(grove.SymbolRecord{
			FilePath: tc.file, QualifiedName: tc.name,
		})
		if got != tc.want {
			t.Errorf("isSyntheticAnonymousJavaSymbol(%q, %q) = %v, want %v", tc.file, tc.name, got, tc.want)
		}
	}
}
