package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const lookupBatchMaxBytes = 64 * 1024

func lookupBatchNames(raw any) ([]string, bool, error) {
	var values []any
	switch value := raw.(type) {
	case nil, string:
		return nil, false, nil
	case []any:
		values = value
	case []string:
		for _, name := range value {
			values = append(values, name)
		}
	default:
		return nil, false, errors.New("name must be a string or an array of 1-10 nonempty strings")
	}
	if len(values) == 0 || len(values) > 10 {
		return nil, true, errors.New("name batch must contain 1-10 nonempty strings; no lookups were run")
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, true, errors.New("name batch must contain only nonempty strings; no lookups were run")
		}
		names = append(names, name)
	}
	return names, true, nil
}

func (h *Handler) toolLookupBatch(ctx context.Context, args map[string]any, names []string) (any, error) {
	results := make([]map[string]any, 0, len(names))
	var omitted []string
	used := 0
	for _, name := range names {
		one := make(map[string]any, len(args))
		for key, value := range args {
			one[key] = value
		}
		one["name"] = name
		result, err := h.toolLookup(ctx, one)
		entry := map[string]any{"name": name}
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["result"] = result
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("lookup batch encoding: %w", err)
		}
		if used+len(encoded) > lookupBatchMaxBytes {
			omitted = append(omitted, name)
			continue
		}
		used += len(encoded)
		results = append(results, entry)
	}
	out := map[string]any{"results": results}
	if len(omitted) > 0 {
		out["omitted"] = omitted
		out["note"] = "Batch body budget reached; omitted results were NOT delivered. Request those names individually or use fields=[\"signature\"]."
	}
	return out, nil
}

func renderLookupBatchAsText(out map[string]any) (string, bool) {
	switch out["results"].(type) {
	case []any, []map[string]any:
	default:
		return "", false
	}
	for key := range out {
		if key != "results" && key != "omitted" && key != "note" {
			return "", false
		}
	}
	var b strings.Builder
	for _, raw := range anySlice(out["results"]) {
		entry, ok := raw.(map[string]any)
		if !ok || len(entry) != 2 {
			return "", false
		}
		name, ok := entry["name"].(string)
		if !ok {
			return "", false
		}
		fmt.Fprintf(&b, "// lookup %s\n", name)
		if failure, ok := entry["error"].(string); ok {
			fmt.Fprintf(&b, "// ERROR: %s\n", failure)
			continue
		}
		result, ok := entry["result"].(map[string]any)
		if !ok {
			return "", false
		}
		text, ok := renderLookupAsText(result)
		if !ok {
			return "", false
		}
		b.WriteString(text)
	}
	if omitted := anySlice(out["omitted"]); len(omitted) > 0 {
		for _, name := range omitted {
			if _, ok := name.(string); !ok {
				return "", false
			}
		}
		fmt.Fprintf(&b, "// NOT DELIVERED: %v\n", omitted)
	}
	if note, _ := out["note"].(string); note != "" {
		fmt.Fprintf(&b, "// %s\n", note)
	}
	return b.String(), true
}
