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

// ChangeImpactResult is the deterministic change-set for a method signature
// change: the declaration(s), the override/implementation family in the
// subtype closure, super-declarations up the hierarchy, and every method
// with a resolved call edge into the set.
type ChangeImpactResult struct {
	Query        string         `json:"query"`
	Declarations []SymbolRecord `json:"declarations"`
	Supers       []SymbolRecord `json:"supers"`
	Family       []SymbolRecord `json:"family"`
	Callers      []SymbolRecord `json:"callers"`

	// DeclaringTypes: type declarations whose bodies contain a change-set
	// member signature that is not indexed as its own symbol (Go and TS
	// interface members) — the type's declaration block is itself a change
	// site and must be relayed as one. Empty for languages whose member
	// declarations are real symbols (Java, Python).
	DeclaringTypes []SymbolRecord `json:"declaringTypes,omitempty"`

	// ExternalSupers: supertype names declared in the hierarchy that resolve
	// to no indexed type (JDK / dependency types). Informational.
	ExternalSupers []string `json:"externalSupers,omitempty"`
	// OverridesExternal: "Type#method" when the queried method belongs to an
	// external supertype's contract — a signature change breaks a contract
	// the project does not own; the change-set is project-local only.
	OverridesExternal []string `json:"overridesExternal,omitempty"`
	// Completeness: "closed" or "project-local".
	Completeness string `json:"completeness,omitempty"`
}

// MissingImplementationsResult answers "which types claiming this contract do
// not implement Type.method" — the interface-evolution companion to
// ChangeImpactResult: what must change vs. who is broken once the member is
// required.
type MissingImplementationsResult struct {
	Query    string         `json:"query"`
	Contract []SymbolRecord `json:"contract"`

	// Missing: concrete closure types with no implementation, own or
	// inherited through the class-extends chain — compile errors once the
	// member is required.
	Missing []SymbolRecord `json:"missing"`
	// AbstractMissing: abstract classes without an implementation.
	// Informational — their concrete subtypes appear in Missing.
	AbstractMissing []SymbolRecord `json:"abstractMissing,omitempty"`
	// Unverifiable: no visible implementation, but the class-extends chain
	// leaves the index — an external base may provide the member.
	Unverifiable []SymbolRecord `json:"unverifiable,omitempty"`

	ImplementedCount int  `json:"implementedCount"`
	DefaultProvided  bool `json:"defaultProvided,omitempty"`

	ExternalSupers    []string `json:"externalSupers,omitempty"`
	OverridesExternal []string `json:"overridesExternal,omitempty"`
	Completeness      string   `json:"completeness,omitempty"`
}

// CoverageSite pairs a change-set site with the tests that reach it within
// the engine's bounded caller-hop horizon.
type CoverageSite struct {
	Symbol    SymbolRecord   `json:"symbol"`
	TestCount int            `json:"testCount"`
	Tests     []SymbolRecord `json:"tests"` // capped; testCount carries the truth
}

// UntestedSurfaceResult partitions a method's change-set by covering-test
// evidence: "before I change Type.method, what in its blast radius has no
// test pinning it?"
type UntestedSurfaceResult struct {
	Query      string         `json:"query"`
	Untested   []SymbolRecord `json:"untested"`
	Covered    []CoverageSite `json:"covered"`
	TotalSites int            `json:"totalSites"`

	ExternalSupers    []string `json:"externalSupers,omitempty"`
	OverridesExternal []string `json:"overridesExternal,omitempty"`
	Completeness      string   `json:"completeness,omitempty"`
}

// RenameEdit is one suggested line edit in a rename plan.
type RenameEdit struct {
	FilePath string `json:"filePath"`
	Line     int    `json:"line"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Site     string `json:"site"`
}

// RenamePlanResult converts a change-impact set into concrete line edits.
type RenamePlanResult struct {
	Query   string `json:"query"`
	NewName string `json:"newName"`

	Edits      []RenameEdit `json:"edits"`
	Ambiguous  []RenameEdit `json:"ambiguous,omitempty"`
	Unresolved []string     `json:"unresolved,omitempty"`

	SitesTotal        int      `json:"sitesTotal"`
	ExternalSupers    []string `json:"externalSupers,omitempty"`
	OverridesExternal []string `json:"overridesExternal,omitempty"`
	Completeness      string   `json:"completeness,omitempty"`
}

// DeadCodeResult reports production functions/methods nothing reaches.
// Precision-first; Caveats are part of the answer, not documentation.
type DeadCodeResult struct {
	RootCount            int            `json:"rootCount"`
	ReachableCount       int            `json:"reachableCount"`
	Considered           int            `json:"considered"`
	Dead                 []SymbolRecord `json:"dead"`
	ExportedUnreferenced []SymbolRecord `json:"exportedUnreferenced"`
	Caveats              []string       `json:"caveats"`
}
