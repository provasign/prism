package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
)

// newDriftHandler builds a handler over a real embedded Grove engine in a
// temp repo and simulates a context delivery for auth.go.
func newDriftHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	authContent := `package main

func Login(user string) error {
	return validate(user)
}

func Logout() {
	clear()
}
`
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(authContent), 0o644); err != nil {
		t.Fatal(err)
	}
	client := grove.NewClient("", "").WithTokenFromDir(root)
	ctx := context.Background()
	if err := client.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)
	if _, err := client.Index(ctx, root); err != nil {
		t.Fatal(err)
	}

	h := NewHandlerWithLedger(config.Default(), root, client, session.NewLedger("drift-test"))
	h.Session.Reset() // discard any warm-loaded cache; this test owns the working set

	// Simulate delivery: record file + symbol SHAs the way the read path does.
	h.Session.Record("auth.go", compression.Hash(authContent), 100, "full")
	syms, err := client.FileSymbols(ctx, "auth.go")
	if err != nil || len(syms) == 0 {
		t.Fatalf("file symbols: %v (%d)", err, len(syms))
	}
	shas := map[string]string{}
	for _, s := range syms {
		shas[s.Name] = compression.Hash(s.RawText)
	}
	h.Session.UpdateSymbolSHAs("auth.go", shas)
	return h, root
}

func TestDriftFreshWorkingSetIsQuiet(t *testing.T) {
	h, _ := newDriftHandler(t)
	if w := h.StaleContextWarning(); w != "" {
		t.Fatalf("fresh working set warned: %q", w)
	}
	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.ChangedFiles != 0 || report.CheckedFiles != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestDriftDetectsSymbolLevelChanges(t *testing.T) {
	h, root := newDriftHandler(t)

	// Login's body changes, Logout is removed, Refresh appears.
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(`package main

func Login(user, password string) error {
	return validate(user, password)
}

func Refresh() {
	renew()
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if w := h.StaleContextWarning(); !strings.Contains(w, "auth.go") || !strings.Contains(w, "stale context") {
		t.Fatalf("warning = %q", w)
	}

	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.ChangedFiles != 1 || len(report.Files) != 1 {
		t.Fatalf("report = %+v", report)
	}
	file := report.Files[0]
	if file.File != "auth.go" || file.Status != "changed" || file.Origin != "edit" {
		t.Fatalf("file drift = %+v", file)
	}
	byChange := map[string][]string{}
	for _, s := range file.Symbols {
		byChange[s.Change] = append(byChange[s.Change], s.Name)
	}
	if len(byChange["changed"]) != 1 || byChange["changed"][0] != "Login" {
		t.Fatalf("changed = %+v", byChange)
	}
	if len(byChange["removed"]) != 1 || byChange["removed"][0] != "Logout" {
		t.Fatalf("removed = %+v", byChange)
	}
	if len(byChange["added"]) != 1 || byChange["added"][0] != "Refresh" {
		t.Fatalf("added = %+v", byChange)
	}
	for _, s := range file.Symbols {
		if s.Change == "changed" && !strings.Contains(s.NewSignature, "password") {
			t.Fatalf("changed symbol missing new signature: %+v", s)
		}
	}
	if !strings.Contains(report.Warning, "ground shifted") {
		t.Fatalf("warning = %q", report.Warning)
	}
}

func TestDriftMergeProvenanceFromFuseRecords(t *testing.T) {
	h, root := newDriftHandler(t)

	// A Fuse merge record for auth.go marks the drift origin as "merge"
	// and carries old→new signatures.
	fuseDir := filepath.Join(root, ".git", "fuse")
	if err := os.MkdirAll(fuseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `[{"timestamp":"2026-06-12T00:00:00Z","file":"auth.go","strategy":"symbol",
		"drift":{"changed":[{"qualifiedName":"Login","change":"signature",
		"oldSignature":"func Login(user string) error",
		"newSignature":"func Login(user, password string) error"}]}}]`
	if err := os.WriteFile(filepath.Join(fuseDir, "drift.json"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(`package main

func Login(user, password string) error {
	return validate(user, password)
}

func Logout() {
	clear()
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.ChangedFiles != 1 {
		t.Fatalf("report = %+v", report)
	}
	file := report.Files[0]
	if file.Origin != "merge" {
		t.Fatalf("origin = %q, want merge", file.Origin)
	}
	if len(file.MergeDetails) != 1 || file.MergeDetails[0].OldSignature == "" {
		t.Fatalf("merge details = %+v", file.MergeDetails)
	}
	if !strings.Contains(report.Warning, "via merge") {
		t.Fatalf("warning = %q", report.Warning)
	}
}

func TestDriftDeletedFile(t *testing.T) {
	h, root := newDriftHandler(t)
	if err := os.Remove(filepath.Join(root, "auth.go")); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_drift", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := out.(DriftReport)
	if report.ChangedFiles != 1 || report.Files[0].Status != "deleted" {
		t.Fatalf("report = %+v", report)
	}
}

func TestStaleContextWarningInjectedIntoToolResponses(t *testing.T) {
	if !contextBearingTool("prism_read") || !contextBearingTool("prism_query") {
		t.Fatal("read/query must be context-bearing")
	}
	if contextBearingTool("prism_savings") || contextBearingTool("prism_drift") {
		t.Fatal("savings/drift must not be annotated")
	}
}
