package mcp

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/provasign/prism/internal/textsearch"
)

// Matching-file inventory for sampled text searches.
//
// Measured 2026-09-26 (jackson-databind pr5977): `staticTyping` matched 147
// lines in 31 files; the response showed 25 lines and a rollup of the ten
// densest enclosing symbols. The file that needed the fix
// (ser/jdk/ObjectArraySerializer.java, `_staticTyping`) was named nowhere,
// the agent concluded "already fixed" from the surfaced sites and submitted
// an empty diff, while grep found the file. A sample of lines is fine; a
// sample of FILES is silent absence. So whenever the lines are sampled, every
// matching file is named once with its hit count, one short line per file.
const (
	// fileInventoryShowCap bounds the per-file lines. Past it the files with
	// the most hits are listed and the rest roll up by directory.
	fileInventoryShowCap = 80
	// fileInventoryDirCap bounds the directory rollup of the remainder.
	fileInventoryDirCap = 40
	// fileInventoryCountTimeout bounds the extra count pass run when the
	// search itself did not produce exact per-file counts.
	fileInventoryCountTimeout = 2 * time.Second
)

// textFileInventory returns the matching-file inventory for a sampled text
// result: from the search's own exact count pass when it ran, otherwise from
// a bounded count pass with the same options. Nil when nothing is known.
func (h *Handler) textFileInventory(ctx context.Context, q string, r textsearch.Result, opts textsearch.Options) map[string]any {
	counts, complete := r.FileCounts, r.CountComplete && len(r.FileCounts) > 0
	if !complete {
		cctx, cancel := context.WithTimeout(ctx, fileInventoryCountTimeout)
		c := textsearch.Count(cctx, h.Root, q, opts)
		cancel()
		counts, complete = c.Files, c.Complete
	}
	if len(counts) == 0 {
		return nil
	}
	return buildFileInventory(counts, complete)
}

// buildFileInventory names every file (up to fileInventoryShowCap, by path)
// and rolls the remainder up by directory, so no matching file is invisible
// and the payload stays one short line per file.
func buildFileInventory(counts []textsearch.FileCount, complete bool) map[string]any {
	all := make([]textsearch.FileCount, len(counts))
	copy(all, counts)
	totalHits := 0
	for _, c := range all {
		totalHits += c.Count
	}
	shown, rest := all, []textsearch.FileCount(nil)
	if len(all) > fileInventoryShowCap {
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].Count != all[j].Count {
				return all[i].Count > all[j].Count
			}
			return all[i].File < all[j].File
		})
		shown, rest = all[:fileInventoryShowCap], all[fileInventoryShowCap:]
	}
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].File < shown[j].File })
	files := make([]map[string]any, 0, len(shown))
	for _, c := range shown {
		files = append(files, map[string]any{"file": c.File, "hits": c.Count})
	}
	inv := map[string]any{
		"total":    len(all),
		"hits":     totalHits,
		"complete": complete,
		"files":    files,
	}
	if len(rest) == 0 {
		return inv
	}
	type dirCount struct {
		dir         string
		files, hits int
	}
	byDir := map[string]*dirCount{}
	var order []*dirCount
	for _, c := range rest {
		d := path.Dir(c.File)
		dc := byDir[d]
		if dc == nil {
			dc = &dirCount{dir: d}
			byDir[d] = dc
			order = append(order, dc)
		}
		dc.files++
		dc.hits += c.Count
	}
	if len(order) > fileInventoryDirCap {
		sort.SliceStable(order, func(i, j int) bool {
			if order[i].files != order[j].files {
				return order[i].files > order[j].files
			}
			return order[i].dir < order[j].dir
		})
		var moreFiles, moreHits int
		for _, dc := range order[fileInventoryDirCap:] {
			moreFiles += dc.files
			moreHits += dc.hits
		}
		inv["moreDirs"] = map[string]any{"dirs": len(order) - fileInventoryDirCap, "files": moreFiles, "hits": moreHits}
		order = order[:fileInventoryDirCap]
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].dir < order[j].dir })
	dirs := make([]map[string]any, 0, len(order))
	for _, dc := range order {
		dirs = append(dirs, map[string]any{"dir": dc.dir, "files": dc.files, "hits": dc.hits})
	}
	inv["dirs"] = dirs
	return inv
}

