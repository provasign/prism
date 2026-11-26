package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// ("file.go::Name@abc123"), leaving the stable "file.go::Name" identity.

// readRange delivers an explicit line window, verbatim and line-numbered —
// the shape `sed -n A,Bp` and native Read(offset,limit) produce, which
// agents reach for on half their reads.
//
// totalLines is always reported: a window without a denominator reads as the
// whole file, which is the silent-narrowing failure this codebase keeps
// re-learning. Out-of-range requests clamp and say so rather than erroring —
// a tool that errors is a tool agents route around.
func (h *Handler) readRange(sessionPath, content, hash string, offset, limit int, force bool) (any, error) {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // trailing newline is not a line
	}
	total := len(lines)
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return map[string]any{
			"file": sessionPath, "totalLines": total, "delivery": "range",
			"warning": fmt.Sprintf("offset %d is past end of file (%d lines); nothing to show",
				offset, total),
		}, nil
	}
	end := total
	if limit > 0 && offset-1+limit < total {
		end = offset - 1 + limit
	}
	if !force && h.deliveredRangeCovered(sessionPath, hash, offset, end) {
		return map[string]any{
			"file": sessionPath, "delivery": "range", "startLine": offset,
			"endLine": end, "totalLines": total,
			"content": fmt.Sprintf("// [prism:cached] %s lines %d-%d — previously delivered; if unavailable, repeat read with force:true\n",
				sessionPath, offset, end),
		}, nil
	}
	var b strings.Builder
	width := len(strconv.Itoa(end))
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%*d"+SourceLineSep+"%s\n", width, i+1, lines[i])
	}
	out := map[string]any{
		"file":       sessionPath,
		"delivery":   "range",
		"startLine":  offset,
		"endLine":    end,
		"totalLines": total,
		"content":    b.String(),
	}
	if end < total || offset > 1 {
		out["note"] = fmt.Sprintf("lines %d-%d of %d — this is a WINDOW, not the file",
			offset, end, total)
	}
	h.recordDeliveredRanges(sessionPath, hash, []lineWindow{{start: offset, end: end}})
	return out, nil
}

