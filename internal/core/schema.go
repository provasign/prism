package core

import (
	"fmt"
	"sort"
	"strings"
)

// ToolSchemas returns the schema list for tools/list.
func ToolSchemas() []map[string]any {
	// The agent-facing surface, cut to six (2026-08-15). The 190-cell paired
	// A/B in research/harness/runs/swebench-live measured which of the
	// fourteen advertised tools agents actually reach for: search 95/190,
	// read 53, lookup 29, query 35, change_impact 2 — and map, dead_code,
	// rename_plan, missing_implementations, arch_check, node and index at
	// ZERO calls across all 190 cells. Eight tools were paying ~9.4 KB of
	// schema per session to never be called, and a long menu measurably
	// mis-routes the ones that are.
	//
	// So: the four measured-routing tools, plus change_impact (rarely
	// reached but carrying the whole concentrated win — 4.2 turns/$0.27 vs
	// grep's 26.8/$1.66, RESULTS.md §9.1) and verify (the pre-commit
	// completeness gate, 258 bytes).
	//
	// Everything dropped is still a CLI command and still Invoke-able over
	// HTTP — this narrows the agent menu, not the product. Do not re-add a
	// tool here without call-count evidence that agents reach for it.
	names := []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_change_impact", "prism_verify",
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		entry := map[string]any{
			"name":        n,
			"description": toolDescription(n),
			"inputSchema": toolSchema(n),
		}
		// NO RESIDENCY (2026-09-01): nothing is always-loaded; all six stay
		// deferred behind the ToolSearch hop. The 2026-08-30 hybrid (query
		// alone resident) backfired on the wide-change bed: across every
		// transcript with all six resident (v0.55.10 cells) sonnet's entry
		// point was prism_search — 23 search + 5 read calls, prism_query
		// ZERO — so residency backed the one tool agents never open with.
		// Worse, the visible prism_query masked deferral: steering's "if
		// you do not see prism_* they are DEFERRED" clause never fired
		// because one prism tool WAS visible, and 8/8 wide prism cells +
		// a post-fix probe made zero prism calls and zero ToolSearch hops.
		// With nothing resident the deferred-tools clause is unambiguous
		// (no prism_* visible at all -> hop), which is the one mechanism
		// measured to gate usage (48 e2e sessions: 25/25 that hopped used
		// prism, 23/23 that didn't used none). Do not re-add residency for
		// any tool without call-count evidence that agents actually open
		// with THAT tool when it is resident.
		out = append(out, entry)
	}
	return out
}

const compactReadLimit = 240

