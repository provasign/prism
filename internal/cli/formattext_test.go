package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs f with stdout redirected and returns what it printed.
func capture(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The four task-shaped ops must actually honor --format text. They have no
// "content"/"symbols" key, so they used to fall through to printJSON —
// meaning --format text was a no-op on exactly the commands the Bash-only
// agent playbook tells agents to run that way.
func TestFormatTextRendersTaskShapedOps(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{{
		name: "change-impact",
		body: `{"query":"T.m","totalSites":2,"completeness":"closed",
			"declarations":[{"qualifiedName":"T.m","filePath":"a.go","line":10}],
			"callers":[{"qualifiedName":"C.call","filePath":"b.go","line":20,"via":"inner"}],
			"family":[],"supers":[]}`,
		want: []string{"change-impact: 2 site(s)", "completeness: closed",
			"declarations (1):", "T.m  a.go:10", "callers (1):", "C.call  b.go:20  (via inner)"},
	}, {
		name: "rename-plan",
		body: `{"query":"T.m","newName":"n","totalSites":1,
			"edits":[{"filePath":"a.go","line":10,"before":"func m()","after":"func n()"}]}`,
		want: []string{"T.m → n — rename-plan: 1 site(s)", "edits (1):", "a.go:10",
			"- func m()", "+ func n()"},
	}, {
		name: "missing-implementations",
		body: `{"query":"I.m","implementedCount":3,
			"contract":[{"qualifiedName":"I.m","filePath":"i.go","line":5}],
			"missing":[{"qualifiedName":"B","filePath":"b.go","line":7}]}`,
		want: []string{"missing-implementations (3 type(s) already implement)",
			"contract (1):", "I.m  i.go:5", "missing (1):", "B  b.go:7"},
	}, {
		name: "dead-code",
		body: `{"considered":10,"reachableCount":8,"rootCount":2,
			"dead":[{"qualifiedName":"unused","filePath":"x.go","line":3}],
			"exportedUnreferenced":[],"caveats":["reflection is invisible"]}`,
		want: []string{"10 considered, 8 reachable from 2 root(s)", "dead (1):",
			"unused  x.go:3", "caveats: reflection is invisible"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatal(err)
			}
			out := capture(t, func() { printOutput(m, formatText) })
			if strings.HasPrefix(strings.TrimSpace(out), "{") {
				t.Fatalf("--format text fell through to JSON:\n%s", out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
		})
	}
}

// Empty groups must not print an empty heading — a "family (0):" line reads
// as a section the caller should look at.
func TestFormatTextSkipsEmptyGroups(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"query":"T.m","totalSites":1,
		"declarations":[{"qualifiedName":"T.m","filePath":"a.go","line":1}],
		"family":[],"supers":[],"callers":[]}`), &m)
	out := capture(t, func() { printOutput(m, formatText) })
	for _, absent := range []string{"family", "supers", "callers"} {
		if strings.Contains(out, absent) {
			t.Errorf("empty group %q was printed:\n%s", absent, out)
		}
	}
}

// The unified task op must render as text too — its prepare payload is
// already markdown under "read", so JSON-encoding it (escaped newlines and
// all) is strictly worse than printing it.
func TestFormatTextRendersTaskOp(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"mode":"prepare","task":"do a thing",
		"read":{"content":"**Context**\n\nbody line\n"},
		"obligations":[{"qualifiedName":"T.m","file":"a.go","line":3,"siteCount":1,
			"completeness":"closed","sites":[{"symbol":"C.call","file":"b.go","line":9}]}],
		"next":"make the edits"}`), &m); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { printOutput(m, formatText) })
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("--format text fell through to JSON:\n%s", out)
	}
	for _, w := range []string{"do a thing — prepare", "**Context**", "body line",
		"obligations (1):", "T.m  a.go:3", "C.call  b.go:9", "next: make the edits"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

// A failed lookup used to render as {"symbol": null}, which tells the caller
// neither why nor where to go next.
func TestFormatTextRendersUnmatchedLookup(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"symbol":null,"name":"Neighbours","matched":false,
		"note":"no symbol named \"Neighbours\" in the index — did you mean one of the candidates?",
		"candidates":["CodeGraph.Neighbors (graph.go:393)"]}`), &m)
	out := capture(t, func() { printOutput(m, formatText) })
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("unmatched lookup rendered as JSON:\n%s", out)
	}
	for _, w := range []string{"did you mean", "CodeGraph.Neighbors (graph.go:393)"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}