func (h *Handler) toolRead(ctx context.Context, args map[string]any) (any, error) {
	path := stringArg(args, "file", stringArg(args, "path", ""))
	if path == "" {
		return nil, errors.New("file is required")
	}
	task := stringArg(args, "task", "")
	abs, sessionPath, err := h.safeReadPath(path)
	if err != nil {
		return nil, err
	}
	offset, limit := intArg(args, "offset", 0), intArg(args, "limit", 0)
	from, to := 0, 0
	if offset > 0 || limit > 0 {
		from = offset
		if from < 1 {
			from = 1
		}
		if limit > 0 {
			to = from + limit - 1
		}
	}
	g, err := readGuarded(abs, normalizePath(sessionPath), boolArg(args, "include_sensitive"), from, to)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return missingReadResult(sessionPath), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if g.refused != "" {
		return refusedReadResult(sessionPath, g), nil
	}
	data := g.data
	// Line-ranged reads. Measured over 374 real file reads by unaided and
	// prism-armed agents: 50.6% are line-ranged (sed -n A,Bp 25.7%, native
	// Read offset+limit 23.8%) and prism_read could express NONE of it. That
	// gap is why 87% of the reads prism never saw were ranged, and why its
	// session ledger fired 0 times in 45 calls -- the agent cannot route a
	// ranged read through a whole-file tool, so the ledger never sees the
	// repeats it exists to collapse.
	//
	// Whole-file reads keep the compression path unchanged; a ranged read is
	// delivered verbatim and line-numbered, because slicing lines and THEN
	// compressing them would be two lossy steps on the same content.
	if offset > 0 || limit > 0 {
		out, err := h.readRange(sessionPath, string(data), compression.Hash(string(data)), offset, limit, boolArg(args, "force"))
		if m, ok := out.(map[string]any); ok && g.note != "" {
			m["note"] = g.note
			if g.totalLines > 0 {
				m["totalLines"] = g.totalLines
			}
		}
		return out, err
	}
	// The file's currently indexed symbols, by exact path (Grove v0.6.1).
	fileSyms, err := h.Grove.FileSymbols(ctx, normalizePath(sessionPath))
	if err != nil {
		return nil, fmt.Errorf("grove symbols: %w", err)
	}
	readCfg := h.Cfg.WithModel(stringArg(args, "model", ""))
	contextUsed := int64(intArg(args, "context_used", 0))
	confidence := session.Low
	if entry, seen, _ := h.Session.Lookup(sessionPath, ""); seen {
		confidence = h.confidenceFor(entry, contextUsed, readCfg.ContextWindow())
	}
	// HOST-CAP DEGRADATION (BACKLOG addendum 2 item 17). Claude Code
	// rejects MCP results past ~25k tokens; a whole-file delivery over
	// that bound never reaches the agent — it sees only "exceeds maximum
	// allowed tokens" and, measured (71o4q969, 2026-09-03), never retries
	// prism_read on ANY file again (50 native Reads followed). For files
	// whose body CANNOT be delivered there is no policy question — the
	// choice is an error the agent routes around forever, or a valid
	// partial. Deliver the head window plus the file's symbol map with
	// explicit continuation instructions. Files under the bound are
	// untouched (this deliberately does NOT reopen the reverted
	// outline/map delivery-shaping work, which concerned deliverable
	// files).
	const hostCapTokens = 20000 // safety margin under the host's ~25k
	if ranking.EstimateTokens(string(data)) > hostCapTokens {
		lines := strings.Split(string(data), "\n")
		const headLines = 400
		head := lines
		if len(head) > headLines {
			head = head[:headLines]
		}
		var b strings.Builder
		for i, l := range head {
			fmt.Fprintf(&b, "%d"+SourceLineSep+"%s\n", i+1, l)
		}
		symMap := make([]map[string]any, 0, len(fileSyms))
		for _, s := range fileSyms {
			symMap = append(symMap, map[string]any{
				"name": displayQN(s), "kind": s.Kind,
				"lines": fmt.Sprintf("%d-%d", s.Span.Start, s.Span.End),
			})
		}
		out := map[string]any{
			"file":       sessionPath,
			"strategy":   "head+map (file exceeds the deliverable result cap)",
			"totalLines": len(lines),
			"content":    b.String(),
			"symbols":    symMap,
			"note": fmt.Sprintf("this file is %d lines (~%d tokens) — larger than one tool "+
				"result can carry. Delivered: lines 1-%d plus the complete symbol map. "+
				"Continue with prism_read(file, offset=%d, limit=400), or fetch one body "+
				"with prism_lookup(name) — do NOT fall back to native Read, it has the "+
				"same size limit.",
				len(lines), ranking.EstimateTokens(string(data)), len(head), len(head)+1),
		}
		if len(fileSyms) > 0 {
			h.setDriftBase(sessionPath, fileSyms)
		}
		return out, nil
	}
	readSession := h.Session
	if boolArg(args, "force") {
		readSession = nil
	}
	res := compression.CompressFileRead(sessionPath, string(data), compression.Options{
		Task:            task,
		Symbols:         fileSyms,
		Session:         readSession,
		Ledger:          h.Ledger,
		TokenLedgerName: "prism_read",
		Confidence:      confidence,
		ContextUsed:     contextUsed,
	})
	if boolArg(args, "force") && h.Session != nil {
		h.Session.Record(sessionPath, compression.Hash(string(data)), int64(res.DeliveredTokens), res.Strategy)
		h.Session.RecordContextUsed(sessionPath, contextUsed)
	}
	// Record the structural baseline for prism_drift: these are the symbols
	// the agent's copy of the file reflects as of this delivery.
	if len(fileSyms) > 0 {
		h.setDriftBase(sessionPath, fileSyms)
	}
	return map[string]any{
		"file":            res.FilePath,
		"strategy":        res.Strategy,
		"originalTokens":  res.OriginalTokens,
		"deliveredTokens": res.DeliveredTokens,
		"savingsPercent":  res.SavingsPercent,
		"content":         res.Content,
	}, nil
}

const compactReadTotalLines = 600

func missingReadResult(path string) map[string]any {
	return map[string]any{
		"file": path, "found": false,
		"warning": "file not found; check the path or search for the file before retrying",
	}
}

