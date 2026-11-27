package core

// schemaQuery is the input schema of prism_query.
func schemaQuery() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"terms"},
		"properties": map[string]any{
			"terms": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "REQUIRED explicit anchors, e.g. [\"AccessCount\"], expanded via text and the call graph. If no anchor is known, locate one with prism_search first.",
			},
			"include": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"graph", "docs"}},
				"description": "graph (callers/callees), docs (filenames). Default [\"graph\"].",
			},
			"delivery": map[string]any{
				"type":        "string",
				"enum":        []string{"source", "symbols"},
				"description": "source = line-numbered windows + callers; symbols = compact list. Default source.",
			},
			"max_files": map[string]any{
				"type":        "integer",
				"description": "source only: max files shown as windows. Default is at least 2, with room for one related file after the named-anchor files.",
			},
			"model":        modelProp,
			"context_used": contextUsedProp,
			"profile":      map[string]any{"type": "string", "description": "Compatibility name: default|implement_feature|fix_bug|code_review currently share one deterministic edge/retrieval ranking rule"},
			"budget":       map[string]any{"type": "integer", "description": "Token budget (default 8000)."},
			"limit":        map[string]any{"type": "integer", "description": "Max candidates before ranking cutoff (default 50)."},
		},
	}
}

// schemaRead is the input schema of prism_read.
func schemaRead() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"file"},
		"properties": map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "File path relative to project root.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "First line (1-based); with limit, an exact window.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Lines from offset. Omit both for the whole file.",
			},
			"force":             map[string]any{"type": "boolean", "description": "Return source again even if it was delivered earlier."},
			"include_sensitive": map[string]any{"type": "boolean", "description": "Read a .env, key or credential file (withheld by default)."},
			"model":             modelProp,
			"context_used":      contextUsedProp,
			"task":              map[string]any{"type": "string", "description": "Log label only."},
		},
	}
}

// schemaSearch is the input schema of prism_search.
func schemaSearch() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        []string{"string", "array"},
				"items":       map[string]any{"type": "string"},
				"description": "One term or an array of up to 10 (batch them). Regex when regex=true.",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "Optional caller label only; does not affect retrieval or scope and is not echoed in results.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"both", "text", "symbols"},
				"description": "\"text\" = grep retrieval (cheapest; exhaustive results also label enclosing indexed symbols); \"symbols\" = index only. Default \"both\".",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "Regex for the text pass (invalid → literal).",
			},
			"path": map[string]any{
				"type":        []string{"string", "array"},
				"items":       map[string]any{"type": "string"},
				"description": "Restrict to repo-relative files/dirs, e.g. \"src/\" or [\"src/\",\"tests/\"].",
			},
			"glob": map[string]any{
				"type":        []string{"string", "array"},
				"items":       map[string]any{"type": "string"},
				"description": "Only files matching, e.g. \"*.py\".",
			},
			"exhaustive": map[string]any{
				"type":        "boolean",
				"description": "Raises text caps to 100000 hits / 10000 per file, symbols to 2000. Requests a compact file/line inventory while source excerpts stay sampled. Deadlines still apply; check the reported completeness.",
			},
			"files_only": map[string]any{
				"type":        "boolean",
				"description": "Paths only, no lines — cheapest \"where does this live\".",
			},
			"rollup_only": map[string]any{
				"type":        "boolean",
				"description": "On a truncated search return only hitRollup (grouped counts), no sample lines.",
			},
			"case": map[string]any{
				"type":        "string",
				"enum":        []string{"smart", "sensitive", "insensitive"},
				"description": "Text case: insensitive (default), sensitive, or smart (case-sensitive when the term has an uppercase letter).",
			},
			"word":                map[string]any{"type": "boolean", "description": "Whole-word text matches only."},
			"no_default_excludes": map[string]any{"type": "boolean", "description": "Also search agent-state directories (.shale, .claude, .cursor, ...) skipped by default."},
			"include_sensitive":   map[string]any{"type": "boolean", "description": "Show the text of matches in .env, key and credential files (withheld by default; the match still counts)."},
			"limit":               map[string]any{"type": "integer", "description": "Max results (default 25, maximum 2000; non-positive uses default)."},
			"context": map[string]any{
				"type":        "integer",
				"description": "Lines around each match (grep -C N, max 15) — instead of a follow-up read. Whole function? prism_lookup.",
			},
			"include_bodies": map[string]any{
				"type":        "boolean",
				"default":     true,
				"description": "By default, return one bounded enclosing body or labeled window for the top non-test hit. Set true for more (one per term first, up to four); false for locators only.",
			},
		},
	}
}

