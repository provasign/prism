package mcp

import (
	"fmt"

	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/textsearch"
)

// boundSearchPresentation limits the default discovery answer across every
// term. The search itself is unchanged: exact counts and completion remain
// available, and a smaller displayed inventory is explicitly labeled.
func boundSearchPresentation(out map[string]any) {
	fitTextPresentation(out, 4000, false)
}

// fitTextPresentation fits the text-hit display to tokenBudget. concise is
// the default-budget form (searchbudget.go): the undisplayed lines are
// counted in one short line instead of a sentence per term.
func fitTextPresentation(out map[string]any, tokenBudget int, concise bool) {
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
	warningReserve := 120 * len(results)
	// Text matches keep a floor of textHitFloor lines per term (or all of
	// them, if fewer). Symbols are bounded next by boundSymbolPresentation,
	// so a symbol-heavy term cannot push its own text matches to zero:
	// jackson pr6030 showed "displayed 0 of 8 retrieved text matches" while
	// eleven member symbols of one test class filled the budget, and the
	// hidden text line was the edit site.
	const textHitFloor = 3
	totals := make([]int, len(results))
	for _, g := range groups {
		totals[g.result] += len(g.hits)
	}
	floor, have := 0, make([]int, len(results))
	for floor < len(order) {
		satisfied := true
		for ri := range results {
			if have[ri] < minInt(textHitFloor, totals[ri]) {
				satisfied = false
				break
			}
		}
		if satisfied {
			break
		}
		have[groups[order[floor].group].result]++
		floor++
	}
	fit := func() int {
		low, high := 0, len(order)
		for low < high {
			mid := (low + high + 1) / 2
			apply(mid)
			if fits(warningReserve) {
				low = mid
			} else {
				high = mid - 1
			}
		}
		if low < floor {
			low = floor
		}
		return low
	}
	low := fit()
	// A file whose every line the display dropped would be named nowhere.
	// Name each matching file once (exact: these groups are the retrieved
	// set) and fit the lines again around that inventory.
	shownGroup := make([]bool, len(groups))
	for _, item := range order[:low] {
		shownGroup[item.group] = true
	}
	hidden := map[int]bool{}
	for gi, g := range groups {
		if len(g.hits) > 0 && !shownGroup[gi] {
			hidden[g.result] = true
		}
	}
	if len(hidden) > 0 {
		attached := false
		for ri := range results {
			result := results[ri]
			if !hidden[ri] {
				continue
			}
			if _, has := result["fileInventory"]; has {
				continue
			}
			var all []any
			for _, g := range groups {
				if g.result == ri {
					all = append(all, g.value)
				}
			}
			counts := textHitsFileCounts(all)
			// Test-file hits collapsed by the default budget still belong
			// to the matching-file set.
			for _, raw := range anySlice(result["testFileCounts"]) {
				if e, ok := raw.(map[string]any); ok {
					file, _ := e["file"].(string)
					counts = append(counts, textsearch.FileCount{File: file, Count: intArg(e, "hits", 0)})
				}
			}
			if len(counts) == 0 {
				continue
			}
			truncated, _ := result["truncated"].(bool)
			result["fileInventory"] = buildFileInventory(counts, !truncated)
			attached = true
		}
		if attached {
			low = fit()
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
		if shown[ri] < total && concise {
			result["budgetHidden"] = intArg(result, "budgetHidden", 0) + total - shown[ri]
		} else if shown[ri] < total {
			result["warning"] = appendNote(stringArg(result, "warning", ""), fmt.Sprintf(
				"Search completion/counts are unchanged; displayed %d of %d retrieved text matches under the shared response budget. Use exhaustive=true or narrow path=/glob= for the undisplayed lines.", shown[ri], total))
		}
	}
}

// boundSymbolPresentation is the same shared display limit for symbol-only
// searches and symbol-heavy batches. It runs after text-hit compaction, so
// exact text counts and the strongest source windows remain untouched.
func boundSymbolPresentation(out map[string]any) {
	fitSymbolPresentation(out, 4000, false)
}

// fitSymbolPresentation fits the symbol lists to tokenBudget; concise uses
// the short default-budget note.
func fitSymbolPresentation(out map[string]any, tokenBudget int, concise bool) {
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
	if concise {
		// The default budget keeps a floor of locators per term: symbol
		// lines are the cheap part of a locate answer.
		floor := 0
		for _, list := range lists {
			floor += minInt(searchBudgetSymbolFloor, len(list))
		}
		low = maxInt(low, floor)
	}
	shown := apply(low)
	for i, result := range results {
		if shown[i] < len(lists[i]) && concise {
			result["symbolsTruncated"] = true
			result["warning"] = appendNote(stringArg(result, "warning", ""), fmt.Sprintf(
				"%d of %d symbols shown; exhaustive=true or paths=/glob= for the rest", shown[i], len(lists[i])))
		} else if shown[i] < len(lists[i]) {
			result["symbolsTruncated"] = true
			result["warning"] = appendNote(stringArg(result, "warning", ""), fmt.Sprintf(
				"Displayed %d of %d retrieved indexed symbols under the shared response budget; use exhaustive=true or narrow path=/glob= for the undisplayed symbols.", shown[i], len(lists[i])))
		}
	}
}
