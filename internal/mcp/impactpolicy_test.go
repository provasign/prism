package mcp

import (
	"strings"
	"testing"
)

// Every instruction surface states the same impact rule: required for the
// triggers in ImpactRequiredFor, optional for ImpactOptionalFor, and no
// blanket obligation (2026-10-17: steering demanded impact for any symbol
// whose callers were not enumerated while these strings made it optional).
func TestImpactPolicyIsStatedTheSameWayInBothInstructionStrings(t *testing.T) {
	for name, text := range map[string]string{"full": serverInstructions, "compact": compactServerInstructions} {
		if !strings.Contains(text, ImpactRequiredFor) {
			t.Errorf("%s instructions omit the impact triggers %q:\n%s", name, ImpactRequiredFor, text)
		}
		if !strings.Contains(text, "optional for "+ImpactOptionalFor) {
			t.Errorf("%s instructions omit %q:\n%s", name, "optional for "+ImpactOptionalFor, text)
		}
		for _, blanket := range []string{"whose callers you have not enumerated", "Before editing a symbol", "before editing every symbol"} {
			if strings.Contains(text, blanket) {
				t.Errorf("%s instructions carry a blanket impact obligation %q", name, blanket)
			}
		}
	}
	if len(serverInstructions) > 2000 {
		t.Errorf("server instructions exceed the 2 KB limit: %d", len(serverInstructions))
	}
}
