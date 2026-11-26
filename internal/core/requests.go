package core

// Typed requests (ENG-01). Callers in this repository (the CLI, the graph
// explorer, prism ci) build one of these instead of a map[string]any, so a
// misspelled field fails to compile instead of being ignored at run time.
// The MCP and HTTP transports still decode their JSON arguments into the
// same maps; Args returns exactly what those transports would send, with the
// same Go types the CLI used to put there, so a typed call and a transport
// call reach the tool identically.

// Request is one tool call.
type Request interface {
	Tool() string
	Args() map[string]any
}

// Do runs a typed request.
func (h *Handler) Do(r Request) (any, error) { return h.Invoke(r.Tool(), r.Args()) }

// set adds key when v is not its zero value.
func set(m map[string]any, key string, v any) {
	switch x := v.(type) {
	case string:
		if x == "" {
			return
		}
	case int:
		if x == 0 {
			return
		}
	case bool:
		if !x {
			return
		}
	case []string:
		if len(x) == 0 {
			return
		}
	case []any:
		if len(x) == 0 {
			return
		}
	case nil:
		return
	}
	m[key] = v
}

func anyList(xs []string) []any {
	if len(xs) == 0 {
		return nil
	}
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// ReadRequest reads one file or a line window of it.
type ReadRequest struct {
	File             string
	Offset, Limit    int // 1-based first line and line count; 0 = unset
	IncludeSensitive bool
}

func (ReadRequest) Tool() string { return "prism_read" }
func (r ReadRequest) Args() map[string]any {
	m := map[string]any{"file": r.File}
	set(m, "include_sensitive", r.IncludeSensitive)
	set(m, "offset", r.Offset)
	set(m, "limit", r.Limit)
	return m
}

// CompactReadRequest is a read through the compact gateway's window rules
// (at most CompactReadLimit lines).
type CompactReadRequest struct {
	File             string
	From, To         int // 0 = unset
	IncludeSensitive bool
}

func (CompactReadRequest) Tool() string { return "prism_read" }
func (r CompactReadRequest) Args() map[string]any {
	m := map[string]any{"file": r.File}
	set(m, "include_sensitive", r.IncludeSensitive)
	set(m, "from", r.From)
	set(m, "to", r.To)
	return m
}

// CompactReadFilesRequest reads several files from their first line, one
// compact window each (the gateway's ranges form).
type CompactReadFilesRequest struct{ Files []string }

func (CompactReadFilesRequest) Tool() string { return "prism_read" }
func (r CompactReadFilesRequest) Args() map[string]any {
	ranges := make([]any, 0, len(r.Files))
	for _, f := range r.Files {
		ranges = append(ranges, map[string]any{"file": f, "from": 1, "to": CompactReadLimit})
	}
	return map[string]any{"ranges": ranges}
}

// LookupRequest returns symbol bodies by name.
type LookupRequest struct {
	Names  []string // one name is sent as a string, several as a list
	AsList bool     // send even one name as a list (the batch answer shape)
	Fields []string
	Repo   string
	File   string
}

func (LookupRequest) Tool() string { return "prism_lookup" }
func (r LookupRequest) Args() map[string]any {
	m := map[string]any{"name": oneOrList(r.Names)}
	if r.AsList {
		m["name"] = r.Names
	}
	set(m, "fields", anyList(r.Fields))
	set(m, "repo", r.Repo)
	set(m, "file", r.File)
	return m
}

func oneOrList(xs []string) any {
	if len(xs) == 1 {
		return xs[0]
	}
	return xs
}

// SearchRequest searches symbol names and source text.
type SearchRequest struct {
	Terms             []string // one term is sent as a string, several as a list
	Limit             *int     // nil = the default response budget
	Scope             string
	Regex             bool
	Case              string
	Word              bool
	NoDefaultExcludes bool
	IncludeSensitive  bool
	Paths, Globs      []string
	FilesOnly         bool
	IncludeBodies     *bool // nil = the default
	Exhaustive        bool
	RollupOnly        bool
	Context           *int // nil = unset; 0 is a valid explicit value
	Repo              string
}

func (SearchRequest) Tool() string { return "prism_search" }
func (r SearchRequest) Args() map[string]any {
	m := map[string]any{"query": oneOrList(r.Terms)}
	if r.Limit != nil {
		m["limit"] = *r.Limit
	}
	set(m, "scope", r.Scope)
	set(m, "regex", r.Regex)
	set(m, "case", r.Case)
	set(m, "word", r.Word)
	set(m, "no_default_excludes", r.NoDefaultExcludes)
	set(m, "include_sensitive", r.IncludeSensitive)
	set(m, "path", r.Paths)
	set(m, "glob", r.Globs)
	set(m, "files_only", r.FilesOnly)
	if r.IncludeBodies != nil {
		m["include_bodies"] = *r.IncludeBodies
	}
	set(m, "exhaustive", r.Exhaustive)
	set(m, "rollup_only", r.RollupOnly)
	if r.Context != nil {
		m["context"] = *r.Context
	}
	set(m, "repo", r.Repo)
	return m
}

// QueryRequest gathers related implementations, callers and tests around
// explicit anchors.
type QueryRequest struct {
	Terms    []string
	Limit    int // always sent
	Delivery string
	MaxFiles int
	Profile  string
	Include  []string
}

func (QueryRequest) Tool() string { return "prism_query" }
func (r QueryRequest) Args() map[string]any {
	m := map[string]any{"terms": r.Terms, "limit": r.Limit}
	set(m, "delivery", r.Delivery)
	set(m, "max_files", r.MaxFiles)
	set(m, "profile", r.Profile)
	set(m, "include", r.Include)
	return m
}

// ChangeImpactRequest lists every site a change to a symbol touches.
type ChangeImpactRequest struct {
	Query string
	File  string
}

func (ChangeImpactRequest) Tool() string { return "prism_change_impact" }
func (r ChangeImpactRequest) Args() map[string]any {
	m := map[string]any{"query": r.Query}
	set(m, "file", r.File)
	return m
}

// ChangeImpactDiffRequest is change-impact over a diff.
type ChangeImpactDiffRequest struct{ Base string }

func (ChangeImpactDiffRequest) Tool() string { return "prism_change_impact_diff" }
func (r ChangeImpactDiffRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "base", r.Base)
	return m
}

