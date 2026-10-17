package mcp

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// searchShapeMode selects the grep-shaped default for compact search answers.
// "grep" turns it on; anything else leaves the answer exactly as before. It
// is set at build time (-ldflags "-X .../internal/mcp.searchShapeMode=grep")
// or with PRISM_SEARCH_SHAPE=grep, so one binary can be measured against the
// shipped default without a behaviour change for anyone else.
//
// Why (2026-10-17, isolated issue42 run, 41 pairs): a prism search call cost
// about 3x a native grep call (median 5.5 KB vs 0.8 KB) at nearly equal call
// counts, and that premium was the whole prism-vs-native task-cost gap on bug
// fixes. The mode keeps what grep gives (every matched line, its file, its
// enclosing symbol, the exact-match count) and drops what the agent can ask
// for: ranked anchors, the root and scope echoes, the symbol-signature block
// beside text hits, the default context lines, and bodies on broad matches.
// Opt-ins are untouched: context=N brings the windows back, include_bodies
// brings the bodies back, and every warning line is kept.
var searchShapeMode = ""

func grepShapeEnabled() bool {
	return searchShapeMode == "grep" || os.Getenv("PRISM_SEARCH_SHAPE") == "grep"
}

// grepShapeBodyMatchLimit: bodies stay on narrow queries, where the replay
// of 106 recorded body deliveries found them used most.
const grepShapeBodyMatchLimit = 3

var (
	grepExactMatches = regexp.MustCompile(`^// COMPLETE — (\d+) exact matches`)
	grepHitLine      = regexp.MustCompile(`^\s+\d+:`)
	grepContextLine  = regexp.MustCompile(`^\s+\d+- `)
	grepBodyHeader   = regexp.MustCompile(`^// (Exact source for|Full enclosing body)`)
	grepSymbolsHead  = regexp.MustCompile(`^symbols \(\d+\):$`)
)

// grepShapeSearchText rewrites a rendered compact search answer into the
// grep-shaped default. args are the legacy search arguments the agent sent.
func grepShapeSearchText(text string, args map[string]any) string {
	lines := strings.Split(text, "\n")
	_, askedContext := args["context"]
	includeBodies, _ := args["include_bodies"].(bool)

	hasHits := false
	maxExact := 0
	for _, l := range lines {
		if grepHitLine.MatchString(l) {
			hasHits = true
		}
		if m := grepExactMatches.FindStringSubmatch(l); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > maxExact {
				maxExact = n
			}
		}
	}
	dropBodies := !includeBodies && !askedContext && maxExact > grepShapeBodyMatchLimit

	out := make([]string, 0, len(lines))
	inBody, inAnchors, inSymbols := false, false, false
	for _, l := range lines {
		if grepBodyHeader.MatchString(l) {
			if dropBodies {
				inBody = true
			}
		}
		if inBody {
			continue
		}
		// Ranked anchors: a hint block of "//   file:line  [..]" lines.
		if strings.HasPrefix(l, "// First source anchors") {
			inAnchors = true
			continue
		}
		if inAnchors {
			if strings.HasPrefix(l, "//   ") {
				continue
			}
			inAnchors = false
		}
		if strings.HasPrefix(l, "// No indexed-name or cross-term source anchor") {
			continue
		}
		if hasHits && strings.HasPrefix(l, "// root:") {
			continue
		}
		if strings.HasPrefix(l, "// match lists restricted to") && !strings.Contains(l, "NOT searched") {
			continue
		}
		if strings.HasPrefix(l, "// locator result") {
			continue
		}
		// The symbols block beside text hits: matched lines already carry
		// the enclosing symbol, and lookup/read give signatures and bodies.
		if hasHits && grepSymbolsHead.MatchString(l) {
			inSymbols = true
			continue
		}
		if inSymbols {
			if strings.HasPrefix(l, "  ") && !grepHitLine.MatchString(l) && !grepContextLine.MatchString(l) && !strings.HasPrefix(l, "//") {
				continue
			}
			inSymbols = false
		}
		if !askedContext && !includeBodies && (grepContextLine.MatchString(l) || strings.TrimSpace(l) == "--") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}
