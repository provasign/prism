// Package version exposes the prism build version. Single source of truth —
// consumed by the CLI, the MCP server, and the HTTP health endpoint.
package version

// Version is the current prism release. Keep in sync with
// vscode-extension/package.json.
const Version = "0.2.0"