// VerifyRequest reviews a diff for missed sites, or checks removed names.
type VerifyRequest struct {
	Base           string
	Strict         bool
	RemovedSymbols []string
}

func (VerifyRequest) Tool() string { return "prism_verify" }
func (r VerifyRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "base", r.Base)
	set(m, "strict", r.Strict)
	set(m, "removed_symbols", r.RemovedSymbols)
	return m
}

// TestPlanRequest lists the tests a working-tree change affects.
type TestPlanRequest struct {
	Base     string
	Coverage []string
}

func (TestPlanRequest) Tool() string { return "prism_test_plan" }
func (r TestPlanRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "base", r.Base)
	set(m, "coverage", anyList(r.Coverage))
	return m
}

// NodeRequest shows one symbol or file with its neighbours.
type NodeRequest struct{ Name, File string }

func (NodeRequest) Tool() string { return "prism_node" }
func (r NodeRequest) Args() map[string]any {
	m := map[string]any{"name": r.Name}
	set(m, "file", r.File)
	return m
}

// ResolveRequest lists the declarations a name could mean.
type ResolveRequest struct{ Name string }

func (ResolveRequest) Tool() string           { return "prism_resolve" }
func (r ResolveRequest) Args() map[string]any { return map[string]any{"name": r.Name} }

// EdgesRequest lists a symbol's graph edges.
type EdgesRequest struct {
	Name, Direction string
	Kinds           []string
}

func (EdgesRequest) Tool() string { return "prism_edges" }
func (r EdgesRequest) Args() map[string]any {
	m := map[string]any{"name": r.Name, "direction": r.Direction}
	set(m, "kinds", anyList(r.Kinds))
	return m
}

// CallersRequest lists transitive callers.
type CallersRequest struct {
	Name  string
	Depth int
	File  string
}

