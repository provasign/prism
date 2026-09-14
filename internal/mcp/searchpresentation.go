package mcp

import (
	"fmt"

	"github.com/provasign/prism/internal/ranking"
)

// boundSearchPresentation limits the default discovery answer across every
// term. The search itself is unchanged: exact counts and completion remain
// available, and a smaller displayed inventory is explicitly labeled.
func boundSearchPresentation(out map[string]any) {
	const tokenBudget = 4000
	fits := func(reserve int) bool {
		text, ok := renderSearchAsText(out)
		return ok && ranking.EstimateTokens(text) <= tokenBudget-reserve
	}
	if fits(0) {
		return
	}
	results := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		results = batch
	}
	type group struct {
		result int
		value  map[string]any
		hits   []any
	}
	var groups []group
	for ri, result := range results {
		for _, raw := range anySlice(result["textHits"]) {
			value, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			groups = append(groups, group{result: ri, value: value, hits: anySlice(value["hits"])})
		}
	}
	// Keep context for only the first few ranked hits. The exact matching
	// lines remain visible, so this step never samples the match inventory.
	contextHits := 0
	for _, g := range groups {
		for _, raw := range g.hits {
			hit, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			contextHits++
			if contextHits > 4 {
				delete(hit, "before")
				delete(hit, "after")
			}
		}
	}
	if fits(0) || len(groups) == 0 {
		return
	}
	// One hit from each file and term precedes second hits from any file.
	// This prevents a dense first file from consuming the entire display.
	type ref struct{ group, hit int }
	var order []ref
	for pass := 0; ; pass++ {
		added := false
		for gi, g := range groups {
			if pass < len(g.hits) {
				order = append(order, ref{gi, pass})
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(order) == 0 {
		return
	}
	apply := func(keep int) []int {
		selected := make([][]any, len(groups))
		shown := make([]int, len(results))
		for _, item := range order[:keep] {
			selected[item.group] = append(selected[item.group], groups[item.group].hits[item.hit])
			shown[groups[item.group].result]++
		}
		for ri, result := range results {
			var displayed []map[string]any
			for gi, g := range groups {
				if g.result != ri {
					continue
				}
				if len(selected[gi]) == 0 && len(g.hits) > 0 {
					continue
				}
				copy := make(map[string]any, len(g.value))
				for key, value := range g.value {
					copy[key] = value
				}
				if len(g.hits) > 0 {
					copy["hits"] = selected[gi]
				}
				displayed = append(displayed, copy)
			}
			result["textHits"] = displayed
		}
		return shown
	}
	low, high := 0, len(order)
	warningReserve := 120 * len(results)
	for low < high {
		mid := (low + high + 1) / 2
		apply(mid)
		if fits(warningReserve) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	shown := apply(low)
	for ri, result := range results {
		total := 0
		for _, g := range groups {
			if g.result == ri {
				total += len(g.hits)
			}
		}
		if shown[ri] < total {
			result["warning"] = appendNote(stringArg(result, "warning", ""), fmt.Sprintf(
				"Search completion/counts are unchanged; displayed %d of %d retrieved text matches under the shared response budget. Use exhaustive=true or narrow path=/glob= for the undisplayed lines.", shown[ri], total))
		}
	}
}

// boundSymbolPresentation is the same shared display limit for symbol-only
// searches and symbol-heavy batches. It runs after text-hit compaction, so
// exact text counts and the strongest source windows remain untouched.
func boundSymbolPresentation(out map[string]any) {
	const tokenBudget = 4000
	renderedTokens := func() int {
		text, ok := renderSearchAsText(out)
		if !ok {
			return 0
		}
		return ranking.EstimateTokens(text)
	}
	if renderedTokens() <= tokenBudget {
		return
	}
	results := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		results = batch
	}
	lists := make([][]any, len(results))
	for i, result := range results {
		lists[i] = anySlice(result["symbols"])
	}
	type ref struct{ result, index int }
	var order []ref
	for pass := 0; ; pass++ {
		added := false
		for ri, list := range lists {
			if pass < len(list) {
				order = append(order, ref{ri, pass})
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(order) == 0 {
		return
	}
	apply := func(keep int) []int {
		shown := make([]int, len(results))
		selected := make([][]any, len(results))
		for _, item := range order[:keep] {
			selected[item.result] = append(selected[item.result], lists[item.result][item.index])
			shown[item.result]++
		}
		for i, result := range results {
			result["symbols"] = selected[i]
		}
		return shown
	}
	low, high := 0, len(order)
	reserve := 120 * len(results)
	for low < high {
		mid := (low + high + 1) / 2
		apply(mid)
		if renderedTokens() <= tokenBudget-reserve {
			low = mid
		} else {
			high = mid - 1
		}
	}
	shown := apply(low)
	for i, result := range results {
		if shown[i] < len(lists[i]) {
			result["symbolsTruncated"] = true
			result["warning"] = appendNote(stringArg(result, "warning", ""), fmt.Sprintf(
				"Displayed %d of %d retrieved indexed symbols under the shared response budget; use exhaustive=true or narrow path=/glob= for the undisplayed symbols.", shown[i], len(lists[i])))
		}
	}
}
