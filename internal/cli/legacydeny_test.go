package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The live report this pins (2026-08-20): a laptop upgraded past the
// deny-era releases kept denying grep, because init never removed what an
// old init wrote. Non-interactive init must at least SAY the stale trio is
// there; and cleanup must never touch user-authored deny rules.

func writeSettings(t *testing.T, deny []any) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"permissions": map[string]any{"deny": deny}}
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func denyList(t *testing.T, p string) []any {
	t.Helper()
	b, _ := os.ReadFile(p)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		return nil
	}
	d, _ := perms["deny"].([]any)
	return d
}

func TestCleanupLegacyDeny_NonInteractiveWarnsButDoesNotEdit(t *testing.T) {
	p := writeSettings(t, []any{"Grep", "Bash(grep:*)", "Bash(rg:*)", "WebFetch"})
	cleanupLegacyDenyEntries(p) // test runs non-interactively: stderr warn only
	if got := denyList(t, p); len(got) != 4 {
		t.Fatalf("non-interactive cleanup must not edit the file, deny now %v", got)
	}
}

func TestCleanupLegacyDeny_NoPrismEntriesIsSilentNoop(t *testing.T) {
	p := writeSettings(t, []any{"WebFetch"})
	cleanupLegacyDenyEntries(p)
	if got := denyList(t, p); len(got) != 1 || got[0] != "WebFetch" {
		t.Fatalf("user-authored deny must be untouched, got %v", got)
	}
}

func TestCleanupLegacyDeny_MissingFileIsNoop(t *testing.T) {
	cleanupLegacyDenyEntries(filepath.Join(t.TempDir(), "nope", "settings.json"))
}
