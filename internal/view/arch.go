package view

import (
	"fmt"
	"path"
	"strings"
)

// Architecture rules: declared component dependencies the induced view must
// not contain. A rule is one line — "<from> -> <to>" — and a violation is an
// induced edge matching it, reported with its concrete constituent sites and
// the tier of its weakest evidence. Deterministic end to end: same rules,
// same index, same verdict. This is what turns the map from descriptive
// into enforceable (a CI gate no prompt-only reviewer can match).

// Rule is one parsed deny rule.
type Rule struct {
	Raw  string `json:"raw"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ParseRule parses "<from> -> <to>". Each side is a component name
// (directory), a prefix (covers subdirectories), a path.Match glob, or "*".
func ParseRule(s string) (Rule, error) {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	from, to, ok := strings.Cut(s, "->")
	if !ok {
		return Rule{}, fmt.Errorf("arch rule %q: want \"<from> -> <to>\"", s)
	}
	r := Rule{Raw: s, From: strings.TrimSpace(from), To: strings.TrimSpace(to)}
	if r.From == "" || r.To == "" {
		return Rule{}, fmt.Errorf("arch rule %q: empty side", s)
	}
	return r, nil
}

// ParseRules parses a rule list, collecting every error (a bad rule must
// fail loudly, not silently weaken the gate).
func ParseRules(raw []string) ([]Rule, error) {
	var rules []Rule
	var errs []string
	for _, s := range raw {
		r, err := ParseRule(s)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		rules = append(rules, r)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return rules, nil
}

// matchComponent reports whether pattern covers component: exact name,
// path prefix (pattern "a/b" covers "a/b/c"), glob, or "*".
func matchComponent(pattern, comp string) bool {
	if pattern == "*" || pattern == comp {
		return true
	}
	if strings.HasPrefix(comp, pattern+"/") {
		return true
	}
	if ok, err := path.Match(pattern, comp); err == nil && ok {
		return true
	}
	return false
}

// Violation is one induced edge that breaks a declared rule. The edge
// carries its constituent sites — the exact file:line crossings to fix.
type Violation struct {
	Rule    string      `json:"rule"`
	Edge    InducedEdge `json:"edge"`
	MinTier string      `json:"minTier"`
}

// CheckRules validates the view against the rules. Exact over the induced
// graph: every reported violation is backed by concrete primitive edges,
// and the verdict's confidence is bounded by the weakest evidence tier of
// each violating edge (a heuristic-tier violation deserves a look, not an
// automatic build break — the caller decides).
func (v *View) CheckRules(rules []Rule) []Violation {
	var out []Violation
	for _, r := range rules {
		for i := range v.Edges {
			e := &v.Edges[i]
			if matchComponent(r.From, e.From) && matchComponent(r.To, e.To) {
				out = append(out, Violation{
					Rule: r.Raw, Edge: *e, MinTier: MinTier(e.Tiers),
				})
			}
		}
	}
	return out
}
