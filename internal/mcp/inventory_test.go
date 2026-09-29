package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// countInventory returns how many files a rendered inventory accounts for
// (group counts plus individually listed paths).
func countInventory(lines []string) int {
	n := 0
	for _, l := range lines {
		var c int
		if i := strings.LastIndex(l, " ("); i >= 0 && strings.HasSuffix(l, ")") && strings.Contains(l[i:], " files") {
			fmt.Sscanf(l[i+2:], "%d files", &c)
			n += c
		} else {
			n++
		}
	}
	return n
}

// TestBoundedInventory: a small set lists every path; a large set stays
// within the line budget, keeps dense directories NAMED at the depth where
// files cluster (the zookeeper-api/curator4/curator5 case, BACKLOG item 11),
// and accounts for every file exactly once.
func TestBoundedInventory(t *testing.T) {
	small := []string{"a/x.go", "b/y.go"}
	lines, note := boundedInventory(small, map[string]int{"a/x.go": 2, "b/y.go": 1}, inventoryLineBudget)
	if len(lines) != 2 || lines[0] != "a/x.go (2)" || !strings.Contains(note, "every one") {
		t.Fatalf("small inventory: %v / %q", lines, note)
	}

	var big []string
	hits := map[string]int{}
	// 5 modules × 40 files under a shared src/main/java prefix, plus three
	// small SPI modules and a few root-level files.
	for m := 0; m < 5; m++ {
		for i := 0; i < 40; i++ {
			f := fmt.Sprintf("dubbo-mod%d/src/main/java/pkg%d/F%d.java", m, i%3, i)
			big = append(big, f)
			hits[f] = 1
		}
	}
	for _, spi := range []string{"api", "curator4", "curator5"} {
		for i := 0; i < 3; i++ {
			f := fmt.Sprintf("dubbo-remoting/dubbo-remoting-zookeeper-%s/src/main/java/Z%d.java", spi, i)
			big = append(big, f)
			hits[f] = 2
		}
	}
	big = append(big, "README.md", "pom.xml")
	hits["README.md"], hits["pom.xml"] = 1, 4

	lines, note = boundedInventory(big, hits, inventoryLineBudget)
	if len(lines) > inventoryLineBudget+2 {
		t.Fatalf("%d files rendered as %d lines — not bounded:\n%s", len(big), len(lines), strings.Join(lines, "\n"))
	}
	if got := countInventory(lines); got != len(big) {
		t.Errorf("inventory accounts for %d files, want %d:\n%s", got, len(big), strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"dubbo-remoting-zookeeper-api/", "dubbo-remoting-zookeeper-curator4/", "dubbo-remoting-zookeeper-curator5/", "README.md (1)", "pom.xml (4)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dense small modules and root files must stay named; missing %q in:\n%s", want, joined)
		}
	}
	if !strings.Contains(note, "grouped by directory") {
		t.Errorf("note should explain the grouping: %q", note)
	}

	// files_only shape: no hit counts anywhere, still complete
	lines, _ = boundedInventory(big, nil, inventoryLineBudget)
	for _, l := range lines {
		if strings.Contains(l, "hits") || strings.HasSuffix(l, " (1)") {
			t.Fatalf("files_only inventory must not carry hit counts: %q", l)
		}
	}
	if got := countInventory(lines); got != len(big) {
		t.Errorf("files_only inventory accounts for %d files, want %d", got, len(big))
	}
}
