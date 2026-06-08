// Package version exposes the prism build version. Single source of truth —
// consumed by the CLI, the MCP server, and the HTTP health endpoint.
package version

// Version is overridden via -ldflags at build time for local dev builds.
var Version = "dev"
