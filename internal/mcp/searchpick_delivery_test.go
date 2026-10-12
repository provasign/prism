package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// A small-result pick the default budget rejects (a body over
// searchBudgetBodyLines) is never sent, yet rendering it recorded its whole
// file section as delivered: a later read of the file answered
// "[prism:cached] sample.py lines 1-71" for source the agent never saw.
func TestRejectedSearchPickIsNotRecordedAsDelivered(t *testing.T) {
	var b strings.Builder
	b.WriteString("def big():\n")
	for i := 0; i < 69; i++ {
		fmt.Fprintf(&b, "    x_%d = %d\n", i, i)
	}
	b.WriteString("    return 0\n")
	srv := compactFixture(t, map[string]string{"sample.py": b.String()})
	search := callCompact(t, srv, "search", map[string]any{"terms": []string{"big"}})
	if strings.Contains(search, "x_60 = 60") {
		t.Skipf("fixture: the whole body was delivered by search, nothing to test:\n%s", search)
	}
	read := callCompact(t, srv, "read", map[string]any{"path": "sample.py"})
	if strings.Contains(read, "[prism:cached]") || !strings.Contains(read, "x_60 = 60") {
		t.Fatalf("read answered from a delivery that never happened:\nsearch:\n%s\nread:\n%s", search, read)
	}
}