// schemaLookup is the input schema of prism_lookup.
func schemaLookup() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"oneOf": []map[string]any{
					{"type": "string"},
					{"type": "array", "minItems": 1, "maxItems": 10, "items": map[string]any{
						"oneOf": []map[string]any{
							{"type": "string"},
							{"type": "object", "required": []string{"name", "file"}, "additionalProperties": false,
								"properties": map[string]any{
									"name": map[string]any{"type": "string", "minLength": 1},
									"file": map[string]any{"type": "string", "minLength": 1, "description": "Exact repo-relative file scope; never widened on a miss."},
								}},
						},
					}},
				},
				"description": "One name, or 1-10 strings/{name,file} items. Objects use exact file scope; outer file hints apply only to strings. fields applies to all; oversized results are explicitly omitted with their scopes.",
			},
			"fields": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"signature", "doc", "body", "kind", "parent", "modifiers"}},
				"description": "Columns to read; omit for the full body.",
			},
			"file": map[string]any{
				"type":        "string",
				"description": "Legacy soft path/substring hint for string names; ignored if no candidate matches. Use {name,file} batch items for exact scope.",
			},
			"signature": map[string]any{
				"type":        "string",
				"description": "Declaration-shaped signature selecting one of several same-named overloads, e.g. \"Parser(JsonNode n, ObjectReadContext c)\".",
			},
		},
	}
}

// schemaReferences is the input schema of prism_references.
func schemaReferences() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Symbol name to find usages of (a class/type/function/constant). Type.member, or a file, resolves one symbol and returns its sites; a bare name returns every use of the name.",
			},
			"file": scopeFileProp,
			"kind": map[string]any{
				"type":        "string",
				"description": "Keep only these resolved site kinds, comma-separated: call, read, write, decl, type.",
			},
		},
	}
}

// schemaResolve is the input schema of prism_resolve.
func schemaResolve() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Bare or qualified name (e.g. \"Get\" or \"SecretsKVStoreSQL.Get\").",
			},
		},
	}
}

// schemaEdges is the input schema of prism_edges.
func schemaEdges() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name":      map[string]any{"type": "string", "description": "Seed symbol (bare or Type.Method)."},
			"direction": map[string]any{"type": "string", "enum": []string{"out", "in", "both"}, "description": "out = edges from the seed (callees, uses-type); in = edges into it (callers, tests). Default both."},
			"kinds":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"calls", "uses-type", "implements", "extends", "overrides", "contains", "defines", "imports"}}, "description": "Edge kinds to return. Default [calls]."},
		},
	}
}

// schemaIndex is the input schema of prism_index.
func schemaIndex() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dir": map[string]any{"type": "string", "description": "Directory to index (default: project root)."},
		},
	}
}

// schemaDrift is the input schema of prism_drift.
func schemaDrift() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// schemaNode is the input schema of prism_node.
func schemaNode() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "A symbol name (e.g. \"JsonNode.get\") OR a repo-relative file path (e.g. \"internal/store/store.go\").",
			},
			"file": map[string]any{
				"type":        "string",
				"description": "Optional disambiguator when several symbols share the name: the file path the intended one lives in.",
			},
		},
	}
}

// schemaMissingImplementations is the input schema of prism_missing_implementations.
// Own schema, not change_impact's. file= is read since 2026-11-22
// (BUG-05); signature= since GAP-04, for a member not declared yet.
func schemaMissingImplementations() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Type.method or Type.method(ParamType, ...) — the interface/abstract member to check implementations of.",
			},
			"signature": map[string]any{
				"type":        "string",
				"description": "For a member the type does not declare yet: the planned signature; implementers with a matching method are marked.",
			},
			"file":         scopeFileProp,
			"model":        modelProp,
			"context_used": contextUsedProp,
		},
	}
}

// schemaChangeImpact is the input schema of prism_change_impact.
func schemaChangeImpact() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Type.method or Type.method(ParamType, ...) — e.g. \"JsonSerializer.serialize(T, JsonGenerator, SerializerProvider)\". A bare member name (\"serialize\") or file:line (\"src/Foo.java:120\") also works when it resolves to exactly one symbol; if several match, the error lists the candidates.",
			},
			"file": map[string]any{
				"type":        "string",
				"description": "Disambiguate same-named types in different packages: only types declared in a file whose path contains this seed the closure (change_impact only; the result says when this is needed).",
			},
			"signature": map[string]any{
				"type":        "string",
				"description": "Optional method signature. Selects one of several same-named overloads; when no local interface declaration exists, match indexed compatible local implementations and union their reported callers in one call; check coverage.",
			},
			"model":        modelProp,
			"context_used": contextUsedProp,
		},
	}
}

