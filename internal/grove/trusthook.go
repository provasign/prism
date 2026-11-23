package grove

// TrustPolicy supplies the user's trust decision for a root (SEC-01). The
// engine wrapper is an architecture leaf (prism.yaml arch_deny), so it does
// not import internal/trust; internal/cli installs the real policy.
type TrustPolicy struct {
	// IsUntrusted reports that root's build tools must not run.
	IsUntrusted func(root string) bool
	// NoticeFor returns a one-time notice to print before indexing root, or "".
	NoticeFor func(root string) string
}

// Trust is the policy in force. The zero value trusts every root and
// prints nothing.
var Trust TrustPolicy

// Untrusted reports whether root's build tools must not run.
func (p TrustPolicy) Untrusted(root string) bool {
	return p.IsUntrusted != nil && p.IsUntrusted(root)
}

// Notice returns the one-time notice for root, or "".
func (p TrustPolicy) Notice(root string) string {
	if p.NoticeFor == nil {
		return ""
	}
	return p.NoticeFor(root)
}
