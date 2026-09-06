package mcp

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestHitRollupStableTiesAndHonestSummary(t *testing.T) {
	h := symbolCapFixture(t, 40)
	var want []map[string]any
	for repeat := 0; repeat < 12; repeat++ {
		out, err := h.Invoke("prism_search", map[string]any{"query": "FooThing", "scope": "text", "limit": 1})
		if err != nil {
			t.Fatal(err)
		}
		m := out.(map[string]any)
		got := m["hitRollup"].([]map[string]any)
		if repeat == 0 {
			want = got
		} else if !reflect.DeepEqual(got, want) {
			t.Fatalf("identical search changed capped groups on repeat %d", repeat)
		}
		if len(got) != rollupSymbolCap+1 {
			t.Fatalf("omitted groups must be counted: %v", got)
		}
		for i := 0; i < rollupSymbolCap; i++ {
			if got[i]["symbol"] != fmt.Sprintf("FooThing%02d", i) {
				t.Fatalf("ties should use file/line order: %v", got[i])
			}
		}
		if got[rollupSymbolCap]["note"] != "+30 more symbols with 30 hit(s)" {
			t.Fatal(got)
		}
		text, ok := renderSearchAsText(m)
		if !ok {
			t.Fatal("unexpected render fallback")
		}
		text = h.once.apply(text)
		if !strings.Contains(text, "bounded") || !strings.Contains(text, "+30 more symbols") || strings.Contains(text, "COMPLETE breakdown") || strings.Contains(text, "ALL matches") || strings.Contains(text, "complete breakdown") {
			t.Fatalf("repeat %d overstated or lost coverage limits: %s", repeat, text)
		}
	}
}

func TestHitRollupBoundsScanAndKeepsPartialScope(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"big.go": "package p\nfunc Big() {\n" + strings.Repeat("println(\"Needle\")\n", rollupMaxHits+5) + "}\n",
	})
	for _, paths := range [][]string{nil, {".", "../elsewhere"}} {
		got := h.hitRollup(t.Context(), "Needle", searchScope{paths: paths}, false)
		if len(got) != 2 || got[0]["hits"] != rollupMaxHits {
			t.Fatalf("aggregation scan must honor the hit cap: %v", got)
		}
		if !strings.Contains(got[1]["note"].(string), "INCOMPLETE rollup scan") {
			t.Fatalf("bounded count claimed completeness: %v", got)
		}
	}
	got := h.hitRollup(t.Context(), "func Big", searchScope{paths: []string{".", "../elsewhere"}}, false)
	if len(got) != 2 || !strings.Contains(got[1]["note"].(string), "rejected scope") {
		t.Fatalf("partial scope must survive even when the hit cap is not reached: %v", got)
	}
}
