// Package sensitive names the files whose contents Prism does not hand to
// an agent unless asked: credentials, keys, environment files (SEC-03).
// A search still reports a match in them (path and line), so a text search
// keeps ripgrep's semantics; only the line text is withheld.
package sensitive

import (
	"path"
	"strings"
	"sync"
)

// Defaults are matched against the base name, case-insensitively.
var Defaults = []string{
	".env", ".env.*", "*.env", "*.pem", "*.key", "id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*",
	"*.p12", "*.pfx", "*.jks", "*.keystore", ".npmrc", ".pypirc", ".netrc", "_netrc",
	"credentials", "credentials.*", "secrets.*", "*.secret", ".htpasswd", "*.kdbx",
}

var (
	mu    sync.RWMutex
	extra []string
)

// Add appends patterns from prism.yaml (sensitive_paths:). Additive only:
// a repository cannot remove a default.
func Add(patterns ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			extra = append(extra, p)
		}
	}
}

// Match reports whether a repository path is sensitive. Patterns without
// a slash match the base name; patterns with one match the whole path.
func Match(rel string) bool {
	rel = strings.ToLower(strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/"))
	base := path.Base(rel)
	if base == ".env.example" || base == ".env.sample" || base == ".env.template" {
		return false // templates carry no secrets by convention
	}
	mu.RLock()
	defer mu.RUnlock()
	for _, list := range [][]string{Defaults, extra} {
		for _, p := range list {
			p = strings.ToLower(p)
			target := base
			if strings.Contains(p, "/") {
				target = rel
			}
			if ok, _ := path.Match(p, target); ok {
				return true
			}
		}
	}
	return false
}

// Withheld is the text that replaces a sensitive line or file.
const Withheld = "[withheld: sensitive file; --include-sensitive (MCP include_sensitive=true) shows it]"
