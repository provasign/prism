package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// The registry is the list of repositories Prism has been set up in on this
// machine, with the packages each declares. It lets discovery match a
// dependency to a checkout that is not a sibling directory. It holds only
// repositories the user ran `prism init` or `prism index` in, lives outside
// every repository, and is never committed.

// RegistryEntry is one known repository.
type RegistryEntry struct {
	Path     string   `json:"path"`
	Packages []string `json:"packages"` // "ecosystem:name"
	Updated  string   `json:"updated"`
}

// StateDir is where Prism keeps per-user state: $PRISM_STATE_DIR, else the
// user configuration directory.
func StateDir() string {
	if d := os.Getenv("PRISM_STATE_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "prism")
}

func registryPath() string {
	d := StateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "repos.json")
}

// Known returns the registered repositories that still exist on disk.
func Known() []RegistryEntry {
	p := registryPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var all []RegistryEntry
	if json.Unmarshal(data, &all) != nil {
		return nil
	}
	out := all[:0]
	for _, e := range all {
		if fi, err := os.Stat(e.Path); err == nil && fi.IsDir() {
			out = append(out, e)
		}
	}
	return out
}

// Register records root and the packages its manifests declare. A
// repository that declares none is not recorded: nothing could match it.
// Failures are ignored; the registry is a convenience, never a dependency.
func Register(root string) {
	p := registryPath()
	if p == "" {
		return
	}
	root = resolve(root)
	var pkgs []string
	for _, pkg := range groveeng.ScanManifests(root).Packages {
		pkgs = append(pkgs, pkg.Ecosystem+":"+pkg.Name)
	}
	sort.Strings(pkgs)
	entries := Known()
	kept := entries[:0]
	for _, e := range entries {
		if e.Path != root {
			kept = append(kept, e)
		}
	}
	if len(pkgs) > 0 {
		kept = append(kept, RegistryEntry{Path: root, Packages: pkgs, Updated: time.Now().UTC().Format(time.RFC3339)})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}