// CompactToolSchemas exposes the six primary operations through one MCP tool.
// It avoids top-level composition keywords because older MCP hosts have
// dropped tools carrying oneOf even when the JSON Schema is valid. The
// selected legacy handler remains the operation-specific authority.
func CompactToolSchemas() []map[string]any {
	prop := func(ops, note string, schema map[string]any) map[string]any {
		schema["description"] = "ops: " + ops + ". " + note
		return schema
	}
	stringOrList := func() map[string]any {
		return map[string]any{"type": []string{"string", "array"}, "minItems": 1,
			"maxItems": 10, "items": map[string]any{"type": "string"}}
	}
	name := map[string]any{"oneOf": []map[string]any{
		{"type": "string"},
		{"type": "array", "minItems": 1, "maxItems": 10, "items": map[string]any{
			"oneOf": []map[string]any{
				{"type": "string"},
				{"type": "object", "required": []string{"name", "file"}, "additionalProperties": false,
					"properties": map[string]any{"name": map[string]any{"type": "string"},
						"file": map[string]any{"type": "string"}}},
			},
		}},
	}}
	args := map[string]any{
		"name":        prop("lookup,change_impact,missing_implementations", "Symbol(s); impact takes one.", name),
		"symbol_file": prop("lookup,change_impact,missing_implementations", "Disambiguating file.", map[string]any{"type": "string"}),
		"fields": prop("lookup", "Projection.", map[string]any{"type": "array",
			"items": map[string]any{"type": "string", "enum": []string{"signature", "doc", "body", "kind", "parent", "modifiers"}}}),
		"signature": prop("lookup,change_impact", "Method signature: selects one overload.", map[string]any{"type": "string"}),
		"file":      prop("read", "Repo-relative path.", map[string]any{"type": "string"}),
		"from":      prop("read", "Inclusive first line.", map[string]any{"type": "integer", "minimum": 1}),
		"to":        prop("read", "Inclusive last line; clamped to 240 lines.", map[string]any{"type": "integer", "minimum": 1}),
		"ranges": prop("read", "Up to 10 {file,from,to} windows.", map[string]any{
			"type": "array", "minItems": 1, "maxItems": 10,
			"items": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"file", "from", "to"},
				"properties": map[string]any{
					"file": map[string]any{"type": "string"},
					"from": map[string]any{"type": "integer", "minimum": 1},
					"to":   map[string]any{"type": "integer", "minimum": 1},
				}},
		}),
		"force":           prop("read", "Return source again even if delivered earlier.", map[string]any{"type": "boolean"}),
		"terms":           prop("search,query", "Batch identifiers or exact substrings (up to 10), one per item: [\"alpha\",\"beta\"]; never combine distinct terms in one space-delimited string.", stringOrList()),
		"scope":           prop("search", "both|text|symbols.", map[string]any{"type": "string", "enum": []string{"both", "text", "symbols"}}),
		"paths":           prop("search,query", "Repo-relative paths.", stringOrList()),
		"glob":            prop("search,query", "File glob(s).", stringOrList()),
		"regex":           prop("search", "Regex text match.", map[string]any{"type": "boolean"}),
		"files_only":      prop("search", "Paths without lines.", map[string]any{"type": "boolean"}),
		"max_results":     prop("search", "Search-only result cap (max 2000).", map[string]any{"type": "integer", "minimum": 1, "maximum": exhaustiveSymbolCap}),
		"include_bodies":  prop("search", "Default: one bounded body or window for the top non-test match; false returns locators only; true returns up to two per term.", map[string]any{"type": "boolean", "default": true}),
		"context":         prop("search", "Lines around each match (grep -C N, max 15): windows only, no enclosing bodies. Above 15: the enclosing functions are delivered instead.", map[string]any{"type": "integer", "minimum": 0, "maximum": searchContextCap}),
		"rollup_only":     prop("search", "Return the compact hit rollup without source excerpts.", map[string]any{"type": "boolean"}),
		"removed_symbols": prop("verify", "Optional exact-identifier text check after removal; includes comments and docs.", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}),
		"base":            prop("verify", "Git ref for the full diff check (default HEAD).", map[string]any{"type": "string"}),
		"strict":          prop("verify", "Treat a review verdict as a gate failure; the verdict and evidence stay unchanged.", map[string]any{"type": "boolean"}),
		"exhaustive":      prop("search", "Request expanded inventory; check completion status.", map[string]any{"type": "boolean"}),
	}
	const opMap = "lookup: name[,symbol_file,fields,signature] | read: file,from,to or ranges | " +
		"search: terms[,scope,paths,glob,regex,files_only,max_results,exhaustive,include_bodies,context,rollup_only] | " +
		"query: terms[,paths,glob] | change_impact: name[,symbol_file,signature] | " +
		"verify: base,removed_symbols,strict | missing_implementations: name[,symbol_file]. " +
		"Known symbol → lookup; search only when location is unknown."
	return []map[string]any{{
		"name":        "prism",
		"description": "Use the op map for each discovery step; do not use shell tools to find code.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"op", "args"},
			"properties": map[string]any{
				"op": map[string]any{"type": "string",
					"enum":        []string{"lookup", "read", "search", "query", "change_impact", "verify", "missing_implementations"},
					"description": opMap},
				"args": map[string]any{"type": "object", "additionalProperties": false,
					"properties": args, "description": "Use only fields owned by the selected op."},
			},
		},
	}}
}

// modelProp is the shared "model" property injected into prism_query and
// prism_read. Agents must pass their current model ID so Prism can correctly
// size the context budget and session confidence thresholds.
// scopeFileProp is the declaring-file scope rename_plan and
// missing_implementations share with change_impact.
var scopeFileProp = map[string]any{
	"type":        "string",
	"description": "Disambiguate same-named types in different packages: only types declared in a file whose path contains this seed the answer.",
}

var modelProp = map[string]any{
	"type":        "string",
	"description": "Your model ID; sizes budgets. Optional.",
}

// contextUsedProp lets agents report how many tokens their context window
// already holds. Prism's ledger only sees its own deliveries; this hint
// keeps re-read confidence honest when most context flows through other
// tools (shell output, edits, other MCP servers).
var contextUsedProp = map[string]any{
	"type":        "integer",
	"description": "Tokens in your context now. Optional.",
}

