// Package version exposes the prism build version. Single source of truth —
// consumed by the CLI, the MCP server, and the HTTP health endpoint.
package version

// Version is overridden via -ldflags at build time. Keep in sync with
// vscode-extension/package.json for local dev builds.
var Version = "dev"
