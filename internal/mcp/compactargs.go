package mcp

import (
	"fmt"
	"strings"
)

var compactFields = map[string][]string{
	"lookup":        {"name", "symbol_file", "fields"},
	"read":          {"file", "from", "to", "ranges"},
	"search":        {"terms", "scope", "paths", "glob", "regex", "files_only", "max_results", "exhaustive"},
	"query":         {"task", "terms", "paths", "glob"},
	"change_impact": {"name", "symbol_file", "signature"},
	"verify":        {"base", "removed_symbols", "strict"},
}

func compactFieldError(op, field string) error {
	accepted := strings.Join(compactFields[op], ", ")
	replacement := map[string]map[string]string{
		"lookup":        {"file": "symbol_file"},
		"read":          {"offset": "from", "limit": "to", "lines": "from and to"},
		"search":        {"query": "terms", "path": "paths", "limit": "max_results"},
		"query":         {"query": "terms", "path": "paths", "limit": "task and terms"},
		"change_impact": {"query": "name", "file": "symbol_file"},
	}
	if next := replacement[op][field]; next != "" {
		return fmt.Errorf("prism: %s does not accept args.%s; use %s. Accepted fields: %s", op, field, next, accepted)
	}
	return fmt.Errorf("prism: %s does not accept args.%s. Accepted fields: %s", op, field, accepted)
}

func compactStringList(value any) bool {
	switch v := value.(type) {
	case string:
		return v != ""
	case []any:
		if len(v) < 1 || len(v) > 10 {
			return false
		}
		for _, item := range v {
			s, ok := item.(string)
			if !ok || s == "" {
				return false
			}
		}
		return true
	}
	return false
}

func compactName(value any) bool {
	if s, ok := value.(string); ok {
		return s != ""
	}
	items, ok := value.([]any)
	if !ok || len(items) < 1 || len(items) > 10 {
		return false
	}
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if v == "" {
				return false
			}
		case map[string]any:
			name, nameOK := v["name"].(string)
			file, fileOK := v["file"].(string)
			if len(v) != 2 || !nameOK || name == "" || !fileOK || file == "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func compactRanges(value any) bool {
	items, ok := value.([]any)
	if !ok || len(items) < 1 || len(items) > 10 {
		return false
	}
	for _, item := range items {
		r, ok := item.(map[string]any)
		if !ok || len(r) != 3 {
			return false
		}
		file, fileOK := r["file"].(string)
		if !fileOK || file == "" ||
			!schemaAcceptsValue(map[string]any{"type": "integer"}, r["from"]) ||
			!schemaAcceptsValue(map[string]any{"type": "integer"}, r["to"]) {
			return false
		}
		from, to := intArg(r, "from", 0), intArg(r, "to", 0)
		if from < 1 || to < from {
			return false
		}
	}
	return true
}

func compactSingleReadNote(out any, envelope map[string]any) {
	result, ok := out.(map[string]any)
	if !ok {
		return
	}
	args, ok := envelope["args"].(map[string]any)
	if !ok {
		return
	}
	from := intArg(args, "from", 1)
	to, explicitTo := args["to"]
	end := intArg(result, "endLine", 0)
	total := intArg(result, "totalLines", 0)
	if end == 0 {
		return
	}
	if (explicitTo && intArg(args, "to", 0)-from+1 > compactReadLimit) ||
		(!explicitTo && total > end) {
		requested := "end of file"
		if explicitTo {
			requested = fmt.Sprint(to)
		}
		result["note"] = appendNote(stringArg(result, "note", ""),
			fmt.Sprintf("compact read %d-%s clamped to %d-%d (max %d lines)", from, requested, from, end, compactReadLimit))
	}
}