// schemaRenamePlan is the input schema of prism_rename_plan.
func schemaRenamePlan() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"query", "newName"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Type.method or Type.method(ParamType, ...) — the member being renamed.",
			},
			"newName": map[string]any{
				"type":        "string",
				"description": "The new member name (bare identifier).",
			},
			"allow_collision": map[string]any{
				"type":        "boolean",
				"description": "Return the plan even when newName is already declared where the renamed member would land (status: collision lists those declarations).",
			},
			"file":         scopeFileProp,
			"model":        modelProp,
			"context_used": contextUsedProp,
		},
	}
}

// schemaDeadCode is the input schema of prism_dead_code.
func schemaDeadCode() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"roots": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Extra entry-point symbol names beyond the defaults (main/init, tests, exported symbols) — e.g. framework hooks registered by name.",
			},
			"model":        modelProp,
			"context_used": contextUsedProp,
		},
	}
}

// schemaVerify is the input schema of prism_verify.
func schemaVerify() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Git ref to diff the working tree against (default \"HEAD\"). The change-set is computed relative to this.",
			},
			"removed_symbols": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Optional mid-loop exact-identifier text check after removal. " +
					"Reports mentions in code, comments, and docs; inspect them rather than " +
					"assuming each is a live reference. Full verify is not a required final gate.",
			},
			"strict": map[string]any{
				"type": "boolean",
				"description": "Treat a review verdict as a gate failure without changing the verdict or evidence. " +
					"The response's gateFailure matches the CLI exit decision. Has no effect with removed_symbols.",
			},
		},
	}
}

// schemaArchCheck is the input schema of prism_arch_check.
// Schema had drifted to just "base" while the handler read deny/
// strict/depth/max_sites/include_tests all along — found the moment
// the unknown-arg validator landed and its own tests started
// rejecting parameters the handler genuinely honors.
func schemaArchCheck() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Optional git ref: report only violations INTRODUCED since it, instead of every current violation.",
			},
			"deny": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Extra \"<from> -> <to>\" deny rules checked in addition to prism.yaml's arch_deny lines.",
			},
			"strict": map[string]any{
				"type":        "boolean",
				"description": "Escalate heuristic-evidence review items to failures.",
			},
			"depth": map[string]any{
				"type":        "integer",
				"description": "Truncate components to the first N path segments (0 = one component per directory).",
			},
			"max_sites": map[string]any{
				"type":        "integer",
				"description": "Max cited file:line sites per violation (default 5).",
			},
			"include_tests": map[string]any{
				"type":        "boolean",
				"description": "Include test files in the component view.",
			},
		},
	}
}

// schemaCycles is the input schema of prism_cycles.
func schemaCycles() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"depth": map[string]any{
				"type":        "integer",
				"description": "Truncate components to the first N path segments before cycle detection (0 = one component per directory).",
			},
			"model":        modelProp,
			"context_used": contextUsedProp,
		},
	}
}

// schemaMap is the input schema of prism_map.
func schemaMap() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"depth": map[string]any{
				"type":        "integer",
				"description": "Truncate components to the first N path segments (0 = one component per directory).",
			},
			"max_sites": map[string]any{
				"type":        "integer",
				"description": "Constituent sites kept per induced edge (default 5); weight always carries the full count.",
			},
			"component": map[string]any{
				"type":        "string",
				"description": "Only return edges touching this component.",
			},
			"include_tests": map[string]any{
				"type":        "boolean",
				"description": "Include test files (excluded by default — the map shows the production shape; the result reports how many were excluded).",
			},
			"include_auxiliary": map[string]any{
				"type":        "boolean",
				"description": "Include auxiliary components (docs/examples directories, and isolated code in a language the rest does not use); excluded by default and named in the result.",
			},
			"no_repos": map[string]any{
				"type":        "boolean",
				"description": "Leave out crossRepo: the edges between this repository's components and those of linked repositories and of repositories uploaded to the link server (included by default when a workspace or server is configured).",
			},
			"model":        modelProp,
			"context_used": contextUsedProp,
			"from": map[string]any{
				"type":        "string",
				"description": "With to: expand one induced edge into its reported indexed constituent sites. A repository-qualified end (repo:component) expands a cross-repository edge.",
			},
			"to": map[string]any{
				"type":        "string",
				"description": "With from: the target component of the edge to expand.",
			},
		},
	}
}