func (CallersRequest) Tool() string { return "prism_callers" }
func (r CallersRequest) Args() map[string]any {
	m := map[string]any{"name": r.Name}
	set(m, "depth", r.Depth)
	set(m, "file", r.File)
	return m
}

// CallPathRequest finds a call path between two symbols.
type CallPathRequest struct{ From, To string }

func (CallPathRequest) Tool() string { return "prism_call_path" }
func (r CallPathRequest) Args() map[string]any {
	return map[string]any{"from": r.From, "to": r.To}
}

// ReferencesRequest lists the uses of a name.
type ReferencesRequest struct{ Name, File, Kind string }

func (ReferencesRequest) Tool() string { return "prism_references" }
func (r ReferencesRequest) Args() map[string]any {
	m := map[string]any{"name": r.Name}
	set(m, "file", r.File)
	set(m, "kind", r.Kind)
	return m
}

// RenamePlanRequest plans a rename.
type RenamePlanRequest struct {
	Query, NewName, File string
	AllowCollision       bool
}

func (RenamePlanRequest) Tool() string { return "prism_rename_plan" }
func (r RenamePlanRequest) Args() map[string]any {
	m := map[string]any{"query": r.Query, "newName": r.NewName}
	set(m, "file", r.File)
	set(m, "allow_collision", r.AllowCollision)
	return m
}

// MissingImplementationsRequest lists implementers lacking a member.
type MissingImplementationsRequest struct{ Query, File, Signature string }

func (MissingImplementationsRequest) Tool() string { return "prism_missing_implementations" }
func (r MissingImplementationsRequest) Args() map[string]any {
	m := map[string]any{"query": r.Query}
	set(m, "file", r.File)
	set(m, "signature", r.Signature)
	return m
}

// DeadCodeRequest lists unreachable symbols.
type DeadCodeRequest struct{ Roots []string }

func (DeadCodeRequest) Tool() string { return "prism_dead_code" }
func (r DeadCodeRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "roots", anyList(r.Roots))
	return m
}

// MapRequest is the component-level architecture map.
type MapRequest struct {
	Depth, MaxSites  int
	Component        string
	ExpandFrom       string // with ExpandTo: one edge's evidence
	ExpandTo         string
	IncludeTests     bool
	IncludeAuxiliary bool
}

func (MapRequest) Tool() string { return "prism_map" }
func (r MapRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "depth", r.Depth)
	set(m, "max_sites", r.MaxSites)
	set(m, "component", r.Component)
	set(m, "from", r.ExpandFrom)
	set(m, "to", r.ExpandTo)
	set(m, "include_tests", r.IncludeTests)
	set(m, "include_auxiliary", r.IncludeAuxiliary)
	return m
}

// CyclesRequest lists dependency cycles.
type CyclesRequest struct {
	Depth        int
	IncludeTests bool
}

func (CyclesRequest) Tool() string { return "prism_cycles" }
func (r CyclesRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "depth", r.Depth)
	set(m, "include_tests", r.IncludeTests)
	return m
}

// ArchCheckRequest validates architecture rules.
type ArchCheckRequest struct {
	Depth        int
	IncludeTests bool
	Strict       bool
	Deny         []string
}

func (ArchCheckRequest) Tool() string { return "prism_arch_check" }
func (r ArchCheckRequest) Args() map[string]any {
	m := map[string]any{}
	set(m, "depth", r.Depth)
	set(m, "include_tests", r.IncludeTests)
	set(m, "strict", r.Strict)
	set(m, "deny", anyList(r.Deny))
	return m
}

// CompactRequest compacts a conversation's turns.
type CompactRequest struct{ Turns []map[string]any }

func (CompactRequest) Tool() string           { return "prism_compact" }
func (r CompactRequest) Args() map[string]any { return map[string]any{"turns": r.Turns} }

// FeedbackRequest records a rating of a tool answer.
type FeedbackRequest struct {
	ToolName, QueryID, Notes string
	Rating                   int
}

func (FeedbackRequest) Tool() string { return "prism_feedback" }
func (r FeedbackRequest) Args() map[string]any {
	return map[string]any{"tool": r.ToolName, "queryId": r.QueryID, "rating": r.Rating, "notes": r.Notes}
}
