package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// A field rename plan used to print "1 site(s)" above 22 edits and list the
// field's own declaration as unresolved. The header now counts what the
// buckets hold, and each data-member edit carries its evidence.
func TestRenamePlanTextCountsBuckets(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"query":"Context.Errors","newName":"Errs","totalSites":1,
		"edits":[{"filePath":"context.go","line":82,"before":"Errors errorMsgs","after":"Errs errorMsgs","reason":"declaration"},
		         {"filePath":"context.go","line":111,"before":"c.Errors = nil","after":"c.Errs = nil","reason":"self receiver in Context"}],
		"ambiguous":[{"filePath":"x.go","line":3,"before":"v.Errors","after":"v.Errs","reason":"receiver v untyped"}],
		"ambiguousNote":"these lines name the member but their receiver could not be typed"}`), &m)
	out := capture(t, func() { printOutput(m, formatText) })
	for _, want := range []string{
		"Context.Errors → Errs — rename-plan: 3 site(s): 2 edit(s), 1 ambiguous, 0 unresolved",
		"// declaration", "// receiver v untyped", "receiver could not be typed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestChangeImpactTextRendersMemberResult(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"query":"DefaultWriter","memberKind":"variable","totalSites":2,
		"completeness":"member-accesses","accessCoverage":"receiver-typed",
		"declarations":[{"qualifiedName":"DefaultWriter","filePath":"mode.go","line":42,"signature":"var DefaultWriter io.Writer"}],
		"accesses":[{"filePath":"mode.go","line":42,"access":"decl"},{"filePath":"debug.go","line":69,"access":"read","enclosing":"debugPrint"}],
		"relaySites":["debug.go:69:debugPrint [read]","mode.go:42:DefaultWriter [decl]"]}`), &m)
	out := capture(t, func() { printOutput(m, formatText) })
	for _, want := range []string{
		"DefaultWriter — change-impact (variable): 2 confirmed site(s), 0 ambiguous",
		"relaySites (2 confirmed; copy this inventory):", "debug.go:69:debugPrint [read]",
		"accessCoverage: receiver-typed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
