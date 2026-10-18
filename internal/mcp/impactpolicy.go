package mcp

// The one statement of when change_impact is required. The installed
// steering block (internal/cli) and both MCP instruction strings are built
// from these constants, so no surface can demand more or less than another.
//
// Found 2026-10-17 (Codex review of 86 paired runs): the steering block told
// agents to run change_impact before editing "any other symbol whose callers
// you have not enumerated", while the MCP instructions called it optional for
// a local body-only edit. Codex followed the blanket rule (eight impact calls
// on seven bug tasks) and Claude, reading the MCP text, made none on 41. The
// early trigger for contract changes is the part that was measured to work
// (impact before the first edit 0/4 -> 4/4 on rename tasks, cost flat on
// 2026-10-12) and is kept; the blanket obligation is what goes.
const (
	// ImpactRequiredFor lists the changes whose correctness depends on every
	// declaration and caller, so they start with an impact check.
	ImpactRequiredFor = "a rename, a signature change, or a change to an override or public contract"

	// ImpactOptionalFor names the edits that stay inside one body and can
	// proceed from the source already read.
	ImpactOptionalFor = "a local body-only edit"
)
