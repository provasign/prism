package grove

import groveeng "github.com/provasign/grove/pkg/grove"

// Capabilities returns Grove's release-level per-language and per-operation
// quality manifest. `grove doctor` reports it directly; `prism doctor` did
// not until this (2026-09-22 handoff gap: a user asking "does prism support
// X" had no answer from `prism doctor`, only from a separate `grove doctor`
// run).
func Capabilities() groveeng.CapabilityManifest {
	return groveeng.CurrentCapabilities()
}
