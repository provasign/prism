package mcp

import (
	"regexp"
	"strings"
	"sync"
)

// Once-per-session notes.
//
// Measured on 261 real prism_search results (wide bed, 2026-09-05): 16.8% of
// all delivered bytes were envelope — `//` notes and headers — and the
// worst of it was verbatim repetition inside one session: "no matches —
// search completed (not truncated, not timed out)" 158 times (121 repeats),
// "locations only — prism_lookup <name> or prism_read for the body" 108
// times (85 repeats), a 300-char structural note for the same symbol up to
// ten times. A field report (BACKLOG, 2026-09-04) had called exactly this
// out and was closed on code inspection; the transcripts say otherwise.
//
// An instruction is worth its bytes once. After the first time a session
// sees a fixed note it gets the short form; a long note that repeats
// byte-for-byte collapses to a pointer at what was already said. Payload
// lines (hits, symbols, files) are never touched.

var onceFixed = map[string]string{
	"// no matches — search completed (not truncated, not timed out)":                        "// no matches",
	"// no symbol matches (full index checked, not a partial pass)":                          "// no symbol matches",
	"// locations only — prism_lookup <name> or prism_read for the body":                     "",
	"// ALL matches by enclosing symbol (graph rollup of the full set):":                     "// by enclosing symbol:",
	"// Grouped matches by enclosing symbol (bounded graph rollup; inspect omission notes):": "// bounded rollup by enclosing symbol; inspect omissions:",
	"// closest indexed symbols:":                                                            "// closest indexed symbols:",
	"// no matches — search timed out before finishing; results may be incomplete":           "// no matches — timed out, INCOMPLETE",
}

var oncePatterns = []struct {
	re    *regexp.Regexp
	short string
}{
	{regexp.MustCompile(`^// (\d+) more files with matches omitted — .*$`), "// $1 more files omitted (exhaustive=true lists them)"},
	{regexp.MustCompile(`^// no matches — search completed, not truncated, not timed out: .*$`), "// no matches (exact strings absent; broaden the term)"},
	{regexp.MustCompile(`^// showing (\d+) of AT LEAST (\d+) matches across (\d+) files — .*hitRollup.*$`), "// showing $1 of ≥$2 matches in $3 files — bounded rollup below; inspect omissions"},
	{regexp.MustCompile(`^// showing (\d+) of AT LEAST (\d+) matches across (\d+) files — .*$`), "// showing $1 of ≥$2 matches in $3 files — a SAMPLE; narrow, or exhaustive=true"},
	{regexp.MustCompile(`^// (\d+) files match — first .*$`), "// $1 files match — listed by path, then by directory (path=<dir> expands)"},
	{regexp.MustCompile(`^// (\d+) more files with matches — exhaustive=true: .*$`), "// $1 more files — by path, then by directory (path=<dir> expands)"},
}

// onceLongNote is the length from which a verbatim-repeated note collapses
// to a pointer. Structural notes and headlines run 200-500 chars.
const onceLongNote = 100

type onceNotes struct {
	mu   sync.Mutex
	said map[string]bool
}

// apply rewrites the rendered text: first occurrence of each note in the
// session is kept, later ones take the short form.
func (o *onceNotes) apply(text string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.said == nil {
		o.said = map[string]bool{}
	}
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		// Current validity/scope facts must survive repeats and compaction.
		lower := strings.ToLower(l)
		if strings.HasPrefix(l, "//") && (strings.HasPrefix(l, "// root:") ||
			strings.Contains(lower, "in the requested scope") ||
			strings.Contains(lower, "incomplete") || strings.Contains(lower, "not searched") ||
			strings.Contains(lower, "a sample") || strings.Contains(lower, "exhaustive symbol search") ||
			strings.Contains(lower, "rejected paths")) {
			out = append(out, l)
			continue
		}
		if !strings.HasPrefix(l, "//") {
			out = append(out, l)
			continue
		}
		if short, ok := onceFixed[l]; ok {
			if o.said[l] {
				if short != "" {
					out = append(out, short)
				}
				continue
			}
			o.said[l] = true
			out = append(out, l)
			continue
		}
		matched := false
		for _, p := range oncePatterns {
			if p.re.MatchString(l) {
				matched = true
				key := p.re.String()
				if o.said[key] {
					out = append(out, p.re.ReplaceAllString(l, p.short))
				} else {
					o.said[key] = true
					out = append(out, l)
				}
				break
			}
		}
		if matched {
			continue
		}
		if len(l) >= onceLongNote {
			if o.said[l] {
				head := l
				if len(head) > 70 {
					head = head[:70]
				}
				out = append(out, "// (as noted earlier) "+strings.TrimPrefix(head, "// ")+"…")
				continue
			}
			o.said[l] = true
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
