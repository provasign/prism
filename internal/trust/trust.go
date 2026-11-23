// Package trust decides whether Prism may run a repository's own build
// tools while indexing it (SEC-01). Indexing a trusted repository runs its
// Maven or Gradle build, its own typescript, rust-analyzer (build scripts,
// proc macros) and a pinned go toolchain; an untrusted one runs none.
//
// The decision is per user, never per repository: the trust file lives in
// the user's config directory, so a hostile prism.yaml cannot grant itself
// trust. Default: trusted, with a one-time notice per repository.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// State values stored per root.
const (
	Trusted        = "trusted"
	Untrusted      = "untrusted"
	TrustedDefault = "trusted-default" // never asked; the notice was shown
)

var mu sync.Mutex

// override is set by --trusted/--untrusted on the running command.
var override string

// SetOverride applies a command-line --trusted or --untrusted.
func SetOverride(state string) { override = state }

// Path is the trust file: $XDG_CONFIG_HOME/prism/trusted.json, else
// ~/.config/prism/trusted.json.
func Path() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "prism", "trusted.json")
	}
	if testing.Testing() {
		// Test binaries index hundreds of temp repositories; they must
		// never write the user's own trust file.
		return filepath.Join(os.TempDir(), fmt.Sprintf("prism-test-trust-%d", os.Getpid()), "trusted.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "prism", "trusted.json")
}

type file struct {
	Roots map[string]string `json:"roots"`
}

func load() file {
	f := file{Roots: map[string]string{}}
	p := Path()
	if p == "" {
		return f
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &f)
		if f.Roots == nil {
			f.Roots = map[string]string{}
		}
	}
	return f
}

func save(f file) error {
	p := Path()
	if p == "" {
		return fmt.Errorf("no home directory for the trust file")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func key(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return filepath.Clean(root)
}

// Decision is the effective trust for a root and why.
type Decision struct {
	Untrusted bool
	Source    string // "--untrusted", "PRISM_UNTRUSTED", "trust file", "default"
}

// String is "trusted (default)" or "untrusted (PRISM_UNTRUSTED)".
func (d Decision) String() string {
	state := "trusted"
	if d.Untrusted {
		state = "untrusted"
	}
	return state + " (" + d.Source + ")"
}

// For returns the effective trust for root: flag, then PRISM_UNTRUSTED,
// then the trust file, then trusted.
func For(root string) Decision {
	switch override {
	case Untrusted:
		return Decision{true, "--untrusted"}
	case Trusted:
		return Decision{false, "--trusted"}
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("PRISM_UNTRUSTED"))); v == "1" || v == "true" || v == "yes" {
		return Decision{true, "PRISM_UNTRUSTED"}
	}
	mu.Lock()
	defer mu.Unlock()
	switch load().Roots[key(root)] {
	case Untrusted:
		return Decision{true, "trust file"}
	case Trusted:
		return Decision{false, "trust file"}
	}
	return Decision{false, "default"}
}

// Set records root as trusted or untrusted.
func Set(root, state string) error {
	mu.Lock()
	defer mu.Unlock()
	f := load()
	f.Roots[key(root)] = state
	return save(f)
}

// NoticeOnce returns the one-time notice for a root indexed with default
// trust, and records that it was shown. "" when the user decided already,
// or the root is untrusted.
func NoticeOnce(root string) string {
	d := For(root)
	if d.Untrusted || d.Source != "default" {
		return ""
	}
	mu.Lock()
	defer mu.Unlock()
	f := load()
	k := key(root)
	if _, seen := f.Roots[k]; seen {
		return ""
	}
	// Scratch checkouts under the temp directory (benchmark cells, CI) are
	// not recorded, so they cannot grow the file; they get the note each time.
	if !underTemp(k) {
		f.Roots[k] = TrustedDefault
		_ = save(f)
	}
	return "prism: indexing " + k + " runs this repository's own build tools (its Maven or Gradle build, its typescript, rust-analyzer build scripts and proc macros, a pinned go toolchain).\n" +
		"       For a repository you do not trust, run `prism untrust " + k + "` (or set PRISM_UNTRUSTED=1); `prism trust` silences this note."
}

func underTemp(root string) bool {
	for _, t := range []string{os.TempDir(), "/tmp", "/private/tmp", "/private/var/folders", "/var/folders"} {
		if real, err := filepath.EvalSymlinks(t); err == nil {
			t = real
		}
		t = filepath.Clean(t)
		if root == t || strings.HasPrefix(root, t+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Roots lists the recorded roots and their state, sorted.
func Roots() [][2]string {
	mu.Lock()
	defer mu.Unlock()
	f := load()
	out := make([][2]string, 0, len(f.Roots))
	for k, v := range f.Roots {
		out = append(out, [2]string{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
