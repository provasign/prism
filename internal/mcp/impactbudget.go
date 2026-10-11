package mcp

import (
	"fmt"
	"regexp"
	"strings"
)

// impactTextBudget bounds one change_impact answer. Claude Code refuses a tool
// result past ~25k tokens and saves it to a file the agent rarely opens: a
// 6-method jackson batch (58.8k characters) and hono Hono.request (77k) both
// went that way on 2026-10-11 and the agents grepped instead. Past the budget
// the answer is shortened in steps that keep every site: evidence lines go
// first, then sites are grouped per file, and only then do sites become
// per-file counts.
const impactTextBudget = 36000

var impactSiteLine = regexp.MustCompile(`^  (\S+)  (\S+):(\d+)$`)

func compactImpactText(text string) string {
	if len(text) <= impactTextBudget {
		return text
	}
	// Step 1: drop indented evidence/signature lines (4+ spaces).
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "    ") {
			continue
		}
		kept = append(kept, l)
	}
	step1 := strings.Join(kept, "\n")
	note1 := "// evidence lines omitted to fit one answer; change_impact on a single name shows them\n"
	if len(step1)+len(note1) <= impactTextBudget {
		return note1 + step1
	}
	// Step 2: group runs of "  Name  path:line" lines per file.
	grouped := groupImpactSites(kept, false)
	note2 := "// evidence omitted and sites grouped per file (path: Name:line ...) to fit one answer\n"
	if len(grouped)+len(note2) <= impactTextBudget {
		return note2 + grouped
	}
	// Step 3: per-file counts.
	return "// too many sites for one answer: per-file site counts only; change_impact on one name, or search with paths, lists a file's sites\n" +
		groupImpactSites(kept, true)
}

func groupImpactSites(lines []string, countsOnly bool) string {
	var out []string
	var order []string
	sites := map[string][]string{}
	flush := func() {
		for _, p := range order {
			if countsOnly {
				out = append(out, fmt.Sprintf("  %s (%d)", p, len(sites[p])))
			} else {
				out = append(out, fmt.Sprintf("  %s: %s", p, strings.Join(sites[p], " ")))
			}
		}
		order, sites = nil, map[string][]string{}
	}
	for _, l := range lines {
		if m := impactSiteLine.FindStringSubmatch(l); m != nil {
			if _, seen := sites[m[2]]; !seen {
				order = append(order, m[2])
			}
			sites[m[2]] = append(sites[m[2]], m[1]+":"+m[3])
			continue
		}
		flush()
		out = append(out, l)
	}
	flush()
	return strings.Join(out, "\n")
}
