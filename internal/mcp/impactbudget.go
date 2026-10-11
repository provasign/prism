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

// Site lines in the two change_impact layouts (graphtext.go): flat
// "  Name  path:line" and, under a "  path:" header, grouped "    Name  line N".
// Either may end in "  (via X)" and/or "  [test]". Evidence and signature
// lines are indented 4 (flat) or 6 (grouped) and never match these.
var (
	impactSiteLine    = regexp.MustCompile(`^  (\S+)  (\S+):(\d+)((?:  \(via [^)]*\))?(?:  \[test\])?)$`)
	impactGroupedSite = regexp.MustCompile(`^    (\S+)  line (\d+)((?:  \(via [^)]*\))?(?:  \[test\])?)$`)
	impactGroupHeader = regexp.MustCompile(`^  (\S+):$`)
)

// dropImpactEvidence removes evidence/signature lines and keeps every site.
func dropImpactEvidence(text string) []string {
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "    ") && !impactGroupedSite.MatchString(l) {
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

func compactImpactText(text string) string {
	if len(text) <= impactTextBudget {
		return text
	}
	// Step 1: drop evidence/signature lines, keep every site line.
	kept := dropImpactEvidence(text)
	step1 := strings.Join(kept, "\n")
	note1 := "// evidence lines omitted to fit one answer; change_impact on a single name shows them\n"
	if len(step1)+len(note1) <= impactTextBudget {
		return note1 + step1
	}
	// Step 2: group sites per file (path: Name:line ...).
	grouped := groupImpactSites(kept, siteNames)
	note2 := "// evidence omitted and sites grouped per file (path: Name:line ...) to fit one answer\n"
	if len(grouped)+len(note2) <= impactTextBudget {
		return note2 + grouped
	}
	// Step 3: per file, line numbers only (path (n): line line ...).
	lines := groupImpactSites(kept, siteLines)
	note3 := "// evidence and caller names omitted to fit one answer: per file, the site count and line numbers\n"
	if len(lines)+len(note3) <= impactTextBudget {
		return note3 + lines
	}
	// Step 4: per-file counts.
	return "// too many sites for one answer: per-file site counts only; change_impact on one name, or search with paths, lists a file's sites\n" +
		groupImpactSites(kept, siteCounts)
}

type siteForm int

const (
	siteNames siteForm = iota
	siteLines
	siteCounts
)

func groupImpactSites(lines []string, form siteForm) string {
	var out []string
	var order []string
	sites := map[string][]string{}
	flush := func() {
		for _, p := range order {
			switch form {
			case siteCounts:
				out = append(out, fmt.Sprintf("  %s (%d)", p, len(sites[p])))
			case siteLines:
				nums := make([]string, len(sites[p]))
				for i, e := range sites[p] {
					nums[i] = siteLineNumber(e)
				}
				out = append(out, fmt.Sprintf("  %s (%d): %s", p, len(nums), strings.Join(nums, " ")))
			default:
				out = append(out, fmt.Sprintf("  %s: %s", p, strings.Join(sites[p], " ")))
			}
		}
		order, sites = nil, map[string][]string{}
	}
	add := func(path, entry string) {
		if _, seen := sites[path]; !seen {
			order = append(order, path)
		}
		sites[path] = append(sites[path], entry)
	}
	header := ""
	for _, l := range lines {
		if m := impactSiteLine.FindStringSubmatch(l); m != nil {
			header = ""
			add(m[2], m[1]+":"+m[3]+strings.ReplaceAll(m[4], "  ", " "))
			continue
		}
		if m := impactGroupHeader.FindStringSubmatch(l); m != nil {
			header = m[1]
			continue
		}
		if m := impactGroupedSite.FindStringSubmatch(l); m != nil && header != "" {
			add(header, m[1]+":"+m[2]+strings.ReplaceAll(m[3], "  ", " "))
			continue
		}
		flush()
		header = ""
		out = append(out, l)
	}
	flush()
	return strings.Join(out, "\n")
}

// siteLineNumber takes "Name:123 [test]" and returns "123" (or "123t" for a
// test site, so tests stay distinguishable in the line-number form).
func siteLineNumber(entry string) string {
	head := entry
	if i := strings.IndexByte(head, ' '); i >= 0 {
		head = head[:i]
	}
	n := head[strings.LastIndexByte(head, ':')+1:]
	if strings.Contains(entry, "[test]") {
		n += "t"
	}
	return n
}
