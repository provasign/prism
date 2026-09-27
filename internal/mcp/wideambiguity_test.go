package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// grafana-querydata-impact transcript (2026-09-06): "QueryData" was ambiguous
// across 41 candidates; the agent read grove's bare "re-run with one of
// these", issued one change_impact call per guessed receiver, and still
// missed 6 of 51 required sites reachable only by text search (an external
// interface with no local declaration to anchor a family query on). Past
// wideMemberAmbiguityThreshold candidates, Prism must synthesize one family
// result instead of inviting per-candidate looping.
func TestChangeImpact_WideAmbiguityReturnsOneInferredFamily(t *testing.T) {
	files := map[string]string{"go.mod": "module example.com/wide\n\ngo 1.26\n"}
	for i := 0; i < wideImpactIdentityThreshold; i++ {
		files[fmt.Sprintf("impl%02d.go", i)] = fmt.Sprintf(
			"package wide\ntype T%02d struct{}\nfunc (t *T%02d) QueryData() int { return %d }\n", i, i, i)
	}
	h := evidenceHandler(t, files)
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "QueryData"})
	if err != nil {
		t.Fatalf("wide family must resolve in one call: %v", err)
	}
	m := out.(map[string]any)
	family := anySlice(m["family"])
	if got := len(family); got != wideImpactIdentityThreshold {
		t.Fatalf("family has %d sites, want %d: %v", got, wideImpactIdentityThreshold, m)
	}
	if m["completeness"] != "project-local" {
		t.Fatalf("inferred external family must be project-local: %v", m)
	}
	note, _ := m["methodFamilyNote"].(string)
	if !strings.Contains(note, "inferred one external-interface method family") {
		t.Fatalf("inference must be explicit: %v", m)
	}
	if _, has := family[0].(map[string]any)["signature"]; has {
		t.Fatalf("wide resolved family repeated signatures instead of compact identities: %v", family[0])
	}
	if evidence, _ := m["evidenceNote"].(string); !strings.Contains(evidence, "compact file:line identities") {
		t.Fatalf("wide impact must explain adaptive evidence delivery: %v", m)
	}
}

// A handful of candidates is cheap to query one at a time; the redirect
// must not fire and drown out the useful candidate list grove already built.
func TestChangeImpact_FewCandidatesKeepsOriginalError(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"go.mod": "module example.com/narrow\n\ngo 1.26\n",
		"a.go":   "package narrow\ntype A struct{}\nfunc (a *A) Get() int { return 1 }\n",
		"b.go":   "package narrow\ntype B struct{}\nfunc (b *B) Get() int { return 2 }\n",
	})
	_, err := h.Invoke("prism_change_impact", map[string]any{"query": "Get"})
	if err == nil {
		t.Fatal("expected ambiguity for two same-named methods")
	}
	if strings.Contains(err.Error(), "WIDE MEMBER") {
		t.Fatalf("two candidates is cheap to query directly; must not redirect: %s", err)
	}
}

func TestChangeImpact_ExternalQualifiedSignatureFiltersDecoys(t *testing.T) {
	files := map[string]string{"go.mod": "module example.com/external\n\ngo 1.26\n"}
	for i := 0; i < wideMemberAmbiguityThreshold; i++ {
		files[fmt.Sprintf("impl%02d.go", i)] = fmt.Sprintf(
			"package external\ntype T%02d struct{}\nfunc (t *T%02d) QueryData(ctx string, req int) error { return nil }\n", i, i)
	}
	files["decoy.go"] = "package external\ntype Decoy struct{}\nfunc (d *Decoy) QueryData(req int) error { return nil }\n"
	h := evidenceHandler(t, files)
	for _, args := range []map[string]any{
		{"query": "backend.QueryDataHandler.QueryData"},
		{"query": "backend.QueryDataHandler.QueryData", "signature": "QueryData(ctx string, req int) error"},
	} {
		out, err := h.Invoke("prism_change_impact", args)
		if err != nil {
			t.Fatalf("external method query failed: %v", err)
		}
		family := anySlice(out.(map[string]any)["family"])
		if len(family) != wideMemberAmbiguityThreshold {
			t.Fatalf("signature family has %d sites, want %d: %v", len(family), wideMemberAmbiguityThreshold, family)
		}
		for _, raw := range family {
			if strings.Contains(fmt.Sprint(raw), "Decoy") {
				t.Fatalf("incompatible same-name decoy entered external family: %v", raw)
			}
		}
	}
}

func TestEnrichAmbiguousImpactError_MalformedMessageFallsBackToNil(t *testing.T) {
	orig := fmt.Errorf(`change-impact: "X" is ambiguous — not-a-number candidates: a, b`)
	if got := enrichAmbiguousImpactError("X", orig); got != nil {
		t.Fatalf("unparseable candidate count must not synthesize a threshold decision: %v", got)
	}
}

// gin wide bed (2026-09-27): change_impact(name="Name", file=binding.go,
// signature="Name() string") took the external-family guess and returned
// Binding.Name AND BindingUri.Name (two local interfaces) as one family. A
// member the project declares in its own interfaces is a local contract.
func ginLikeFixture() map[string]string {
	files := map[string]string{
		"go.mod": "module example.com/gin\n\ngo 1.26\n",
		"binding.go": "package gin\n\ntype Binding interface {\n\tName() string\n\tBind(v any) error\n}\n\n" +
			"type BindingUri interface {\n\tName() string\n\tBindUri(v any) error\n}\n",
		"uri.go": "package gin\ntype uriBinding struct{}\nfunc (uriBinding) Name() string { return \"uri\" }\nfunc (uriBinding) BindUri(v any) error { return nil }\n",
	}
	for i := 0; i < wideMemberAmbiguityThreshold; i++ {
		files[fmt.Sprintf("impl%02d.go", i)] = fmt.Sprintf(
			"package gin\ntype b%02d struct{}\nfunc (b%02d) Name() string { return \"%d\" }\nfunc (b%02d) Bind(v any) error { return nil }\n", i, i, i, i)
	}
	return files
}

func TestChangeImpact_FileScopedSignatureDoesNotMergeLocalInterfaces(t *testing.T) {
	h := evidenceHandler(t, ginLikeFixture())
	_, err := h.Invoke("prism_change_impact", map[string]any{
		"query": "Name", "file": "binding.go", "signature": "Name() string"})
	if err == nil {
		t.Fatal("two local interfaces declare Name() string in binding.go; the answer must ask which")
	}
	for _, want := range []string{"Binding.Name", "BindingUri.Name"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity must name %s: %s", want, err)
		}
	}
}

func TestChangeImpact_WideBareLocalContractNamesOwners(t *testing.T) {
	h := evidenceHandler(t, ginLikeFixture())
	_, err := h.Invoke("prism_change_impact", map[string]any{"query": "Name"})
	if err == nil || !strings.Contains(err.Error(), "LOCAL CONTRACT") {
		t.Fatalf("wide bare name declared by local interfaces must name the owners, got %v", err)
	}
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "Binding.Name"})
	if err != nil {
		t.Fatalf("owner-qualified retry failed: %v", err)
	}
	for _, raw := range anySlice(out.(map[string]any)["family"]) {
		if s := fmt.Sprint(raw); strings.Contains(s, "uriBinding") || strings.Contains(s, "BindingUri") {
			t.Fatalf("Binding.Name family contains a BindingUri look-alike: %v", raw)
		}
	}
}
