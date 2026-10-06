package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func ambiguityAnswer(syms ...map[string]any) map[string]any {
	return map[string]any{"symbols": syms}
}

func ambiguitySymbol(name, qn, file string, start, end int, match string) map[string]any {
	return map[string]any{"name": name, "qualifiedName": qn, "filePath": file,
		"span": map[string]any{"start": start, "end": end}, "matchKind": match}
}

func ambiguityRegion(name, qn, file string, start, end int) searchSourceRegion {
	return searchSourceRegion{file: file, hit: start, start: start, end: end, hasSymbol: true,
		symbol: grove.SymbolRecord{Name: name, QualifiedName: qn, FilePath: file, Span: grove.SpanInfo{Start: start, End: end}}}
}

// flask: make_response is both Flask.make_response and the helper. A search
// body for one of them, under a header that never mentions the other, was
// read as "the" make_response. The body is replaced by a line naming both.
func TestSearchBodyForSharedNameIsReplacedByCandidates(t *testing.T) {
	out := ambiguityAnswer(
		ambiguitySymbol("make_response", "Flask.make_response", "src/flask/app.py", 1227, 1367, "name-exact"),
		ambiguitySymbol("make_response", "make_response", "src/flask/helpers.py", 151, 197, "name-exact"),
		ambiguitySymbol("test_make_response", "test_make_response", "tests/test_basic.py", 1287, 1306, "name-substring"),
	)
	region := ambiguityRegion("make_response", "make_response", "src/flask/helpers.py", 151, 197)
	others := sharedNameSymbols(out, region)
	if len(others) != 1 || others[0] != "Flask.make_response src/flask/app.py:1227-1367" {
		t.Fatalf("other same-named symbols = %v", others)
	}
	h := &Handler{Root: t.TempDir()}
	got := h.renderSearchBodiesUnlessShared(out, []searchSourceRegion{region}, true)
	if !strings.Contains(got, `No body: 2 different symbols share the name "make_response"`) ||
		!strings.Contains(got, "make_response src/flask/helpers.py:151-197") ||
		!strings.Contains(got, "Flask.make_response src/flask/app.py:1227-1367") ||
		!strings.Contains(got, "op=lookup") {
		t.Fatalf("shared-name note missing or incomplete:\n%s", got)
	}
	if strings.Contains(got, "Full enclosing body") || strings.Contains(got, "WINDOW") {
		t.Fatalf("a body was still delivered for a shared name:\n%s", got)
	}
}

// A class and its own constructor share a name but are one piece of code;
// a unique name has no other exact-name symbol. Neither loses its body.
func TestSearchBodyKeptForConstructorAndUniqueNames(t *testing.T) {
	ctorOut := ambiguityAnswer(
		ambiguitySymbol("PropertyValueBuffer", "PropertyValueBuffer", "src/PropertyValueBuffer.java", 20, 400, "name-exact"),
		ambiguitySymbol("PropertyValueBuffer", "PropertyValueBuffer.PropertyValueBuffer", "src/PropertyValueBuffer.java", 80, 95, "name-exact"),
	)
	ctor := ambiguityRegion("PropertyValueBuffer", "PropertyValueBuffer.PropertyValueBuffer", "src/PropertyValueBuffer.java", 80, 95)
	if others := sharedNameSymbols(ctorOut, ctor); len(others) != 0 {
		t.Fatalf("class and its own constructor treated as different symbols: %v", others)
	}
	uniqueOut := ambiguityAnswer(ambiguitySymbol("full_dispatch_request", "Flask.full_dispatch_request", "src/flask/app.py", 995, 1020, "name-exact"))
	unique := ambiguityRegion("full_dispatch_request", "Flask.full_dispatch_request", "src/flask/app.py", 995, 1020)
	if others := sharedNameSymbols(uniqueOut, unique); len(others) != 0 {
		t.Fatalf("unique name reported as shared: %v", others)
	}
}
