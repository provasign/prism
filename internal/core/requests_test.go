package core

import (
	"reflect"
	"testing"
)

// Each typed request must produce exactly the argument map its caller used
// to build by hand, Go types included: the tools read some arguments by type
// assertion, so []any and []string, or int and float64, are not the same.
func TestRequestArgs(t *testing.T) {
	two, zero, yes := 2, 0, true
	cases := []struct {
		name string
		req  Request
		tool string
		want map[string]any
	}{
		{"read", ReadRequest{File: "a.go", Offset: 3, Limit: 4}, "prism_read",
			map[string]any{"file": "a.go", "offset": 3, "limit": 4}},
		{"read sensitive", ReadRequest{File: ".env", IncludeSensitive: true}, "prism_read",
			map[string]any{"file": ".env", "include_sensitive": true}},
		{"compact read", CompactReadRequest{File: "a.go", From: 5, To: 9}, "prism_read",
			map[string]any{"file": "a.go", "from": 5, "to": 9}},
		{"compact read files", CompactReadFilesRequest{Files: []string{"a.go"}}, "prism_read",
			map[string]any{"ranges": []any{map[string]any{"file": "a.go", "from": 1, "to": CompactReadLimit}}}},
		{"lookup one", LookupRequest{Names: []string{"A"}}, "prism_lookup",
			map[string]any{"name": "A"}},
		{"lookup as list", LookupRequest{Names: []string{"A"}, AsList: true, Fields: []string{"doc"}, File: "a.go"}, "prism_lookup",
			map[string]any{"name": []string{"A"}, "fields": []any{"doc"}, "file": "a.go"}},
		{"search", SearchRequest{Terms: []string{"x"}}, "prism_search",
			map[string]any{"query": "x"}},
		{"search all", SearchRequest{Terms: []string{"x", "y"}, Limit: &two, Scope: "text", Regex: true, Case: "sensitive", Word: true,
			NoDefaultExcludes: true, IncludeSensitive: true, Paths: []string{"p"}, Globs: []string{"*.go"}, FilesOnly: true,
			IncludeBodies: &yes, Exhaustive: true, RollupOnly: true, Context: &zero, Repo: "r"}, "prism_search",
			map[string]any{"query": []string{"x", "y"}, "limit": 2, "scope": "text", "regex": true, "case": "sensitive", "word": true,
				"no_default_excludes": true, "include_sensitive": true, "path": []string{"p"}, "glob": []string{"*.go"}, "files_only": true,
				"include_bodies": true, "exhaustive": true, "rollup_only": true, "context": 0, "repo": "r"}},
		{"query", QueryRequest{Terms: []string{"A"}, Limit: 0, MaxFiles: 3, Include: []string{"docs"}}, "prism_query",
			map[string]any{"terms": []string{"A"}, "limit": 0, "max_files": 3, "include": []string{"docs"}}},
		{"change impact", ChangeImpactRequest{Query: "T.m", File: "a.go"}, "prism_change_impact",
			map[string]any{"query": "T.m", "file": "a.go"}},
		{"impact diff", ChangeImpactDiffRequest{Base: "HEAD"}, "prism_change_impact_diff",
			map[string]any{"base": "HEAD"}},
		{"verify", VerifyRequest{Strict: true, RemovedSymbols: []string{"A"}}, "prism_verify",
			map[string]any{"strict": true, "removed_symbols": []string{"A"}}},
		{"test plan", TestPlanRequest{Coverage: []string{"c.out"}}, "prism_test_plan",
			map[string]any{"coverage": []any{"c.out"}}},
		{"edges", EdgesRequest{Name: "A", Direction: "both", Kinds: []string{"calls"}}, "prism_edges",
			map[string]any{"name": "A", "direction": "both", "kinds": []any{"calls"}}},
		{"callers", CallersRequest{Name: "A", Depth: 2}, "prism_callers",
			map[string]any{"name": "A", "depth": 2}},
		{"call path", CallPathRequest{From: "A", To: "B"}, "prism_call_path",
			map[string]any{"from": "A", "to": "B"}},
		{"rename", RenamePlanRequest{Query: "A", NewName: "B", AllowCollision: true}, "prism_rename_plan",
			map[string]any{"query": "A", "newName": "B", "allow_collision": true}},
		{"dead code", DeadCodeRequest{Roots: []string{"main"}}, "prism_dead_code",
			map[string]any{"roots": []any{"main"}}},
		{"map", MapRequest{MaxSites: 3, ExpandFrom: "a", ExpandTo: "b", IncludeAuxiliary: true}, "prism_map",
			map[string]any{"max_sites": 3, "from": "a", "to": "b", "include_auxiliary": true}},
		{"arch", ArchCheckRequest{Deny: []string{"a -> b"}, Strict: true}, "prism_arch_check",
			map[string]any{"deny": []any{"a -> b"}, "strict": true}},
		{"feedback", FeedbackRequest{ToolName: "prism_query", Rating: 4}, "prism_feedback",
			map[string]any{"tool": "prism_query", "queryId": "", "rating": 4, "notes": ""}},
	}
	for _, c := range cases {
		if got := c.req.Tool(); got != c.tool {
			t.Errorf("%s: tool %q, want %q", c.name, got, c.tool)
		}
		if got := c.req.Args(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, got, c.want)
		}
	}
}
