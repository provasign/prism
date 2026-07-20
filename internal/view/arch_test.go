package view

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func TestParseRule(t *testing.T) {
	r, err := ParseRule("internal/grove -> internal/mcp  # engine must not know the server")
	if err != nil {
		t.Fatal(err)
	}
	if r.From != "internal/grove" || r.To != "internal/mcp" {
		t.Fatalf("parsed = %+v", r)
	}
	for _, bad := range []string{"no arrow here", " -> x", "x -> ", "->"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) accepted", bad)
		}
	}
	if _, err := ParseRules([]string{"a -> b", "broken"}); err == nil {
		t.Fatal("a bad rule in the list must fail loudly, not weaken the gate")
	}
}

func TestMatchComponent(t *testing.T) {
	cases := []struct {
		pattern, comp string
		want          bool
	}{
		{"internal/grove", "internal/grove", true},
		{"internal/grove", "internal/grove/store", true}, // prefix covers subdirs
		{"internal/grove", "internal/grovex", false},
		{"internal/*", "internal/mcp", true},
		{"*", "anything/at/all", true},
		{"cmd", "internal/cmd", false},
	}
	for _, c := range cases {
		if got := matchComponent(c.pattern, c.comp); got != c.want {
			t.Errorf("matchComponent(%q, %q) = %v, want %v", c.pattern, c.comp, got, c.want)
		}
	}
}

func TestCheckRules(t *testing.T) {
	symbols := []grove.SymbolRecord{
		sym("e1", "engine/core/e.go", "function", "core.E", 3, true),
		sym("s1", "server/s.go", "function", "server.S", 7, true),
		sym("u1", "ui/u.go", "function", "ui.U", 9, true),
	}
	edges := []grove.Edge{
		edge("s1", "e1", "calls", "astkit"),     // allowed: server -> engine
		edge("e1", "s1", "uses-type", "native"), // forbidden: engine -> server
		edge("u1", "e1", "calls", "heuristic"),  // forbidden: ui skips server
	}
	v := Build(symbols, edges, Options{})

	rules, err := ParseRules([]string{
		"engine -> server", // prefix: covers engine/core
		"ui -> engine",
	})
	if err != nil {
		t.Fatal(err)
	}
	violations := v.CheckRules(rules)
	if len(violations) != 2 {
		t.Fatalf("violations = %+v, want 2", violations)
	}
	// Every violation must carry concrete sites and its evidence tier.
	for _, viol := range violations {
		if len(viol.Edge.Sites) == 0 {
			t.Fatalf("violation without sites: %+v", viol)
		}
		if viol.MinTier == "" {
			t.Fatalf("violation without tier: %+v", viol)
		}
	}
	// The engine->server violation is precise-tier; ui->engine is heuristic.
	byRule := map[string]Violation{}
	for _, viol := range violations {
		byRule[strings.Fields(viol.Rule)[0]] = viol
	}
	if byRule["engine"].MinTier != "precise" || byRule["ui"].MinTier != "heuristic" {
		t.Fatalf("tiers = %s / %s", byRule["engine"].MinTier, byRule["ui"].MinTier)
	}

	// A rule with no matching edge yields nothing.
	ok, _ := ParseRules([]string{"server -> ui"})
	if got := v.CheckRules(ok); len(got) != 0 {
		t.Fatalf("clean rule reported %+v", got)
	}
}
