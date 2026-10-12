package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An edit is indexed from the parse first and grove runs the compiler pass
// in the background (grove Config.DeferNative). A graph answer computed
// before that pass lands says so and claims no completeness; once it lands
// the same question is compiler-backed again.
func TestImpactAfterEditIsProvisionalUntilTheCompilerPassLands(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("no javac on PATH")
	}
	srv := compactFixture(t, map[string]string{
		"p/Adapter.java": "package p;\n\npublic abstract class Adapter {\n  public abstract void write(Object v);\n}\n",
		"p/Plain.java":   "package p;\n\npublic class Plain extends Adapter {\n  @Override\n  public void write(Object v) {}\n}\n",
	})
	first := callCompact(t, srv, "change_impact", map[string]any{"name": "Adapter.write"})
	if strings.Contains(first, "PROVISIONAL") {
		t.Fatalf("a cold index is compiler-backed:\n%s", first)
	}

	t.Setenv("PRISM_COMPILER_WAIT", "0s")
	root := srv.handler.Root
	if err := os.WriteFile(filepath.Join(root, "p", "Registry.java"), []byte(
		"package p;\n\npublic class Registry {\n  public static final Adapter ANON =\n      new Adapter() {\n        @Override\n        public void write(Object v) {}\n      };\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	during := callCompact(t, srv, "change_impact", map[string]any{"name": "Adapter.write"})
	// The pass may already have finished on a fast machine; when it has
	// not, the answer must say so and must not claim completeness.
	if strings.Contains(during, "PROVISIONAL") &&
		(!strings.Contains(during, "completeness: provisional") || strings.Contains(during, "safeToClaimComplete: true")) {
		t.Fatalf("provisional answer still claims completeness:\n%s", during)
	}
	t.Logf("answer during the compiler pass was provisional: %v", strings.Contains(during, "PROVISIONAL"))

	t.Setenv("PRISM_COMPILER_WAIT", "2m")
	if !srv.handler.Grove.WaitNative(t.Context(), 2*time.Minute) {
		t.Fatalf("compiler pass did not drain: %v", srv.handler.Grove.NativePending())
	}
	after := callCompact(t, srv, "change_impact", map[string]any{"name": "Adapter.write"})
	if strings.Contains(after, "PROVISIONAL") || !strings.Contains(after, "p/Registry.java") {
		t.Fatalf("after the compiler pass the answer must be compiler-backed and include the new override:\n%s", after)
	}
}

// The mark itself: completeness fields are downgraded only for a
// provisional note, and only where the answer has them.
func TestAttachStaleWarningDowngradesOnlyProvisional(t *testing.T) {
	m := map[string]any{"completeness": "closed", "familyCompleteness": "closed", "safeToClaimComplete": true}
	attachStaleWarning(m, provisionalPrefix+"pass running")
	if m["completeness"] != "provisional" || m["familyCompleteness"] != "provisional" || m["safeToClaimComplete"] != false {
		t.Fatalf("provisional note did not downgrade: %v", m)
	}
	n := map[string]any{"completeness": "closed", "safeToClaimComplete": true}
	attachStaleWarning(n, "index refresh failed (x)")
	if n["completeness"] != "closed" || n["safeToClaimComplete"] != true {
		t.Fatalf("a stale note must not rewrite completeness fields: %v", n)
	}
	o := map[string]any{"symbols": []any{}}
	attachStaleWarning(o, provisionalPrefix+"pass running")
	if _, has := o["completeness"]; has {
		t.Fatalf("no completeness field may be invented: %v", o)
	}
}

// A provisional answer repeated verbatim is sent again in full, never as a
// pointer to the earlier one: the mark must stay visible on every answer
// computed before the compiler pass lands.
func TestProvisionalAnswerIsNeverDeduplicated(t *testing.T) {
	srv := compactFixture(t, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	h := srv.handler
	big := map[string]any{"completeness": "closed", "staleWarning": provisionalPrefix + "pass running",
		"family": strings.Repeat("site ", 4000)}
	args := map[string]any{"query": "A"}
	first := h.graphDedupe("prism_change_impact", args, big)
	second := h.graphDedupe("prism_change_impact", args, big)
	if m, ok := second.(map[string]any); !ok || m["staleWarning"] == nil || first == nil {
		t.Fatalf("a provisional answer was replaced by a pointer: %#v", second)
	}
}
