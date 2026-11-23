// Package config loads Prism configuration from prism.yaml and environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Config holds the resolved Prism configuration. It is the one parser of
// prism.yaml (CON-08): init's own keys (version, harnesses, mcp_surface,
// interface) are read here too, not by ad hoc line scanners.
type Config struct {
	// Version, Harnesses, MCPSurface and Interface are written by prism init.
	Version    string
	Harnesses  string
	MCPSurface string
	Interface  string
	// Model is the active AI model identifier. There is NO auto-detection —
	// the MCP initialize handshake does not carry the model — so it is set
	// only via prism.yaml, PRISM_MODEL, or a per-call model= argument. When
	// empty, ContextWindow() returns a safe 200k default that covers all
	// current production models.
	Model         string
	Profile       string
	MaxCacheFiles int
	// Port is prism serve's default port, from `port:` (0 = 8888).
	Port int
	// ArchDeny holds declared architecture rules, one per `arch_deny:` line
	// in prism.yaml: "<from> -> <to>" where each side is a component name
	// (directory), a prefix (covers subdirectories), a glob, or "*".
	// Validated by prism arch / prism_arch_check against the induced
	// component view; every violation cites concrete file:line sites.
	ArchDeny []string
	// WorkspaceRepos lists other repositories linked to this one, one per
	// `workspace_repo: <id>=<path>` line. Path is as written: relative to the
	// directory holding this prism.yaml, or absolute.
	WorkspaceRepos []WorkspaceRepo
	// WorkspaceServer is the shared link server, from `workspace_server:`.
	WorkspaceServer string
	// CIFailOn lists the finding kinds that fail `prism ci`, from
	// `ci_fail_on: missed_sites, arch` (default both).
	CIFailOn []string
	// CIComment: the CI action posts one sticky PR comment, `ci_comment:`.
	CIComment bool
	// SensitivePaths adds patterns to the files whose text is withheld from
	// search and read, from `sensitive_paths: a, b` (additive only).
	SensitivePaths []string

	// Sources maps each key set by prism.yaml or the environment to where
	// it came from ("prism.yaml:<line>" or "env PRISM_X").
	Sources map[string]string
	// Problems lists unknown keys and invalid values, one message each.
	Problems []string
}

// knownKeys are the keys prism.yaml may hold.
var knownKeys = []string{
	"version", "harnesses", "mcp_surface", "interface", "model", "profile", "port",
	"arch_deny", "workspace_repo", "workspace_server", "ci_fail_on", "ci_comment", "sensitive_paths",
}

// retiredKeys are accepted and ignored, so old files load without a warning.
var retiredKeys = map[string]string{
	"agent_mode":   "init stopped writing it in v0.38.0",
	"grove_url":    "Grove is embedded",
	"grove_binary": "Grove is embedded",
}

// WorkspaceRepo is one `workspace_repo:` entry.
type WorkspaceRepo struct {
	ID   string
	Path string
}

