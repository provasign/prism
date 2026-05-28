// Package config loads Prism configuration from prism.yaml and environment.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Config holds the resolved Prism configuration.
type Config struct {
	GroveURL          string
	GroveBinary       string
	// Model is the active AI model identifier. Empty means "auto" — Prism
	// will use whatever is reported by the MCP client at initialize time or
	// passed per-call. When empty, ContextWindow() returns a safe 200k
	// default that covers all current production models.
	Model             string
	Profile           string
	EmbeddingsBackend string // "tfidf" (only backend implemented today)
	MaxCacheFiles     int
	Port              int
}

// Default returns config values with environment overrides applied.
// Model intentionally defaults to "" (auto-detect at runtime).
func Default() *Config {
	c := &Config{
		GroveURL:          envOr("PRISM_GROVE_URL", "http://localhost:7777"),
		GroveBinary:       envOr("PRISM_GROVE_BINARY", "grove"),
		Model:             envOr("PRISM_MODEL", ""), // "" = auto
		Profile:           envOr("PRISM_PROFILE", "default"),
		EmbeddingsBackend: envOr("PRISM_EMBEDDINGS_BACKEND", "tfidf"),
		MaxCacheFiles:     50000,
		Port:              8888,
	}
	return c
}

// LoadFromDir looks for prism.yaml in dir and merges over Default().
// Parser is intentionally minimal: KEY: VALUE per line, comments with '#'.
// This keeps Prism single-binary with no YAML dep.
func LoadFromDir(dir string) (*Config, error) {
	c := Default()
	path := filepath.Join(dir, "prism.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		switch k {
		case "grove_url":
			c.GroveURL = v
		case "grove_binary":
			c.GroveBinary = v
		case "model":
			c.Model = v
		case "profile":
			c.Profile = v
		}
	}
	return c, nil
}

// ContextWindow returns the total context window in tokens for the model.
// When Model is empty or unrecognised, returns a safe 200k default that is
// correct for all current Claude models and generous for others.
func (c *Config) ContextWindow() int {
	return ModelContextWindow(c.Model)
}

// WithModel returns a shallow copy of the config with Model overridden.
// Use this for per-call effective context windows without mutating state.
func (c *Config) WithModel(model string) *Config {
	if model == "" {
		return c
	}
	cp := *c
	cp.Model = model
	return &cp
}

// ModelContextWindow maps known model name substrings to their context window.
// Matching is case-insensitive prefix/substring to handle versioned IDs like
// "claude-sonnet-4-6" or "gpt-4o-2024-11-20" without exhaustive enumeration.
func ModelContextWindow(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		// Unknown / auto — assume 200k (safe for all current Claude models).
		return 200000
	case strings.Contains(m, "gemini-1.5") || strings.Contains(m, "gemini-2"):
		return 1000000
	case strings.Contains(m, "gemini"):
		return 128000
	case strings.Contains(m, "claude"):
		// All current Claude models (claude-3-*, claude-opus-4*, claude-sonnet-4*,
		// claude-haiku-4*) share a 200k context window.
		return 200000
	case strings.Contains(m, "gpt-4o") || strings.Contains(m, "gpt-4-turbo") ||
		strings.Contains(m, "o1") || strings.Contains(m, "o3"):
		return 128000
	case strings.Contains(m, "gpt-4"):
		return 128000
	case strings.Contains(m, "gpt-3"):
		return 16000
	default:
		// Unknown model — conservative 128k avoids context overflow.
		return 128000
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
