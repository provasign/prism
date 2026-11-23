package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MKT-05: init --dry-run, the install manifest and prism uninstall share
// one mechanism. Snapshot the files init can touch, run init, diff. The
// manifest records only what init added, so uninstall removes exactly that
// and nothing a user or another tool wrote.

// installDirs are the agent-config directories init writes into; top-level
// files of the project are snapshotted too.
var installDirs = []string{".claude", ".cursor", ".vscode", ".gemini", ".codex", ".github", ".windsurf", ".kiro", ".devin", ".opencode"}

const (
	maxSnapshotFile = 1 << 20
	manifestPath    = ".grove/install-manifest.json"
)

// prismYAMLOwnedKeys are the prism.yaml keys init writes.
var prismYAMLOwnedKeys = []string{"version", "profile", "harnesses", "mcp_surface", "interface"}

type manifestEntry struct {
	File    string   `json:"file"`
	Kind    string   `json:"kind"` // json | steering | toml | yaml | lines | file
	Created bool     `json:"created,omitempty"`
	Keys    []string `json:"keys,omitempty"`  // json: "a.b" or "a.b[]=<json value>"; yaml: key names
	Lines   []string `json:"lines,omitempty"` // lines appended (.gitignore)
	SHA256  string   `json:"sha256,omitempty"`
}

type installManifest struct {
	Entries []manifestEntry `json:"entries"`
}

// snapshotInstallFiles reads the top-level files of root and every file in
// installDirs, each up to 1 MB.
func snapshotInstallFiles(root string) map[string][]byte {
	snap := map[string][]byte{}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if e.Type().IsRegular() {
				addSnapshot(snap, root, e.Name())
			}
		}
	}
	for _, d := range installDirs {
		base := filepath.Join(root, d)
		_ = filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !e.Type().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			addSnapshot(snap, root, filepath.ToSlash(rel))
			return nil
		})
	}
	return snap
}

func addSnapshot(snap map[string][]byte, root, rel string) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if fi, err := os.Stat(p); err != nil || fi.Size() > maxSnapshotFile {
		return
	}
	if b, err := os.ReadFile(p); err == nil {
		snap[rel] = b
	}
}

// manifestFromDiff lists what init added between two snapshots.
func manifestFromDiff(before, after map[string][]byte) []manifestEntry {
	var out []manifestEntry
	rels := make([]string, 0, len(after))
	for rel := range after {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		old, existed := before[rel]
		cur := after[rel]
		if existed && bytes.Equal(old, cur) {
			continue
		}
		e := manifestEntry{File: rel, Created: !existed}
		base := filepath.Base(rel)
		switch {
		case base == "prism.yaml":
			e.Kind = "yaml"
			had := config0Keys(old)
			for _, k := range prismYAMLOwnedKeys {
				if config0Keys(cur)[k] && (!had[k] || !existed) {
					e.Keys = append(e.Keys, k)
				}
			}
		case strings.HasSuffix(base, ".json"):
			e.Kind = "json"
			var a, b any
			if existed {
				_ = json.Unmarshal(old, &a)
			}
			if json.Unmarshal(cur, &b) != nil {
				continue
			}
			e.Keys = jsonAdditions(a, b, "")
		case strings.HasSuffix(base, ".toml"):
			if !bytes.Contains(cur, []byte("[mcp_servers.prism]")) || bytes.Contains(old, []byte("[mcp_servers.prism]")) && existed {
				continue
			}
			e.Kind = "toml"
			e.Keys = []string{"mcp_servers.prism"}
		case base == ".gitignore":
			e.Kind = "lines"
			had := map[string]bool{}
			for _, l := range strings.Split(string(old), "\n") {
				had[strings.TrimSpace(l)] = true
			}
			for _, l := range strings.Split(string(cur), "\n") {
				if t := strings.TrimSpace(l); t != "" && !had[t] {
					e.Lines = append(e.Lines, t)
				}
			}
		case bytes.Contains(cur, []byte("## Prism —")):
			e.Kind = "steering"
		default:
			if !e.Created {
				continue // a change Prism cannot attribute: never undone
			}
			e.Kind = "file"
			sum := sha256.Sum256(cur)
			e.SHA256 = hex.EncodeToString(sum[:])
		}
		if (e.Kind == "json" || e.Kind == "yaml") && len(e.Keys) == 0 && !e.Created {
			continue
		}
		if e.Kind == "lines" && len(e.Lines) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// config0Keys lists the top-level keys of a prism.yaml.
func config0Keys(data []byte) map[string]bool {
	keys := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || l != t {
			continue
		}
		if k, _, ok := strings.Cut(t, ":"); ok {
			keys[strings.TrimSpace(k)] = true
		}
	}
	return keys
}

// jsonAdditions lists key paths present in b and not in a, and array
// elements b gained, as "path[]=<json>".
func jsonAdditions(a, b any, prefix string) []string {
	var out []string
	switch bv := b.(type) {
	case map[string]any:
		av, _ := a.(map[string]any)
		keys := make([]string, 0, len(bv))
		for k := range bv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			old, had := av[k]
			if !had {
				out = append(out, addedJSONValue(k, bv[k], p)...)
				continue
			}
			out = append(out, jsonAdditions(old, bv[k], p)...)
		}
	case []any:
		av, _ := a.([]any)
		had := map[string]bool{}
		for _, x := range av {
			raw, _ := marshalNoEscape(x)
			had[string(raw)] = true
		}
		for _, x := range bv {
			raw, _ := marshalNoEscape(x)
			if !had[string(raw)] {
				out = append(out, prefix+"[]="+string(raw))
			}
		}
	default:
		// A changed scalar under a prism-named key is Prism's own entry
		// being rewritten; any other changed scalar is not recorded.
		if a != nil && fmt.Sprint(a) != fmt.Sprint(b) && strings.Contains(prefix, "prism") {
			out = append(out, prefix)
		}
	}
	return out
}