func (h *Handler) readRanges(ctx context.Context, args map[string]any) (any, error) {
	raw := args["ranges"].([]any) // compact argument validation ran before dispatch
	type window struct {
		file, content, hash string
		from, limit         int
		warning             string
		missing             bool
		// budgeted windows share the line and byte budgets below.
		budgeted    bool
		lines       []string
		index       int
		path        string
		requestedTo int
	}
	windows := make([]window, 0, len(raw))
	var notes []string
	var continuations []map[string]any
	largeTotals := map[string]int{} // a large file read in part: its real line count
	remainingLines, remainingBytes := compactReadTotalLines, 20*1024
	for i, entry := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := entry.(map[string]any)
		path := r["file"].(string)
		abs, sessionPath, err := h.safeReadPath(path)
		if err != nil {
			return nil, err
		}
		from, _ := r["from"].(int)
		if f, ok := r["from"].(float64); ok {
			from = int(f)
		}
		to, _ := r["to"].(int)
		if t, ok := r["to"].(float64); ok {
			to = int(t)
		}
		g, err := readGuarded(abs, normalizePath(sessionPath), boolArg(args, "include_sensitive"), from, to)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				windows = append(windows, window{file: sessionPath, missing: true})
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if g.refused != "" {
			// Not a regular file, binary, or sensitive (SEC-03/04).
			windows = append(windows, window{file: sessionPath, warning: sessionPath + ": " + g.refused})
			continue
		}
		if g.note != "" {
			notes = append(notes, sessionPath+": "+g.note)
		}
		if g.totalLines > 0 {
			largeTotals[sessionPath] = g.totalLines
		}
		data := g.data
		content := string(data)
		lines := strings.Split(content, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		from, requestedTo := intArg(r, "from", 0), intArg(r, "to", 0)
		w := window{file: sessionPath, content: content, hash: compression.Hash(content), from: from, index: i, path: path}
		if from > len(lines) {
			w.limit = 1 // readRange will report the past-EOF warning
			windows = append(windows, w)
			continue
		}
		requestedLines := requestedTo - from + 1
		availableLines := minInt(requestedLines, len(lines)-from+1)
		if !boolArg(args, "force") {
			cachedLines := minInt(availableLines, compactReadLimit)
			if cachedLines > 0 && h.deliveredRangeCovered(sessionPath, w.hash, from, from+cachedLines-1) {
				w.limit = cachedLines
				if cachedLines < availableLines {
					notes = append(notes, fmt.Sprintf("range %d (%s:%d-%d) clamped to %d-%d",
						i+1, path, from, requestedTo, from, from+cachedLines-1))
					continuations = append(continuations, map[string]any{"file": path, "from": from + cachedLines, "to": from + availableLines - 1})
				}
				windows = append(windows, w)
				continue
			}
		}
		w.limit = minInt(requestedLines, minInt(compactReadLimit, len(lines)-from+1))
		w.lines = lines
		w.requestedTo = requestedTo
		w.budgeted = true
		windows = append(windows, w)
	}
	// The line and byte budgets go to the small windows in full first; what
	// they leave is shared by the rest (a 140-line window beside two short
	// ones was cut to 38 lines by an even split, costing a turn).
	var pending []int
	for j, w := range windows {
		if w.budgeted {
			pending = append(pending, j)
		}
	}
	cost := func(w *window, n int) int {
		c := 0
		for k := 0; k < n; k++ {
			c += len(w.lines[w.from-1+k]) + 16
		}
		return c
	}
	sort.SliceStable(pending, func(a, b int) bool {
		wa, wb := &windows[pending[a]], &windows[pending[b]]
		return cost(wa, wa.limit) < cost(wb, wb.limit)
	})
	for n, j := range pending {
		w := &windows[j]
		shareLines := remainingLines / (len(pending) - n)
		shareBytes := remainingBytes / (len(pending) - n)
		w.limit = minInt(w.limit, shareLines)
		for k := 0; k < w.limit; k++ {
			c := len(w.lines[w.from-1+k]) + 16
			if c > shareBytes {
				w.limit = k
				break
			}
			shareBytes -= c
			remainingBytes -= c
		}
		remainingLines -= w.limit
	}
	for j := range windows {
		w := &windows[j]
		if !w.budgeted {
			continue
		}
		i, path := w.index, w.path
		requestedLines := w.requestedTo - w.from + 1
		availableLines := minInt(requestedLines, len(w.lines)-w.from+1)
		if w.limit == 0 {
			w.warning = fmt.Sprintf("range %d (%s:%d-%d) omitted by the compact read budget", i+1, path, w.from, w.requestedTo)
			notes = append(notes, w.warning)
		} else if w.limit < requestedLines {
			notes = append(notes, fmt.Sprintf("range %d (%s:%d-%d) clamped to %d-%d",
				i+1, path, w.from, w.requestedTo, w.from, w.from+w.limit-1))
		}
		if w.limit < availableLines {
			continuations = append(continuations, map[string]any{"file": path, "from": w.from + w.limit, "to": w.from + availableLines - 1})
		}
	}
	results := make([]map[string]any, 0, len(windows))
	for _, w := range windows {
		if w.missing {
			results = append(results, missingReadResult(w.file))
			continue
		}
		if w.warning != "" {
			results = append(results, map[string]any{"file": w.file, "delivery": "range", "warning": w.warning})
			continue
		}
		result, err := h.readRange(w.file, w.content, w.hash, w.from, w.limit, boolArg(args, "force"))
		if err != nil {
			return nil, err
		}
		rm := result.(map[string]any)
		if n, ok := largeTotals[w.file]; ok {
			rm["totalLines"] = n
		}
		results = append(results, rm)
	}
	out := map[string]any{"delivery": "ranges", "ranges": results}
	if len(notes) > 0 {
		out["note"] = strings.Join(notes, "; ")
	}
	if len(continuations) > 0 {
		out["continuation"] = map[string]any{"op": "read", "args": map[string]any{"ranges": continuations}}
	}
	return out, nil
}
