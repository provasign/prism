package compression

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// TestSemanticDelta_StaleIndexCannotMaskEdit pins the fix for the stale-index
// hazard: symbol SHAs are computed from the delivered content's bytes at each
// span, never from the index's RawText. When the index lags the working tree,
// the stale RawText still hashes to the previous delivery and would pointer a
// freshly edited body out of the response.
func TestSemanticDelta_StaleIndexCannotMaskEdit(t *testing.T) {
	oldBody := "func Login() {\n\told()\n}"
	newBody := "func Login() {\n\tbrandNewCall()\n}"
	oldContent := "package a\n\n" + oldBody + "\n\nfunc Logout() {}\n"
	newContent := "package a\n\n" + newBody + "\n\nfunc Logout() {}\n"

	// Index state is stale: spans and RawText describe the OLD content.
	staleSymbols := []grove.SymbolRecord{
		{Name: "Login", QualifiedName: "Login", Span: grove.SpanInfo{Start: 3, End: 5}, RawText: oldBody},
		{Name: "Logout", QualifiedName: "Logout", Span: grove.SpanInfo{Start: 7, End: 7}, RawText: "func Logout() {}"},
	}
	// Previous delivery recorded SHAs from the OLD content.
	prev := computeSymbolSHAs(staleSymbols, oldContent)

	delta, ok := renderSemanticDelta(staleSymbols, newContent, prev)
	if ok && !strings.Contains(delta, "brandNewCall()") {
		t.Fatalf("edited body was masked by a cached pointer:\n%s", delta)
	}
	// Logout is genuinely unchanged and may be pointered; if a delta was
	// produced at all, the changed Login body must be present verbatim.
	if ok && strings.Contains(delta, "[prism:cached] Login") {
		t.Fatalf("stale Login symbol pointered:\n%s", delta)
	}
}