// argAliases are accepted parameter names that deliberately do not appear
// in the published schema: legacy spellings the handlers still honor, plus
// "dir" which Invoke itself validates for every tool.
var argAliases = map[string]map[string]bool{
	"prism_read": {"path": true},
	// repo: a linked repository's id, or "all". Accepted without a schema
	// entry: the schema is paid for in every session, the argument only
	// matters where repositories are linked, and initialize names it there.
	"prism_search": {"repo": true},
	"prism_lookup": {"repo": true},
}

// rejectUnknownArgs errors on any argument key the tool's published schema
// does not declare (aliases above excepted). Tools without a published
// schema (HTTP-only surfaces) are not checked.
func rejectUnknownArgs(name string, args map[string]any) error {
	schema := toolSchema(name)
	if schema == nil {
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil
	}
	var unknown []string
	for k := range args {
		if k == "dir" || props[k] != nil || argAliases[name][k] {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	valid := make([]string, 0, len(props))
	for k := range props {
		valid = append(valid, k)
	}
	sort.Strings(valid)
	return fmt.Errorf("%s: unknown parameter(s) %s — this tool accepts: %s. "+
		"The call was NOT run; passing an unsupported parameter would have "+
		"silently returned an unscoped/unfiltered answer that reads as if the "+
		"parameter worked", name, strings.Join(unknown, ", "), strings.Join(valid, ", "))
}

func toolSchema(name string) map[string]any {
	open := map[string]any{"type": "object", "additionalProperties": true}
	switch name {
	case "prism_query":
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
	case "prism_read":
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
	case "prism_search":
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
	case "prism_lookup":
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
	case "prism_references":
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
	case "prism_resolve":
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
	case "prism_edges":
		return map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Seed symbol (bare or Type.Method)."},
				"direction": map[string]any{"type": "string", "enum": []string{"out", "in", "both"}, "description": "out = edges from the seed (callees, uses-type); in = edges into it (callers, tests). Default both."},
				"kinds":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"calls", "uses-type", "implements", "extends", "overrides", "contains", "defines", "imports"}}, "description": "Edge kinds to return. Default [calls]."},
			},
		}
	case "prism_index":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir": map[string]any{"type": "string", "description": "Directory to index (default: project root)."},
			},
		}
	case "prism_drift":
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	case "prism_node":
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
	case "prism_missing_implementations":
		// Own case, not change_impact's. file= is read since 2026-11-22
		// (BUG-05); signature= since GAP-04, for a member not declared yet.
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
	case "prism_change_impact":
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
	case "prism_rename_plan":
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
	case "prism_dead_code":
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
	case "prism_verify":
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
	case "prism_arch_check":
		// Schema had drifted to just "base" while the handler read deny/
		// strict/depth/max_sites/include_tests all along — found the moment
		// the unknown-arg validator landed and its own tests started
		// rejecting parameters the handler genuinely honors.
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
	case "prism_cycles":
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
	case "prism_map":
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
				"model":        modelProp,
				"context_used": contextUsedProp,
				"from": map[string]any{
					"type":        "string",
					"description": "With to: expand one induced edge into its reported indexed constituent sites.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "With from: the target component of the edge to expand.",
				},
			},
		}
	default:
		return open
	}
}