// Default returns config values with environment overrides applied.
// Model intentionally defaults to "" (unset — 200k-window default applies).
func Default() *Config {
	c := &Config{
		Model:         envOr("PRISM_MODEL", ""), // "" = unset (no auto-detection)
		Profile:       envOr("PRISM_PROFILE", "default"),
		MaxCacheFiles: 50000,
		Sources:       map[string]string{},
	}
	for key, env := range map[string]string{"model": "PRISM_MODEL", "profile": "PRISM_PROFILE"} {
		if os.Getenv(env) != "" {
			c.Sources[key] = "env " + env
		}
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
	parseInto(c, data)
	return c, nil
}

// LoadFile parses one prism.yaml path; a missing file gives the defaults.
func LoadFile(path string) *Config {
	c := Default()
	if data, err := os.ReadFile(path); err == nil {
		parseInto(c, data)
	}
	return c
}

// QuietProblems stops WarnProblems; doctor and config report problems in
// their own output.
var QuietProblems bool

// WarnProblems prints each problem once per process to stderr.
func (c *Config) WarnProblems() {
	if QuietProblems {
		return
	}
	for _, p := range c.Problems {
		if !warned[p] {
			warned[p] = true
			fmt.Fprintln(os.Stderr, "prism.yaml:", p)
		}
	}
}

var warned = map[string]bool{}

// Keys lists every known key with its resolved value and source
// ("default" when neither prism.yaml nor the environment set it).
func (c *Config) Keys() [][3]string {
	val := map[string]string{
		"version": c.Version, "harnesses": c.Harnesses, "mcp_surface": c.MCPSurface,
		"interface": c.Interface, "model": c.Model, "profile": c.Profile,
		"port": "", "arch_deny": strings.Join(c.ArchDeny, "; "),
		"workspace_server": c.WorkspaceServer, "ci_comment": strconv.FormatBool(c.CIComment),
		"sensitive_paths": strings.Join(c.SensitivePaths, ", "),
	}
	if c.Port != 0 {
		val["port"] = strconv.Itoa(c.Port)
	}
	if c.CIFailOn != nil {
		val["ci_fail_on"] = strings.Join(c.CIFailOn, ", ")
	}
	var repos []string
	for _, r := range c.WorkspaceRepos {
		repos = append(repos, r.ID+"="+r.Path)
	}
	val["workspace_repo"] = strings.Join(repos, "; ")
	out := make([][3]string, 0, len(knownKeys))
	for _, k := range knownKeys {
		src := c.Sources[k]
		if src == "" {
			src = "default"
		}
		out = append(out, [3]string{k, val[k], src})
	}
	return out
}

// nearestKey returns the known key closest to k by edit distance, or "".
func nearestKey(k string) string {
	best, bestD := "", 4
	for _, cand := range knownKeys {
		if d := editDistance(k, cand); d < bestD {
			best, bestD = cand, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Parse reads prism.yaml content (for a revision other than the working
// tree: prism ci reads its settings from the base commit).
func Parse(data []byte) *Config {
	c := Default()
	parseInto(c, data)
	return c
}

func parseInto(c *Config, data []byte) {
	if c.Sources == nil {
		c.Sources = map[string]string{}
	}
	fileSet := map[string]bool{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			c.Problems = append(c.Problems, fmt.Sprintf("line %d: not a key: value line: %q", n+1, line))
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		if _, retired := retiredKeys[k]; !retired && !fileSet[k] {
			// A repeatable key (arch_deny, workspace_repo) names its first line.
			c.Sources[k] = fmt.Sprintf("prism.yaml:%d", n+1)
			fileSet[k] = true
		}
		switch k {
		case "version":
			c.Version = stripComment(v)
		case "harnesses":
			c.Harnesses = stripComment(v)
		case "mcp_surface":
			c.MCPSurface = stripComment(v)
		case "interface":
			c.Interface = stripComment(v)
		case "port":
			p, err := strconv.Atoi(stripComment(v))
			if err != nil || p < 1 || p > 65535 {
				c.Problems = append(c.Problems, fmt.Sprintf("line %d: port %q is not 1..65535", n+1, v))
				delete(c.Sources, k)
				continue
			}
			c.Port = p
		case "grove_url", "grove_binary":
			// Retired: Grove is embedded in-process.
		case "model":
			c.Model = v
		case "profile":
			c.Profile = v
		case "agent_mode":
			// Vestigial: init stopped writing this in v0.38.0 and nothing has
			// ever consumed it at runtime. Accepted and discarded so existing
			// prism.yaml files stay loadable; not stored.
		case "arch_deny":
			// Repeatable. Inline comments allowed after '#'.
			if j := strings.IndexByte(v, '#'); j >= 0 {
				v = strings.TrimSpace(v[:j])
			}
			if v != "" {
				c.ArchDeny = append(c.ArchDeny, v)
			}
		case "workspace_repo":
			// Repeatable: `<id>=<path>`, or a bare path whose last element is
			// the id. Inline comments allowed after " #".
			if j := strings.Index(v, " #"); j >= 0 {
				v = strings.Trim(strings.TrimSpace(v[:j]), `"'`)
			}
			id, p := "", v
			if j := strings.IndexByte(v, '='); j >= 0 {
				id, p = strings.TrimSpace(v[:j]), strings.TrimSpace(v[j+1:])
			}
			if p == "" {
				continue
			}
			if id == "" {
				id = filepath.Base(filepath.Clean(p))
			}
			c.WorkspaceRepos = append(c.WorkspaceRepos, WorkspaceRepo{ID: id, Path: p})
		case "workspace_server":
			c.WorkspaceServer = v
		case "ci_fail_on":
			if j := strings.IndexByte(v, '#'); j >= 0 {
				v = v[:j]
			}
			c.CIFailOn = nil
			for _, k := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '[' || r == ']' }) {
				c.CIFailOn = append(c.CIFailOn, strings.Trim(k, `"'`))
			}
			if c.CIFailOn == nil {
				c.CIFailOn = []string{}
			}
		case "sensitive_paths":
			if j := strings.IndexByte(v, '#'); j >= 0 {
				v = v[:j]
			}
			for _, k := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '[' || r == ']' }) {
				c.SensitivePaths = append(c.SensitivePaths, strings.Trim(k, `"'`))
			}
		case "ci_comment":
			switch stripComment(v) {
			case "true", "yes", "1":
				c.CIComment = true
			case "false", "no", "0", "":
				c.CIComment = false
			default:
				c.Problems = append(c.Problems, fmt.Sprintf("line %d: ci_comment %q is not true or false", n+1, v))
				delete(c.Sources, k)
			}
		default:
			delete(c.Sources, k)
			msg := fmt.Sprintf("line %d: unknown key %q (ignored)", n+1, k)
			if near := nearestKey(k); near != "" {
				msg += fmt.Sprintf("; did you mean %q?", near)
			}
			c.Problems = append(c.Problems, msg)
		}
	}
	for _, k := range c.CIFailOn {
		if k != "missed_sites" && k != "arch" {
			c.Problems = append(c.Problems, fmt.Sprintf("ci_fail_on: unknown kind %q (missed_sites, arch)", k))
		}
	}
	sort.Strings(c.Problems)
}

// stripComment drops an inline " # comment" and surrounding quotes.
func stripComment(v string) string {
	if j := strings.Index(v, " #"); j >= 0 {
		v = v[:j]
	}
	return strings.Trim(strings.TrimSpace(v), `"'`)
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
