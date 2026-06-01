package ranking

import "testing"

func TestSelectProfile(t *testing.T) {
	// Known profile returns itself.
	if got := SelectProfile("default"); got.Name != "default" {
		t.Errorf("SelectProfile(default).Name = %q", got.Name)
	}
	// Unknown profile falls back to default.
	if got := SelectProfile("does-not-exist"); got.Name != "default" {
		t.Errorf("SelectProfile(unknown) should fall back to default, got %q", got.Name)
	}
}
