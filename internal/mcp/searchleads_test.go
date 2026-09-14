package mcp

import (
	"strings"
	"testing"
)

func TestSearchLeadsPrefersSourceAndCombinesTermEvidence(t *testing.T) {
	results := []map[string]any{
		{"query": "soft_wrap", "textHits": []map[string]any{
			{"file": "tests/test_console.py", "hits": []map[string]any{{"line": 10, "text": "def test_soft_wrap"}}},
			{"file": "rich/console.py", "hits": []map[string]any{{"line": 20, "text": "if soft_wrap:"}}},
		}},
		{"query": "new_line_start", "textHits": []map[string]any{
			{"file": "rich/console.py", "hits": []map[string]any{{"line": 30, "text": "new_line_start = True"}}},
		}},
	}
	leads := searchLeads(results)
	if len(leads) != 1 || leads[0]["file"] != "rich/console.py" || leads[0]["terms"] != 2 {
		t.Fatalf("multi-term source evidence did not lead: %#v", leads)
	}
	text, ok := renderSearchAsText(map[string]any{"results": results, "searchLeads": leads})
	if !ok || !strings.Contains(text, "rich/console.py:20") {
		t.Fatalf("source anchors missing from rendered answer: %s", text)
	}
}

func TestSearchLeadsDoesNotPromoteOneGenericTextTerm(t *testing.T) {
	results := []map[string]any{
		{"query": "elif", "textHits": []map[string]any{
			{"file": "examples/aliases.py", "hits": []map[string]any{{"line": 10, "text": "elif x:"}}},
			{"file": "src/compat.py", "hits": []map[string]any{{"line": 20, "text": "elif y:"}}},
		}},
		{"query": "zsh_source", "textHits": []map[string]any{
			{"file": "docs/completion.md", "hits": []map[string]any{{"line": 30, "text": "zsh_source"}}},
		}},
	}
	if got := searchLeads(results); got != nil {
		t.Fatalf("unrelated text hits were promoted to source anchors: %#v", got)
	}
}

func TestSearchLeadsPointsAtCodeUseBeforeEarlierDocstring(t *testing.T) {
	results := []map[string]any{
		{"query": "proxy_mode", "textHits": []map[string]any{{"file": "src/proxy.py", "hits": []map[string]any{
			{"line": 12, "text": ":param proxy_mode: choose forwarding"},
			{"line": 48, "text": "if config.proxy_mode:"},
		}}}},
		{"query": "proxy_config", "textHits": []map[string]any{{"file": "src/proxy.py", "hits": []map[string]any{
			{"line": 47, "text": "proxy_config = build_config()"},
		}}}},
	}
	leads := searchLeads(results)
	if len(leads) != 1 || leads[0]["line"].(int) < 47 {
		t.Fatalf("code use should be the source anchor: %#v", leads)
	}
}