// fileInventoryWarning is the pointer appended to a sampled-lines warning.
const fileInventoryWarning = "Every matching file is named in the file list below with its hit count; path=<file> shows its lines."

// renderFileInventory prints the inventory: each directory once, then one
// "  Leaf.ext (hits)" line per file, then any directory rollup.
func renderFileInventory(b *strings.Builder, raw any) bool {
	inv, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	files := anySlice(inv["files"])
	total, _ := inv["total"].(int)
	complete, _ := inv["complete"].(bool)
	bestLines, _ := inv["bestLines"].(bool)
	switch {
	case bestLines && complete:
		fmt.Fprintf(b, "// all %d matching files, hits per file and the file's best match line:\n", total)
	case !complete:
		fmt.Fprintf(b, "// matching files found before the count deadline (%d; INCOMPLETE, more may exist), hits per file; the lines above are a SAMPLE:\n", total)
	case len(files) < total:
		fmt.Fprintf(b, "// all %d matching files: the %d with most hits, then the other %d by directory (path=<dir> lists them); the lines above are a SAMPLE:\n",
			total, len(files), total-len(files))
	default:
		fmt.Fprintf(b, "// all %d matching files, hits per file; the lines above are a SAMPLE:\n", total)
	}
	lastDir := "\x00"
	for _, rf := range files {
		entry, ok := rf.(map[string]any)
		if !ok {
			return false
		}
		file, _ := entry["file"].(string)
		dir, base := "", file
		if i := strings.LastIndexByte(file, '/'); i >= 0 {
			dir, base = file[:i+1], file[i+1:]
		}
		if dir != lastDir {
			if dir == "" {
				b.WriteString("./\n")
			} else {
				b.WriteString(dir + "\n")
			}
			lastDir = dir
		}
		if line, ok := entry["line"].(int); ok && line > 0 {
			fmt.Fprintf(b, "  %s (%v)  %d: %v\n", base, entry["hits"], line, entry["text"])
		} else {
			fmt.Fprintf(b, "  %s (%v)\n", base, entry["hits"])
		}
	}
	if dirs := anySlice(inv["dirs"]); len(dirs) > 0 {
		b.WriteString("// other matching files by directory:\n")
		for _, rd := range dirs {
			entry, ok := rd.(map[string]any)
			if !ok {
				return false
			}
			fmt.Fprintf(b, "  %v/ — %v files, %v hits\n", entry["dir"], entry["files"], entry["hits"])
		}
	}
	if more, ok := inv["moreDirs"].(map[string]any); ok {
		fmt.Fprintf(b, "  +%v more directories — %v files, %v hits\n", more["dirs"], more["files"], more["hits"])
	}
	return true
}

// textHitsFileCounts derives an exact inventory from a complete, retrieved
// hit set (every line is present, only the display was bounded).
func textHitsFileCounts(groups []any) []textsearch.FileCount {
	var out []textsearch.FileCount
	for _, raw := range groups {
		g, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		file, _ := g["file"].(string)
		if file == "" {
			continue
		}
		n := len(anySlice(g["hits"]))
		if more, ok := g["moreHits"].(int); ok {
			n += more
		}
		if n == 0 {
			n = len(anySlice(g["lines"]))
		}
		out = append(out, textsearch.FileCount{File: file, Count: n})
	}
	return out
}

// textHitsOmitFiles reports whether rendered text hits dropped whole files
// at the per-response file cap (renderTextMatches' "more files" note).
func textHitsOmitFiles(textHits any) bool {
	for _, raw := range anySlice(textHits) {
		g, ok := raw.(map[string]any)
		if !ok || g["file"] != nil {
			continue
		}
		if note, _ := g["note"].(string); strings.Contains(note, "more files with matches omitted") {
			return true
		}
	}
	return false
}
