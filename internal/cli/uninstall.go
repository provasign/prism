package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cmdUninstall removes exactly what prism init recorded in the install
// manifest (MKT-05): Prism's keys, array entries, steering blocks and
// sections. Every other byte stays (CON-07 encoder); a file init created is
// deleted once nothing else is left in it.
func cmdUninstall(args []string) int {
	dry := false
	var rest []string
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			return rejectUnknownFlag("uninstall", a)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) > 1 {
		return usageError(fmt.Errorf("uninstall: takes one directory, got %q and %q", rest[0], rest[1]))
	}
	root := mustAbs(dirArg(rest, 0, "."))
	m := loadManifest(root)
	if len(m.Entries) == 0 {
		fmt.Fprintf(os.Stderr, "uninstall: no install manifest in %s (%s); nothing recorded to remove\n", root, manifestPath)
		return exitNotFound
	}
	verb := "removed"
	if dry {
		verb = "would remove"
	}
	failed := 0
	for _, e := range m.Entries {
		msg, err := undoEntry(root, e, dry)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "uninstall: %s: %v\n", e.File, err)
			continue
		}
		if msg != "" {
			fmt.Printf("%s %s\n", verb, msg)
		}
	}
	if !dry && failed == 0 {
		_ = os.Remove(filepath.Join(root, manifestPath))
	}
	if failed > 0 {
		return exitFinding
	}
	return exitOK
}

// undoEntry reverses one manifest entry and says what it removed.
func undoEntry(root string, e manifestEntry, dry bool) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(e.File))
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var next []byte
	what := ""
	switch e.Kind {
	case "json":
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", fmt.Errorf("not valid JSON, left unchanged: %w", err)
		}
		var containers []string
		for _, k := range e.Keys {
			if c, ok := strings.CutSuffix(k, "{}"); ok {
				containers = append(containers, c)
				continue
			}
			doc = removeJSONPath(doc, k)
		}
		// Containers init created go last, deepest first, and only if
		// nothing else was put in them since.
		sort.Slice(containers, func(i, j int) bool { return strings.Count(containers[i], ".") > strings.Count(containers[j], ".") })
		for _, c := range containers {
			doc = removeIfEmpty(doc, c)
		}
		if e.Created && jsonEmpty(doc) {
			next = nil
		} else {
			out, err := encodeJSONFile(doc, raw)
			if err != nil {
				return "", err
			}
			next = append(out, '\n')
		}
		var shown []string
		for _, k := range e.Keys {
			if !strings.HasSuffix(k, "{}") {
				shown = append(shown, k)
			}
		}
		what = strings.Join(shown, ", ") + " from " + e.File
	case "yaml":
		owned := map[string]bool{}
		for _, k := range e.Keys {
			owned[k] = true
		}
		var kept []string
		for _, l := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(l)
			if k, _, ok := strings.Cut(t, ":"); ok && l == t && owned[strings.TrimSpace(k)] {
				continue
			}
			kept = append(kept, l)
		}
		next = []byte(strings.Join(kept, "\n"))
		what = strings.Join(e.Keys, ", ") + " from " + e.File
	case "toml":
		var kept []string
		in := false
		for _, l := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "[") {
				in = t == "[mcp_servers.prism]" || strings.HasPrefix(t, "[mcp_servers.prism.")
			}
			if !in {
				kept = append(kept, l)
			}
		}
		next = []byte(strings.Join(kept, "\n"))
		what = "[mcp_servers.prism] from " + e.File
	case "steering":
		next = []byte(stripPrismSections(string(raw)))
		what = "the Prism steering block from " + e.File
	case "lines":
		drop := map[string]bool{}
		for _, l := range e.Lines {
			drop[l] = true
		}
		var kept []string
		for _, l := range strings.Split(string(raw), "\n") {
			if !drop[strings.TrimSpace(l)] {
				kept = append(kept, l)
			}
		}
		next = []byte(strings.Join(kept, "\n"))
		what = strings.Join(e.Lines, ", ") + " from " + e.File
	case "file":
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != e.SHA256 {
			return "", fmt.Errorf("changed since init wrote it; left in place")
		}
		next = nil
		what = e.File
	default:
		return "", nil
	}
	if e.Created && strings.TrimSpace(stripComments(string(next))) == "" {
		next = nil
	}
	if next == nil {
		what = e.File + " (created by init)"
		if !dry {
			if err := os.Remove(p); err != nil {
				return "", err
			}
			removeEmptyParents(root, filepath.Dir(p))
		}
		return what, nil
	}
	if string(next) == string(raw) {
		return "", nil
	}
	if !dry {
		if err := os.WriteFile(p, next, 0o644); err != nil {
			return "", err
		}
	}
	return what, nil
}

// stripComments drops # comment lines, for an "only comments left" check.
func stripComments(s string) string {
	var kept []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// removeJSONPath deletes "a.b" or the element "a.b[]=<json>".
func removeJSONPath(doc any, path string) any {
	key, elem, isElem := strings.Cut(path, "[]=")
	parts := strings.Split(key, ".")
	parent := doc
	for _, k := range parts[:len(parts)-1] {
		m, ok := parent.(map[string]any)
		if !ok {
			return doc
		}
		parent = m[k]
	}
	m, ok := parent.(map[string]any)
	if !ok {
		return doc
	}
	last := parts[len(parts)-1]
	if !isElem {
		delete(m, last)
		return doc
	}
	arr, _ := m[last].([]any)
	var kept []any
	for _, x := range arr {
		if raw, _ := marshalNoEscape(x); string(raw) == elem {
			continue
		}
		kept = append(kept, x)
	}
	if len(kept) == 0 {
		delete(m, last)
	} else {
		m[last] = kept
	}
	return doc
}

// removeIfEmpty deletes the object or array at path when it is empty.
func removeIfEmpty(doc any, path string) any {
	parts := strings.Split(path, ".")
	parent := doc
	for _, k := range parts[:len(parts)-1] {
		m, ok := parent.(map[string]any)
		if !ok {
			return doc
		}
		parent = m[k]
	}
	m, ok := parent.(map[string]any)
	if !ok {
		return doc
	}
	switch v := m[parts[len(parts)-1]].(type) {
	case map[string]any:
		if len(v) == 0 {
			delete(m, parts[len(parts)-1])
		}
	case []any:
		if len(v) == 0 {
			delete(m, parts[len(parts)-1])
		}
	}
	return doc
}

// jsonEmpty reports a document holding only empty objects.
func jsonEmpty(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, x := range m {
		if !jsonEmpty(x) {
			return false
		}
	}
	return true
}

// removeEmptyParents removes directories init left empty, up to root.
func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
