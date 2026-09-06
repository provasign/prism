package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type lookupRequest struct {
	name string
	file string
}

func lookupBatchRequests(raw any) ([]lookupRequest, bool, error) {
	files := map[int]string{}
	if values, ok := raw.([]any); ok {
		names := append([]any(nil), values...)
		for i, value := range values {
			if item, ok := value.(map[string]any); ok {
				name, nameOK := item["name"].(string)
				file, fileOK := item["file"].(string)
				if len(item) != 2 || !nameOK || !fileOK || strings.TrimSpace(name) == "" || strings.TrimSpace(file) == "" {
					return nil, true, fmt.Errorf("name[%d] must contain only nonempty string name and file; no lookups were run", i)
				}
				names[i], files[i] = name, file
			}
		}
		raw = names
	}
	names, batch, err := lookupBatchNames(raw)
	if err != nil || !batch {
		return nil, batch, err
	}
	requests := make([]lookupRequest, len(names))
	for i, name := range names {
		requests[i] = lookupRequest{name: name, file: files[i]}
	}
	return requests, true, nil
}

func (h *Handler) lookupFileScope(file string) (string, error) {
	if filepath.IsAbs(file) {
		return "", errors.New("scoped lookup file must be repo-relative")
	}
	abs, rel, err := safePathWithinRoot(h.Root, file)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("scoped lookup file %q: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("scoped lookup file %q is not a regular file", file)
	}
	return rel, nil
}

func (h *Handler) toolLookupBatch(ctx context.Context, args map[string]any, requests []lookupRequest) (any, error) {
	results := make([]map[string]any, 0, len(requests))
	var omitted []string
	var omittedItems []map[string]any
	used := 0
	for _, request := range requests {
		one := make(map[string]any, len(args))
		for key, value := range args {
			one[key] = value
		}
		one["name"] = request.name
		result, err := h.lookupSymbol(ctx, one, request.file)
		entry := map[string]any{"name": request.name}
		if request.file != "" {
			entry["file"] = request.file
		}
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
			if request.file == "" {
				omitted = append(omitted, request.name)
			} else {
				omittedItems = append(omittedItems, map[string]any{"name": request.name, "file": request.file})
			}
			continue
		}
		used += len(encoded)
		results = append(results, entry)
	}
	out := map[string]any{"results": results}
	if len(omitted) > 0 {
		out["omitted"] = omitted
	}
	if len(omittedItems) > 0 {
		out["omittedItems"] = omittedItems
	}
	if len(omitted)+len(omittedItems) > 0 {
		out["note"] = "Batch body budget reached; omitted results were NOT delivered. Retry in smaller batches with the same file scopes or use fields=[\"signature\"]."
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
		if key != "results" && key != "omitted" && key != "omittedItems" && key != "note" {
			return "", false
		}
	}
	var b strings.Builder
	for _, raw := range anySlice(out["results"]) {
		entry, ok := raw.(map[string]any)
		if !ok {
			return "", false
		}
		label, ok := lookupRequestLabel(entry)
		if !ok || (len(entry) != 2 && len(entry) != 3) {
			return "", false
		}
		for key := range entry {
			if key != "name" && key != "file" && key != "error" && key != "result" {
				return "", false
			}
		}
		_, hasError := entry["error"]
		_, hasResult := entry["result"]
		if hasError == hasResult {
			return "", false
		}
		fmt.Fprintf(&b, "// lookup %s\n", label)
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
	if raw, exists := out["omittedItems"]; exists {
		switch raw.(type) {
		case []any, []map[string]any:
		default:
			return "", false
		}
		for _, value := range anySlice(raw) {
			item, ok := value.(map[string]any)
			if !ok || len(item) != 2 || item["file"] == nil {
				return "", false
			}
			label, ok := lookupRequestLabel(item)
			if !ok {
				return "", false
			}
			fmt.Fprintf(&b, "// NOT DELIVERED: %s\n", label)
		}
	}
	if note, _ := out["note"].(string); note != "" {
		fmt.Fprintf(&b, "// %s\n", note)
	}
	return b.String(), true
}

func lookupRequestLabel(entry map[string]any) (string, bool) {
	name, ok := entry["name"].(string)
	if !ok {
		return "", false
	}
	if raw, exists := entry["file"]; exists {
		file, ok := raw.(string)
		if !ok || file == "" {
			return "", false
		}
		return fmt.Sprintf("%s (file=%q)", name, file), true
	}
	return name, true
}
