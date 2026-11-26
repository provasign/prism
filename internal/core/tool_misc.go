package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/ranking"
)

func (h *Handler) toolIndex(_ context.Context, _ map[string]any) (any, error) {
	// Always index the server's own root: Invoke already rejected any foreign
	// dir, and the engine's store is bound to h.Root regardless of the arg.
	// Indexing large codebases can take several minutes; use a fresh context
	// with an extended deadline instead of the 60-second Invoke-level one.
	idxCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := h.Grove.Index(idxCtx, h.Root)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// toolCompact takes an array of conversation "turns" and emits a compressed
// view. Each turn is { "role": string, "content": string, "kind"?: string }.
func (h *Handler) toolCompact(_ context.Context, args map[string]any) (any, error) {
	turnsRaw, ok := args["turns"]
	if !ok {
		return nil, errors.New("turns is required (array)")
	}
	buf, _ := json.Marshal(turnsRaw)
	var turns []map[string]any
	if err := json.Unmarshal(buf, &turns); err != nil {
		return nil, fmt.Errorf("turns: %w", err)
	}
	out := make([]map[string]any, 0, len(turns))
	keepFullFromIdx := len(turns) - 3
	if keepFullFromIdx < 0 {
		keepFullFromIdx = 0
	}
	// Deduplicate exact file-read content by keeping only the most recent.
	seen := map[string]int{} // content hash → index in out
	originalTokens, deliveredTokens := 0, 0
	for i, t := range turns {
		content, _ := t["content"].(string)
		kind, _ := t["kind"].(string)
		originalTokens += ranking.EstimateTokens(content)
		if i >= keepFullFromIdx {
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(content)
			continue
		}
		switch kind {
		case "exploration", "file_read", "search":
			// Compress to a single-line reference summary.
			ref := "[" + kind + "] " + summarize(content, 120)
			t["content"] = ref
			h := compression.Hash(content)
			if prev, ok := seen[h]; ok {
				out[prev] = map[string]any{"role": "system", "content": "[dedup] previous " + kind + " repeated"}
			} else {
				seen[h] = len(out)
			}
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(ref)
		case "implementation", "edit":
			t["content"] = summarize(content, 400)
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(t["content"].(string))
		default:
			t["content"] = summarize(content, 200)
			out = append(out, t)
			deliveredTokens += ranking.EstimateTokens(t["content"].(string))
		}
	}
	savings := 0.0
	if originalTokens > 0 {
		savings = (1.0 - float64(deliveredTokens)/float64(originalTokens)) * 100.0
	}
	h.Ledger.Record("prism_compact", originalTokens, deliveredTokens)
	return map[string]any{
		"compressedTurns": out,
		"originalTokens":  originalTokens,
		"deliveredTokens": deliveredTokens,
		"savingsPercent":  savings,
	}, nil
}

func (h *Handler) toolSavings(_ context.Context, _ map[string]any) (any, error) {
	return h.Ledger.Snapshot(), nil
}

func (h *Handler) toolFeedback(_ context.Context, args map[string]any) (any, error) {
	tool := stringArg(args, "tool", "")
	queryID := stringArg(args, "queryId", "")
	rating := intArg(args, "rating", -1)
	notes := stringArg(args, "notes", "")
	if rating < 0 || rating > 5 {
		return nil, errors.New("rating must be in [0,5]")
	}
	entry := FeedbackEntry{
		Tool:      tool,
		QueryID:   queryID,
		Rating:    rating,
		Notes:     notes,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	h.fbMu.Lock()
	defer h.fbMu.Unlock()
	// Persisted: the in-memory list vanished when a CLI call exited, so
	// "recorded" was a no-op there (BUG-12).
	path := FeedbackPath(h.Root)
	line, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("feedback: %w", werr)
	}
	h.feedback = append(h.feedback, entry)
	return map[string]any{"recorded": entry, "file": path, "totalRatings": len(h.feedback)}, nil
}

// FeedbackPath is where feedback records are appended, one JSON per line.
func FeedbackPath(root string) string {
	return filepath.Join(root, ".grove", "feedback.jsonl")
}
