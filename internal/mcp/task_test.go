package mcp

// Tests for the unified `prism` task tool: the prepare -> edit -> verify
// loop on a real git fixture (reusing the verify fixture: S.Do with six
// callers across two packages).

import (
	"fmt"
	"testing"
)

func obligationsOf(t *testing.T, out any) []map[string]any {
	t.Helper()
	m := out.(map[string]any)
	obs, ok := m["obligations"].([]taskObligation)
	if !ok {
		t.Fatalf("obligations missing or wrong type: %T", m["obligations"])
	}
	var got []map[string]any
	for _, ob := range obs {
		got = append(got, map[string]any{
			"symbol": ob.Symbol, "completeness": ob.Completeness,
			"siteCount": ob.SiteCount,
		})
	}
	return got
}

// Prepare must discover S.Do from a natural task description, record its six
// call sites as an obligation, and persist the package for verify.
func TestToolTask_PrepareRecordsObligations(t *testing.T) {
	h, _, _ := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}

	out, err := h.Invoke("prism", map[string]any{
		"task": "change how S.Do processes its input string",
		// The fixture is tiny and synthetic; seed with the anchor the way an
		// agent that grepped would. Discovery quality is prism_query's
		// already-tested surface — this test pins the obligation pipeline.
		"terms": []any{"Do"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["mode"] != "prepare" {
		t.Fatalf("mode = %v, want prepare", m["mode"])
	}
	if m["read"] == nil {
		t.Fatal("prepare returned no source delivery")
	}
	obs := obligationsOf(t, out)
	var doOb map[string]any
	for _, ob := range obs {
		if ob["symbol"] == "Do" {
			doOb = ob
		}
	}
	if doOb == nil {
		t.Fatalf("no obligation recorded for Do; got %v", obs)
	}
	if doOb["siteCount"].(int) != 6 {
		t.Errorf("Do obligation siteCount = %v, want 6 callers", doOb["siteCount"])
	}

	pkg := h.loadTaskPackage()
	if pkg == nil {
		t.Fatal("task package not persisted")
	}
	if pkg.Task != "change how S.Do processes its input string" {
		t.Errorf("persisted task = %q", pkg.Task)
	}
}

// The full loop: prepare, an incomplete edit (3 of 6 callers), then verify
// via the SAME tool with changed_files — must be verdict incomplete with the
// three missed sites, and unaddressed prepare-time obligations reported.
func TestToolTask_PrepareEditVerifyLoop(t *testing.T) {
	h, dir, sites := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	task := "add a count parameter to S.Do"
	if _, err := h.Invoke("prism", map[string]any{"task": task, "terms": []any{"Do"}}); err != nil {
		t.Fatal(err)
	}

	// The "agent" changes the signature and fixes callers 0,1,3 only.
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 3: true})

	out, err := h.Invoke("prism", map[string]any{
		"task":          task,
		"changed_files": []any{"core/core.go", "use1/a.go", "use2/b.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["mode"] != "verify" {
		t.Fatalf("mode = %v, want verify (changed_files present)", m["mode"])
	}
	if m["verdict"] != "incomplete" {
		t.Fatalf("verdict = %v, want incomplete; missed=%v", m["verdict"], m["missedSites"])
	}
	got := missedSet(t, out)
	for _, id := range []int{2, 4, 5} {
		key := fmt.Sprintf("%s:%d", sites[id].file, sites[id].line)
		if !got[key] {
			t.Errorf("missed site %s not reported; got %v", key, got)
		}
	}
	// Obligation comparison ran against the stored package.
	if _, ok := m["obligationsSatisfied"]; !ok {
		t.Error("obligationsSatisfied missing — stored package not compared")
	}
}

// A complete edit must come back complete, with no unaddressed obligations
// (all obligation files touched).
func TestToolTask_CompleteEditPasses(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	task := "add a count parameter to S.Do"
	if _, err := h.Invoke("prism", map[string]any{"task": task, "terms": []any{"Do"}}); err != nil {
		t.Fatal(err)
	}
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true})

	out, err := h.Invoke("prism", map[string]any{"task": task, "mode": "verify"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "complete" {
		t.Fatalf("verdict = %v, want complete; missed=%v", m["verdict"], m["missedSites"])
	}
	if ua, ok := m["unaddressedObligations"]; ok {
		t.Errorf("unexpected unaddressed obligations on a complete change: %v", ua)
	}
}

// A verify call whose task differs from the stored package must skip the
// obligation comparison with a note, not compare against the wrong task.
func TestToolTask_TaskMismatchSkipsObligations(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke("prism", map[string]any{"task": "task A", "terms": []any{"Do"}}); err != nil {
		t.Fatal(err)
	}
	applyAgentChange(t, h, dir, map[int]bool{0: true})
	out, err := h.Invoke("prism", map[string]any{"task": "task B", "mode": "verify"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, ok := m["obligationsSatisfied"]; ok {
		t.Error("obligation comparison ran despite task mismatch")
	}
	if m["obligationsNote"] == nil {
		t.Error("task mismatch not surfaced as a note")
	}
}

// Mode resolution: no changed_files and no stored dirty state -> prepare;
// explicit mode wins; bad mode is rejected loudly.
func TestToolTask_ModeResolution(t *testing.T) {
	h, _, _ := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism", map[string]any{"task": "look at S.Do", "terms": []any{"Do"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["mode"] != "prepare" {
		t.Errorf("default mode = %v, want prepare", out.(map[string]any)["mode"])
	}
	if _, err := h.Invoke("prism", map[string]any{"task": "x", "mode": "audit"}); err == nil {
		t.Error("invalid mode accepted")
	}
	if _, err := h.Invoke("prism", map[string]any{}); err == nil {
		t.Error("missing task accepted")
	}
}
