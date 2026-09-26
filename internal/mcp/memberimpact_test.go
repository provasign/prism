package mcp

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func memberFixture() *grove.ChangeImpactResult {
	return &grove.ChangeImpactResult{
		Query:        "Context.Errors",
		MemberKind:   "field",
		Completeness: "member-accesses",
		Declarations: []grove.SymbolRecord{{Name: "Errors", QualifiedName: "Context.Errors", FilePath: "context.go", Kind: "field", Language: "go", Span: grove.SpanInfo{Start: 82, End: 82}, Signature: "Errors errorMsgs"}},
		Accesses: []grove.MemberAccess{
			{FilePath: "context.go", Line: 82, Access: "decl", Evidence: "declaration", Enclosing: "Context.Errors"},
			{FilePath: "context.go", Line: 111, Access: "write", Evidence: "self receiver in Context", Enclosing: "Context.reset", Text: "\tc.Errors = c.Errors[:0]"},
			{FilePath: "logger.go", Line: 215, Access: "read", Evidence: "receiver c typed Context", Enclosing: "LoggerWithConfig"},
		},
		AmbiguousAccesses: []grove.MemberAccess{
			{FilePath: "x_test.go", Line: 9, Access: "read", Evidence: "receiver v untyped; 1 other type(s) also declare Errors", Enclosing: "TestX"},
		},
		ExcludedAccesses: 2,
		AccessCoverage:   "partial",
		AccessNote:       "2 access line(s) confirmed; 1 more match the name",
	}
}

// The failure this replaces: a field returned its declaration alone and the
// instruction to "copy this 1-site inventory". A member result relays the
// confirmed LINES, keeps name-only matches in a labelled bucket, and never
// folds them into the inventory it tells the agent to copy.
func TestMemberImpactOutputRelaysConfirmedLinesOnly(t *testing.T) {
	out := memberImpactOutput(memberFixture(), false)
	relay, _ := out["relaySites"].([]string)
	want := []string{
		"context.go:82:Context.Errors [decl]",
		"context.go:111:Context.reset [write]",
		"logger.go:215:LoggerWithConfig [read]",
	}
	if strings.Join(relay, "|") != strings.Join(want, "|") {
		t.Fatalf("relaySites = %v, want %v", relay, want)
	}
	if out["totalSites"] != 3 || out["ambiguousSites"] != 1 || out["excludedAccesses"] != 2 {
		t.Fatalf("counts: total=%v ambiguous=%v excluded=%v", out["totalSites"], out["ambiguousSites"], out["excludedAccesses"])
	}
	amb, _ := out["ambiguousAccesses"].([]map[string]any)
	if len(amb) != 1 || !strings.Contains(amb[0]["reason"].(string), "untyped") {
		t.Fatalf("ambiguousAccesses = %v", amb)
	}
	if note, _ := out["relayNote"].(string); !strings.Contains(note, "ambiguousAccesses") || !strings.Contains(note, "CONFIRMED") {
		t.Fatalf("relayNote must separate confirmed from ambiguous: %q", note)
	}
	if cov, _ := out["coverageNote"].(string); !strings.Contains(cov, "not call edges") {
		t.Fatalf("coverageNote must say accesses come from source, not call edges: %q", cov)
	}
	if out["callerCoverage"] != "partial" || out["familyCompleteness"] != "member-accesses" {
		t.Fatalf("labels: callerCoverage=%v familyCompleteness=%v", out["callerCoverage"], out["familyCompleteness"])
	}
}

func TestMemberImpactTextRendering(t *testing.T) {
	out := memberImpactOutput(memberFixture(), false)
	text, ok := renderChangeImpactAsText(out)
	if !ok {
		t.Fatal("member result fell back to JSON")
	}
	for _, want := range []string{
		"Context.Errors — change-impact (field): 3 confirmed site(s), 1 ambiguous",
		"relaySites (3 confirmed; copy this inventory):",
		"context.go:111:Context.reset [write]",
		"ambiguousAccesses (name match, receiver untyped — check each) (1):",
		"x_test.go:9  TestX  [read] [test] — receiver v untyped",
		"excludedAccesses: 2",
		"accessCoverage: partial",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "1; copy this inventory") {
		t.Errorf("stale 1-site relay wording:\n%s", text)
	}
}
