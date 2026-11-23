package cli

import (
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/trust"
)

// The engine wrapper takes the user's trust decision through a hook so it
// stays an architecture leaf; every prism command, the MCP server
// included, runs through this package.
func init() {
	grove.Trust = grove.TrustPolicy{
		IsUntrusted: func(root string) bool { return trust.For(root).Untrusted },
		NoticeFor:   func(root string) string { return trust.NoticeOnce(root) },
	}
}
