package core

import (
	"context"
	"fmt"

	"github.com/provasign/prism/internal/grove"
)

func (h *Handler) toolDeadCode(ctx context.Context, args map[string]any) (any, error) {
	var roots []string
	if raw, ok := args["roots"].([]any); ok {
		for _, v := range raw {
			if sv, ok := v.(string); ok && sv != "" {
				roots = append(roots, sv)
			}
		}
	}
	r, err := h.Grove.DeadCode(ctx, roots)
	if err != nil {
		return nil, fmt.Errorf("dead-code: %w", err)
	}
	h.Ledger.RecordCall("prism_dead_code")
	site := func(s grove.SymbolRecord) map[string]any {
		qn := s.QualifiedName
		if qn == "" {
			qn = s.Name
		}
		return map[string]any{
			"name": s.Name, "qualifiedName": qn, "filePath": s.FilePath,
			"line": s.Span.Start, "kind": s.Kind,
		}
	}
	dead := make([]map[string]any, 0, len(r.Dead))
	for _, s := range r.Dead {
		dead = append(dead, site(s))
	}
	exported := make([]map[string]any, 0, len(r.ExportedUnreferenced))
	for _, s := range r.ExportedUnreferenced {
		exported = append(exported, site(s))
	}
	return map[string]any{
		"rootCount":            r.RootCount,
		"reachableCount":       r.ReachableCount,
		"considered":           r.Considered,
		"dead":                 dead,
		"exportedUnreferenced": exported,
		"caveats":              r.Caveats,
	}, nil
}
