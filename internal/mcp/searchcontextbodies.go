package mcp

import (
	"regexp"
	"strconv"

	"github.com/provasign/prism/internal/grove"
)

var (
	deliveredBodyFile = regexp.MustCompile("(?m)^\\*\\*`([^`]+)`\\*\\*$")
	deliveredSrcLine  = regexp.MustCompile(`(?m)^(\d+)` + regexp.QuoteMeta(SourceLineSep))
)

type deliveredSpan struct {
	file       string
	start, end int
}

// deliveredSourceSpans lists the file ranges a body section already prints:
// each **`file`** header followed by its line-numbered source, as one
// contiguous range per header (every section this package renders is
// contiguous). The headers' own span notes are not trusted over the lines
// actually printed, which can be a window of the span.
func deliveredSourceSpans(bodies string) []deliveredSpan {
	var spans []deliveredSpan
	headers := deliveredBodyFile.FindAllStringSubmatchIndex(bodies, -1)
	for i, m := range headers {
		file := bodies[m[2]:m[3]]
		sectionEnd := len(bodies)
		if i+1 < len(headers) {
			sectionEnd = headers[i+1][0]
		}
		span := deliveredSpan{file: file}
		for _, l := range deliveredSrcLine.FindAllStringSubmatch(bodies[m[1]:sectionEnd], -1) {
			n, _ := strconv.Atoi(l[1])
			if span.start == 0 || n < span.start {
				span.start = n
			}
			if n > span.end {
				span.end = n
			}
		}
		if span.start > 0 {
			spans = append(spans, span)
		}
	}
	return spans
}

// deliveredSpanSymbols turns printed ranges into the skip records the
// enclosing pass takes, so a later body never repeats source a section
// already printed (a pick's file section is wider than its symbol).
func deliveredSpanSymbols(bodies string) []grove.SymbolRecord {
	var syms []grove.SymbolRecord
	for _, s := range deliveredSourceSpans(bodies) {
		sym := grove.SymbolRecord{FilePath: s.file}
		sym.Span.Start, sym.Span.End = s.start, s.end
		syms = append(syms, sym)
	}
	return syms
}

// dropContextInsideBodies removes context lines that a delivered body or
// window already prints, so no source line appears twice in one answer. The
// match line itself stays as the locator. Context lines are numbered by their
// distance from the hit, so each side keeps only its run of lines next to
// the hit that no delivered span covers (a doc comment above a declaration
// survives; a neighbouring delivered function does not).
func dropContextInsideBodies(out map[string]any, bodies string) {
	spans := deliveredSourceSpans(bodies)
	if len(spans) == 0 {
		return
	}
	results := []map[string]any{out}
	if batch, ok := out["results"].([]map[string]any); ok {
		results = batch
	}
	for _, r := range results {
		for _, raw := range anySlice(r["textHits"]) {
			group, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := group["file"].(string)
			for _, rawHit := range anySlice(group["hits"]) {
				hit, ok := rawHit.(map[string]any)
				if !ok {
					continue
				}
				line := intArg(hit, "line", 0)
				covered := func(n int) bool {
					for _, s := range spans {
						if s.file == file && s.start <= n && n <= s.end {
							return true
						}
					}
					return false
				}
				before := anySlice(hit["before"])
				keep := 0
				for keep < len(before) && !covered(line-1-keep) {
					keep++
				}
				if keep == 0 {
					delete(hit, "before")
				} else if keep < len(before) {
					hit["before"] = before[len(before)-keep:]
				}
				after := anySlice(hit["after"])
				keep = 0
				for keep < len(after) && !covered(line+1+keep) {
					keep++
				}
				if keep == 0 {
					delete(hit, "after")
				} else if keep < len(after) {
					hit["after"] = after[:keep]
				}
			}
		}
	}
}