// addedJSONValue records a value init added at path p. A Prism-named
// entry is recorded whole; any other new object is recorded as a
// container ("p{}", removed only if empty at uninstall) plus its own
// additions, so a user's later sibling in it is never removed.
func addedJSONValue(key string, v any, p string) []string {
	m, isMap := v.(map[string]any)
	if !isMap || key == "prism" || strings.HasPrefix(key, "mcp__prism") {
		if arr, ok := v.([]any); ok && !strings.Contains(key, "prism") {
			out := []string{p + "{}"}
			for _, x := range arr {
				raw, _ := marshalNoEscape(x)
				out = append(out, p+"[]="+string(raw))
			}
			return out
		}
		return []string{p}
	}
	out := []string{p + "{}"}
	for _, k := range sortedKeys(m) {
		out = append(out, addedJSONValue(k, m[k], p+"."+k)...)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func loadManifest(root string) installManifest {
	var m installManifest
	if raw, err := os.ReadFile(filepath.Join(root, manifestPath)); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// recordManifest merges new entries into the project's manifest: a file
// first created by init stays "created" across re-inits.
func recordManifest(root string, entries []manifestEntry) error {
	if len(entries) == 0 {
		return nil
	}
	m := loadManifest(root)
	idx := map[string]int{}
	for i, e := range m.Entries {
		idx[e.File] = i
	}
	for _, e := range entries {
		i, ok := idx[e.File]
		if !ok {
			idx[e.File] = len(m.Entries)
			m.Entries = append(m.Entries, e)
			continue
		}
		prev := &m.Entries[i]
		prev.Created = prev.Created || e.Created
		prev.Keys = unionStrings(prev.Keys, e.Keys)
		prev.Lines = unionStrings(prev.Lines, e.Lines)
		if e.SHA256 != "" {
			prev.SHA256 = e.SHA256
		}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, ".grove"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, manifestPath), append(raw, '\n'), 0o644)
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// makeInitMirror copies the snapshot into a fresh temporary directory (a
// git repository when root is one) for init --dry-run.
func makeInitMirror(root string, snap map[string][]byte) (string, error) {
	dir, err := os.MkdirTemp("", "prism-init-dry-run-")
	if err != nil {
		return "", err
	}
	for rel, b := range snap {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		_ = exec.Command("git", "-C", dir, "init", "-q").Run()
	}
	return dir, nil
}

// printDryRunDiff prints a unified diff per file init would change, with
// the mirror's path shown as the real root.
func printDryRunDiff(root, mirror string, before, after map[string][]byte) int {
	rels := make([]string, 0, len(after))
	for rel, b := range after {
		if old, ok := before[rel]; !ok || !bytes.Equal(old, b) {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	if len(rels) == 0 {
		fmt.Println("init --dry-run: no file would change")
		return 0
	}
	fmt.Printf("init --dry-run: %d file(s) would change; nothing was written\n", len(rels))
	tmp, err := os.MkdirTemp("", "prism-init-diff-")
	if err != nil {
		return exitInternal
	}
	defer os.RemoveAll(tmp)
	for _, rel := range rels {
		oldPath := os.DevNull
		if old, ok := before[rel]; ok {
			oldPath = filepath.Join(tmp, "old")
			_ = os.WriteFile(oldPath, old, 0o644)
		}
		newPath := filepath.Join(mirror, filepath.FromSlash(rel))
		out, _ := exec.Command("git", "diff", "--no-index", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", oldPath, newPath).Output()
		if len(out) == 0 {
			fmt.Printf("would write %s\n", rel)
			continue
		}
		_, existed := before[rel]
		for _, line := range strings.SplitAfter(string(out), "\n") {
			switch {
			case strings.HasPrefix(line, "diff --git "):
				line = "diff --git a/" + rel + " b/" + rel + "\n"
			case strings.HasPrefix(line, "--- "):
				if existed {
					line = "--- a/" + rel + "\n"
				} else {
					line = "--- /dev/null\n"
				}
			case strings.HasPrefix(line, "+++ "):
				line = "+++ b/" + rel + "\n"
			default:
				line = strings.ReplaceAll(line, mirror, root)
			}
			fmt.Print(line)
		}
	}
	return 0
}

// captureInitStdout runs fn with stdout collected.
func captureInitStdout(fn func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	defer func() { os.Stdout = orig }()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}
