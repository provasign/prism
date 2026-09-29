package mcp

import (
	"context"
	"strconv"

	"github.com/provasign/prism/internal/textsearch"
)

// completeContextHitMax: complete result sets up to this many lines keep
// their context windows (a bug-fix locate, where context saves a Read).
const completeContextHitMax = 10

func (h *Handler) renderedTextSearchHits(ctx context.Context, r textsearch.Result, exhaustive, contextAsked bool) []map[string]any {
	if r.ResultsComplete && !exhaustive {
		// A complete set past a handful of lines is a site list (a rename,
		// a sweep): every line, grep-shaped, beats context around some.
		if len(r.Hits) > completeContextHitMax && !contextAsked {
			return h.renderExhaustiveLines(ctx, r.Hits)
		}
		return h.renderCompleteTextMatches(r.Hits)
	}
	return h.renderTextMatches(ctx, r.Hits, exhaustive)
}

func attachTextSearchCompleteness(out map[string]any, r textsearch.Result) {
	if r.Truncated || r.CountComplete {
		out["totalHits"] = r.TotalHits
		out["filesMatched"] = r.FilesMatched
	}
	if r.CountComplete {
		out["countComplete"] = true
	}
	if r.ResultsComplete {
		out["resultsComplete"] = true
	}
}

func textMatchCount(r textsearch.Result, lower bool) string {
	if r.CountComplete {
		return strconv.Itoa(r.TotalHits)
	}
	if lower {
		return "at least " + strconv.Itoa(r.TotalHits)
	}
	return "AT LEAST " + strconv.Itoa(r.TotalHits)
}

func textMatchFileWord(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}
