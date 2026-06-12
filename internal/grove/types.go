// Package grove holds typed mirrors of Grove's result shapes used by the
// Prism client. They are plain structs sharing Grove's JSON wire tags so the
// rest of Prism (ranking, MCP, CLI) never touches engine types directly, and
// records can round-trip back into the engine (see toEngineSymbol).
package grove

// SymbolRecord mirrors grove/internal/core.SymbolRecord.
type SymbolRecord struct {
	ID             string     `json:"id"`
	FilePath       string     `json:"filePath"`
	BlobSha        string     `json:"blobSha"`
	Language       string     `json:"language"`
	Kind           string     `json:"kind"`
	Name           string     `json:"name"`
	QualifiedName  string     `json:"qualifiedName"`
	Signature      string     `json:"signature"`
	Docstring      string     `json:"docstring,omitempty"`
	Span           SpanInfo   `json:"span"`
	ParentSymbol   string     `json:"parentSymbol,omitempty"`
	Imports        []string   `json:"imports,omitempty"`
	Exports        bool       `json:"exports"`
	RawText        string     `json:"rawText,omitempty"`
	Modifiers      []string   `json:"modifiers,omitempty"`
	TypeParameters []string   `json:"typeParameters,omitempty"`
	Annotations    []string   `json:"annotations,omitempty"`
	CallSites      []CallSite `json:"callSites,omitempty"`
}

// SpanInfo is the start/end line range.
type SpanInfo struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CallSite mirrors grove/internal/core.CallSite.
type CallSite struct {
	Callee string `json:"callee"`
	Line   int    `json:"line"`
}

// Edge mirrors grove/internal/core.Edge.
type Edge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// StatusResult mirrors Grove's /status response.
type StatusResult struct {
	FilesIndexed int `json:"filesIndexed"`
	SymbolCount  int `json:"symbolCount"`
	EdgeCount    int `json:"edgeCount"`
}

// IndexResult mirrors Grove's /index response.
type IndexResult struct {
	Root         string `json:"root"`
	FilesSeen    int    `json:"filesSeen"`
	FilesUpdated int    `json:"filesUpdated"`
	FilesSkipped int    `json:"filesSkipped"`
	FilesPruned  int    `json:"filesPruned"`
	SymbolCount  int    `json:"symbolCount"`
	EdgeCount    int    `json:"edgeCount"`
}

// ImpactNode is one entry returned by Grove's /impact endpoint.
type ImpactNode = SymbolRecord

// SemanticResult mirrors Grove's /semantic response entry.
type SemanticResult struct {
	Score  float64      `json:"score"`
	Symbol SymbolRecord `json:"symbol"`
}

// SymbolChange mirrors grove's core.SymbolChange: one symbol's before/after
// pair in a structural diff.
type SymbolChange struct {
	Before           *SymbolRecord `json:"before,omitempty"`
	After            *SymbolRecord `json:"after,omitempty"`
	SignatureChanged bool          `json:"signatureChanged"`
	BodyChanged      bool          `json:"bodyChanged"`
}

// FileGraphDiff mirrors grove's core.GraphDiff scoped to one file: the
// structural delta between a delivered symbol snapshot and the current
// index, with renames paired and breaking changes classified.
type FileGraphDiff struct {
	Added    []SymbolRecord `json:"added"`
	Removed  []SymbolRecord `json:"removed"`
	Changed  []SymbolChange `json:"changed"`
	Renamed  []SymbolChange `json:"renamed,omitempty"`
	Breaking []SymbolChange `json:"breakingChanges"`
}
