package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func impactFixtureText(callers, evidenceLen int) string {
	var b strings.Builder
	b.WriteString("// A.m — change-impact: many site(s)\ncompleteness: closed\ncallers (" + fmt.Sprint(callers) + "):\n")
	for i := 0; i < callers; i++ {
		fmt.Fprintf(&b, "  Caller%d.run  src/main/java/com/example/deep/pkg/File%d.java:%d\n", i, i%40, i+10)
		if evidenceLen > 0 {
			fmt.Fprintf(&b, "    %s\n", strings.Repeat("x", evidenceLen))
		}
	}
	return b.String()
}

func TestCompactImpactTextKeepsSmallAnswers(t *testing.T) {
	in := impactFixtureText(20, 40)
	if got := compactImpactText(in); got != in {
		t.Error("an answer under budget must be unchanged")
	}
}

func TestCompactImpactTextDropsEvidenceFirst(t *testing.T) {
	in := impactFixtureText(300, 120) // ~60k with evidence, ~25k without
	got := compactImpactText(in)
	if len(got) > impactTextBudget || !strings.Contains(got, "evidence lines omitted") {
		t.Fatalf("step 1 expected, got %d chars:\n%.300s", len(got), got)
	}
	for i := 0; i < 300; i++ {
		if !strings.Contains(got, fmt.Sprintf("Caller%d.run  ", i)) {
			t.Fatalf("site %d lost", i)
		}
	}
}

func TestCompactImpactTextGroupsThenCounts(t *testing.T) {
	in := impactFixtureText(900, 0) // ~80k of bare site lines
	got := compactImpactText(in)
	if len(got) > impactTextBudget || !strings.Contains(got, "grouped per file") {
		t.Fatalf("step 2 expected, got %d chars:\n%.300s", len(got), got)
	}
	for i := 0; i < 900; i++ {
		if !strings.Contains(got, fmt.Sprintf("Caller%d.run:%d", i, i+10)) {
			t.Fatalf("site %d lost in grouping", i)
		}
	}
	big := compactImpactText(impactFixtureText(5000, 0))
	if len(big) > impactTextBudget || !strings.Contains(big, "site count and line numbers") {
		t.Fatalf("line-number form expected, got %d chars", len(big))
	}
	for _, n := range []int{10, 2500, 5009} {
		if !strings.Contains(big, fmt.Sprintf(" %d", n)) {
			t.Fatalf("line %d missing from line-number form", n)
		}
	}
	huge := compactImpactText(impactFixtureText(40000, 0))
	if len(huge) > impactTextBudget || !strings.Contains(huge, "per-file site counts only") {
		t.Fatalf("counts-only expected, got %d chars", len(huge))
	}
}

// The MCP renderer may group callers per file ("  path:" then "    Name  line N",
// evidence at 6 spaces). Dropping evidence must keep those site lines: an
// earlier version dropped every 4-space line and sent file names without sites.
func TestCompactImpactTextKeepsGroupedLayoutSites(t *testing.T) {
	var b strings.Builder
	b.WriteString("// A.m — change-impact: many site(s)\ncallers (600):\n")
	for f := 0; f < 60; f++ {
		fmt.Fprintf(&b, "  src/main/java/com/example/deep/pkg/File%d.java:\n", f)
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&b, "    Caller%d_%d.run  line %d  [test]\n", f, i, i+1)
			fmt.Fprintf(&b, "      %d: %s\n", i+1, strings.Repeat("y", 60))
		}
	}
	got := compactImpactText(b.String())
	if len(got) > impactTextBudget {
		t.Fatalf("over budget: %d", len(got))
	}
	for f := 0; f < 60; f++ {
		for i := 0; i < 10; i++ {
			if !strings.Contains(got, fmt.Sprintf("Caller%d_%d.run", f, i)) {
				t.Fatalf("grouped site Caller%d_%d lost:\n%.600s", f, i, got)
			}
		}
	}
	if strings.Contains(got, "yyyy") {
		t.Error("evidence lines should be dropped")
	}
}

// Over budget, the line partition's lists shrink to their counts before any
// evidence line is dropped.
func TestCompactImpactShrinksPartitionFirst(t *testing.T) {
	var b strings.Builder
	b.WriteString("callSitesByOverload:\n    a/B.java: 1:2, 3:4\n")
	b.WriteString("Every line naming value in this repository (whole word, 900): 1 in editSites above, 0 code line(s) naming something else, 0 string literal(s), 899 comment/doc line(s).\n")
	b.WriteString("comments/docs naming value (899):\n")
	for i := 0; i < 899; i++ {
		b.WriteString(fmt.Sprintf("  docs/f.md:%d: %s\n", i+1, strings.Repeat("value ", 10)))
	}
	b.WriteString("related (1; NOT in the change set):\n")
	got := compactImpactText(b.String())
	for _, want := range []string{"    a/B.java: 1:2, 3:4", "comments/docs naming value (899): not listed, to fit one answer", "related (1;", "Every line naming value"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "docs/f.md:1:") || strings.Contains(got, "evidence lines omitted") {
		t.Errorf("partition list kept or evidence dropped:\n%s", got[:400])
	}
}
