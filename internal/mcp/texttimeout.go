package mcp

import (
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/textsearch"
)

// textTimeoutWarning says a text scan ran out of time, and why when Prism
// can tell. The scope=text path used to blame a missing ripgrep on every
// timeout, rg or not (2026-10-13: a data-collection repo with rg installed
// timed out and the advice pointed at the wrong fix). A timeout on a normal
// source tree is rare (grafana, 21k files: 0.2s), so it usually means bulk
// text — data exports, vendored copies — that the project can exclude.
func textTimeoutWarning(r textsearch.Result) string {
	msg := "INCOMPLETE text scan: deadline reached; absence is not established."
	if r.Backend == "native" {
		msg += " The built-in scanner is in use because neither ripgrep nor grep was found on PATH; installing ripgrep makes text search much faster."
	}
	if len(r.HeavyDirs) > 0 {
		parts := make([]string, 0, len(r.HeavyDirs))
		for _, d := range r.HeavyDirs {
			parts = append(parts, fmt.Sprintf("%s (%d files, %s)", d.Dir, d.Files, humanBytes(d.Bytes)))
		}
		msg += " Most searched text: " + strings.Join(parts, ", ") +
			". If any are data, exports or copies, list them in .groveignore: Prism then skips them in the index and in text search."
	}
	return msg
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