func toolDescription(name string) string {
	switch name {
	case "prism_query":
		return "RELATED-CONTEXT TOOL: use when you have explicit anchors and need their related implementations, " +
			"callers, and tests together. Batch relevant errors, classes, methods, and files into terms=[...] " +
			"(the only retrieval key). It returns budgeted source context: " +
			"one hop through the call graph plus a full-text pass, delivered as line-numbered " +
			"source windows with callers and a 'tested by' file:line. Check omission markers; do not re-read unchanged source " +
			"it shows; once the relevant implementation and test are present, make the smallest local edit. " +
			"Size with budget= and max_files=. If no anchor is known, locate one with prism_search first."
	case "prism_read":
		return "KNOWN-FILE TOOL: read a file, whole or by line range " +
			"(offset/limit), line-numbered. A repeat " +
			"read of an unchanged file returns a `// [prism:cached]` pointer — use the copy " +
			"you already have. For known symbol bodies use prism_lookup; for a related code neighborhood use prism_query."
	case "prism_search":
		return "LOCATOR TOOL: use this before Grep, Glob, find, rg, or shell search when code location " +
			"is genuinely unknown. Locate unknown names or paths: symbol names AND raw text (real rg/grep). " +
			"Known symbol? Use lookup for bodies or change_impact for affected sites directly. Batch up to 10 " +
			"terms in query=[...]. scope=\"text\" uses grep retrieval, cheapest — use it wherever you " +
			"would run grep/rg. Narrow with path=/glob=/files_only. context=N adds the lines " +
			"around each hit (grep -C). exhaustive=true requests a compact file/line inventory " +
			"while source excerpts stay sampled; check completion and partial-result warnings."
	case "prism_lookup":
		return "KNOWN-SYMBOL TOOL: read known symbol bodies by qualified name, with explicit omission markers for oversized results. " +
			"Batch related methods in name=[...] " +
			"(up to 10); use name=[{\"name\":\"Type.method\",\"file\":\"path/to/file\"}] for exact per-item file scope. For a small local bug, " +
			"read the relevant methods together; impact is for affected-site questions. " +
			"fields=[...] narrows to signature/doc/body/...; omit for whole bodies."
	case "prism_resolve":
		return "Disambiguate a name you ALREADY HAVE into the symbol(s) it could be — each with kind and " +
			"indexed file:line, test doubles tagged and last. Then prism_edges/prism_lookup the one you want. " +
			"Inspect ambiguous or stale locations before editing. " +
			"NOTE: resolve does not DISCOVER. If you don't yet know a symbol name (you only have a concept, " +
			"like 'where a secret is read'), first FIND the anchor with grep/prism_search/prism_references, " +
			"then resolve/traverse from it. Never guess names by trying resolve repeatedly."
	case "prism_edges":
		return "Walk the code graph one hop from a symbol. The graph has these edge kinds: " +
			"calls (X calls Y), uses-type (X mentions a type), " +
			"implements/extends/overrides, contains, defines, imports. direction=out gives edges FROM " +
			"the seed (its callees, the types it uses); direction=in gives edges INTO it (its " +
			"callers). Recipes: what does X call → (out, [calls]); who calls X → (in, [calls]); " +
			"interface dispatch: (out, [calls]) reports indexed candidate implementors. " +
			"Results are grouped by '<kind> <direction>' and capped with a reported total. " +
			"Locations point to indexed source; inspect ambiguous or stale edges. " +
			"This is the precise primitive — prefer it over prism_query when you know the anchor."
	case "prism_references":
		return "Find indexed syntactic occurrences of a symbol name (class/type/function/constant), " +
			"grouped by file, excluding comments and strings. Check result completeness. " +
			"Use for 'where is X used' and as evidence when considering a removal. " +
			"Reports 'ambiguous' when several definitions share the name. " +
			"Catches syntactic uses only — reflection/dynamic usage is not seen, so an empty " +
			"result is best-effort, not proof of dead code."
	case "prism_index":
		return "Delta-index the workspace through Grove. Indexing is AUTOMATIC — " +
			"the server indexes at startup, a never-indexed repo indexes itself on " +
			"first query, and whole-repo graph ops delta-refresh before they run. " +
			"Call this ONLY after a stale-context warning or an explicit " +
			"empty-index failure, never routinely."
	case "prism_compact":
		return "Compress a conversation history JSON array. " +
			"Call when the context window is near capacity to summarize older turns " +
			"while preserving recent ones."
	case "prism_drift":
		return "Check whether the ground shifted under you: re-check up to 500 recent files " +
			"delivered in this session against the working tree and report file changes, with symbol " +
			"detail and Fuse merge provenance when available. Call this when a stale-" +
			"context warning appears, before editing files you read a while ago, " +
			"or after another agent's branch lands."
	case "prism_feedback":
		return "Record a 0–5 quality rating for the last prism_query result. " +
			"0 = completely wrong context, 5 = perfect. Optional notes field."
	case "prism_change_impact":
		// Wording restored 2026-09-05: a trimmed version ("…— one call, before
		// editing…") coincided with haiku opening on prism_search instead of
		// this tool on both change tasks of the A/B gate (typeorm 2->4 turns,
		// grafana 5->18). Descriptions are steering; this one earns its bytes.
		return "CALL THIS BEFORE editing a known symbol's signature or enumerating affected sites. " +
			"Reports indexed potential impact sites, not a list of edits all required by the change. " +
			"Pass 'Type.method' for declarations, the indexed override/implementation family, " +
			"super-declarations, callers, and declaringTypes in one call. May include signatures, test labels, and bounded " +
			"matching call expressions; large results omit some evidence and mark this in evidenceNote. Read bodies " +
			"for behavior or evidence gaps, not routinely for site enumeration. 'partial' means coverage gaps; " +
			"follow coverageNote. For bounded results, relaySites is the canonical answer-shaped inventory: copy it instead of manually rebuilding the groups. " +
			"safeToClaimComplete is false because 'closed' describes indexed project scope, not proof of runtime completeness or receiver certainty. For a wide same-name " +
			"member or an external/unresolved interface with no local anchor, Prism infers the compatible local method family " +
			"and unions file-scoped resolved impacts in this call; signature= pins the external contract when known. Do not " +
			"guess concrete type names or issue one call per receiver. 'project-local' " +
			"+ overridesExternal = the method implements an external contract; a local signature edit may break it. " +
			"Relay the sites with their coverage notes and inspect ambiguous evidence."
	case "prism_verify":
		return "CHANGE CHECK (optional): with removed_symbols=[...] it is a mid-loop exact-identifier " +
			"mention check after removals; inspect code, comments, and docs. Without removed_symbols, it compares the " +
			"working diff with Prism's impact closure and reports potentially missed sites. Consider it for Python, " +
			"unchecked JavaScript, and PHP contract changes; for TypeScript or checked JavaScript, use it only when " +
			"affected files lack a complete typecheck. Skip it for Go, Java, Rust, C/C++, and C# after a complete " +
			"build/typecheck of affected targets. strict=true marks a review verdict as gateFailure without changing " +
			"the verdict. It is not a required closing step; run relevant tests."
	case "prism_missing_implementations":
		return "The interface-evolution companion to prism_change_impact: pass 'Type.method' " +
			"and get indexed subtype candidates that appear to lack the member. " +
			"Some may fail a compiler or typecheck when the member becomes required. Use when adding " +
			"a method to an interface/base class ('which implementors may need work?'), " +
			"auditing a contract, or after change_impact to plan the implementation work. " +
			"Result groups: missing (concrete types with no implementation, own or inherited " +
			"— potential required edits), abstractMissing (abstract classes, informational), " +
			"unverifiable (superclass chain leaves the index; an external base may provide " +
			"it — verify before treating as broken), implementedCount (coverage evidence). " +
			"defaultProvided=true means the indexed contract supplies a body; 'missing' may " +
			"inherit that default until the member becomes required. safeToClaimComplete is false: check completeness and " +
			"runtime/external behavior before treating a candidate as broken."
	case "prism_node":
		return "One-shot orientation on a single thing. Pass a SYMBOL name and get its source " +
			"plus its immediate graph neighbours (callers, callees, implementors) in one call — " +
			"the 'what is this and what touches it' view, without a lookup-then-edges round-trip. " +
			"Pass a repo-relative FILE PATH instead and get the file's source, the symbols it " +
			"defines, and indexed dependent files. Ambiguous symbol names return the candidate " +
			"list unchanged rather than guessing. Use this to orient; use change_impact when you " +
			"need indexed potential impact sites for a signature change."
	case "prism_rename_plan":
		return "Proposed rename plan: pass 'Type.method' and newName for concrete line edits " +
			"(file, line, before, after) at indexed resolved sites. First review coverage, 'unresolved', " +
			"and 'ambiguous' before applying edits; ambiguous lines may call a same-named method " +
			"on an unrelated type, so check receiver types. 'project-local' may indicate an " +
			"external contract requiring a coordinated rename. safeToClaimComplete is false. RELAY proposed sites and gaps, " +
			"then validate the resulting code with the relevant build or tests."
	case "prism_dead_code":
		return "Static deletion candidates: production functions/methods unreachable from " +
			"indexed roots (main/init, tests, exported symbols, plus optional roots=[...] for " +
			"framework hooks). Non-exported candidates also have no indexed name occurrence. " +
			"This is not proof that deletion is safe: callbacks, reflection, DI, serialization, " +
			"and codegen can use symbols invisibly. exportedUnreferenced lists public APIs with " +
			"no indexed project references; check external consumers. Relay the caveats and " +
			"validate deletions with a build and relevant tests."
	case "prism_map":
		return "Architecture map in ONE call: the repository's components (directories) and " +
			"reported indexed component dependencies, aggregated from call/import/type edges " +
			"crossing between them — with weights, per-kind breakdown, dependency cycles, and " +
			"the evidence tier of each reported claim. Use for: 'map/explain this repo', refactor and " +
			"extraction planning, layering questions, 'what depends on X'. Every edge is " +
			"evidence-backed within the indexed graph: pass from+to to expand one edge into the " +
			"list of concrete crossing sites (file:line). depth=1 gives the top-level view of " +
			"a large repo. The result covers indexed project edges at the reported " +
			"tier; external dependencies are excluded — this is the project's internal shape. " +
			"A repeat call whose recomputed result is IDENTICAL to one already delivered this " +
			"session returns a one-line [prism:cached] pointer — use the prior delivery."
	}
	return "Prism tool: " + name
}
