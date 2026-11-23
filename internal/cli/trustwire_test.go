package cli

import (
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// SEC-01 after the arch fix: the CLI installs the trust policy the engine
// wrapper consults, so PRISM_UNTRUSTED still reaches the engine.
func TestTrustPolicyWired(t *testing.T) {
	if grove.Trust.IsUntrusted == nil || grove.Trust.NoticeFor == nil {
		t.Fatal("grove.Trust not installed")
	}
	root := t.TempDir()
	t.Setenv("PRISM_UNTRUSTED", "1")
	if !grove.Trust.Untrusted(root) {
		t.Fatal("PRISM_UNTRUSTED=1 did not make the root untrusted")
	}
	t.Setenv("PRISM_UNTRUSTED", "")
	if grove.Trust.Untrusted(root) {
		t.Fatal("root untrusted without PRISM_UNTRUSTED or a recorded decision")
	}
}
