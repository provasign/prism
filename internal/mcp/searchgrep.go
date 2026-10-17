package mcp

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// searchShapeMode selects the shape of compact search answers. The default
// ("") is the grep shape; "legacy" restores the previous, fuller answer. The
// environment variable PRISM_SEARCH_SHAPE (legacy|grep) overrides it, and a
// build can set it with -ldflags "-X .../internal/mcp.searchShapeMode=legacy".
//
// Why (2026-10-17, isolated issue42 runs, 41 pairs): a prism search call cost
// about 3x a native grep call (median 5.5 KB vs 0.8 KB) at nearly equal call
// counts, and that premium was the whole prism-vs-native task-cost gap on bug
// fixes. The grep shape keeps what grep gives (every matched line, its file,
// its enclosing symbol, the exact-match count, every warning) and drops what
// the agent can ask for: ranked anchors, the root and scope echoes, the
// symbol-signature block beside text hits, the default context lines, and
// bodies on wide matches. Opt-ins are untouched: context=N brings the
// windows back, include_bodies brings the bodies back. Measured against the
// legacy shape on the same bed: task cost 1.107x -> 1.061x of native, solves
// 23 -> 25 of 41 (inside run-to-run noise, direction consistent with a
// 514-result replay that lost no matched line).
var searchShapeMode = ""

func grepShapeEnabled() bool {
	switch strings.ToLower(os.Getenv("PRISM_SEARCH_SHAPE")) {
	case "legacy":
		return false
	case "grep":
		return true
	}
	return searchShapeMode != "legacy"
}

// grepShapeBodyMatchLimit: a broad body pick stays up to this many exact matches.
// The replay of 106 recorded body deliveries found bodies unused in all 7 answers
// over 20 matches and about break-even between 4 and 20, so only wide answers drop.
// of 106 recorded body deliveries found them used most.
const grepShapeBodyMatchLimit = 20

var (
	grepExactMatches = regexp.MustCompile(`^// COMPLETE — (\d+) exact matches`)
	grepHitLine      = regexp.MustCompile(`^\s+\d+:`)
	grepContextLine  = regexp.MustCompile(`^\s+\d+- `)
	// Broad body sections: the evidence-ranked picks over many hits. A body
	// delivered for one unique symbol, or for a small complete result, is the
	// answer itself and stays.
	grepBroadBody   = regexp.MustCompile(`^// Exact source for (bounded enclosing hits|top hits)`)
	grepSymbolsHead = regexp.MustCompile(`^symbols \(\d+\):$`)
	grepScopeEcho   = regexp.MustCompile(`^// match lists restricted to [^;]+; other files were not searched$`)
)

// grepShapeSearchText rewrites a rendered compact search answer into the
// grep-shaped default. args are the legacy search arguments the agent sent.
func grepShapeSearchText(text string, args map[string]any) string {
	lines := strings.Split(text, "\n")
	_, askedContext := args["context"]
	_, askedScope := args["scope"]
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
		if dropBodies && grepBroadBody.MatchString(l) {
			inBody = true
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
		// Only the plain echo of the agent's own path/glob filters goes. A
		// scope note that says anything more ("text search was not run",
		// rejected paths, unindexed files) is information and stays.
		if grepScopeEcho.MatchString(l) {
			continue
		}
		// The symbols block beside text hits: matched lines already carry
		// the enclosing symbol, and lookup/read give signatures and bodies.
		// An explicit scope (both, symbols) asked for that block; keep it.
		if hasHits && !askedScope && grepSymbolsHead.MatchString(l) {
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
