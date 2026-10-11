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
	huge := compactImpactText(impactFixtureText(5000, 0))
	if len(huge) > impactTextBudget || !strings.Contains(huge, "per-file site counts only") {
		t.Fatalf("step 3 expected, got %d chars", len(huge))
	}
}
