package mcp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// renameCollisions finds declarations that already use newName where the
// renamed member would land: a member of the same owner type (in the same
// Go package, or the same file elsewhere), or for a free function a
// same-named top-level symbol in that scope. rename-plan used to return
// clean edits for Provider.Chat -> Name although Provider declares Name()
// (BUG-13).
func (h *Handler) renameCollisions(ctx context.Context, r *grove.ChangeImpactResult, newName string) []map[string]any {
	members := make([]grove.SymbolRecord, 0, len(r.Declarations)+len(r.Family)+len(r.Supers))
	members = append(members, r.Declarations...)
	members = append(members, r.Family...)
	members = append(members, r.Supers...)
	seen := map[string]bool{}
	var out []map[string]any
	add := func(name, file string, line int, renamed string) {
		key := fmt.Sprintf("%s:%d", normalizePath(file), line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, map[string]any{"name": name, "file": normalizePath(file), "line": line, "renamedMember": renamed})
	}
	queried := map[string]bool{}
	for _, m := range members {
		qn := displayQN(m)
		owner := ""
		if i := strings.LastIndexByte(qn, '.'); i >= 0 {
			owner = qn[:i]
		}
		target := newName
		if owner != "" {
			target = owner + "." + newName
		}
		if queried[target+"\x00"+m.FilePath] {
			continue
		}
		queried[target+"\x00"+m.FilePath] = true
		cands, err := h.Grove.Resolve(ctx, target)
		if err != nil {
			continue
		}
		for _, c := range cands {
			switch {
			case leafName(c.Name) != newName:
				continue
			case owner != "" && c.Name != target:
				continue
			case owner == "" && strings.Contains(c.Name, "."):
				continue
			case normalizePath(c.File) == normalizePath(m.FilePath) && c.Line == m.Span.Start:
				continue // the member itself (newName equals its name)
			case !sameRenameScope(m, c.File):
				continue
			}
			add(c.Name, c.File, c.Line, qn)
		}
	}
	// Go and TypeScript interface members are not indexed as symbols; the
	// declaring type's body is read for a member already named newName.
	member := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(newName) + `\s*[(?:<]`)
	for _, t := range r.DeclaringTypes {
		for _, line := range linesMatching(filepath.Join(h.Root, t.FilePath), t.Span.Start, t.Span.End, member) {
			add(displayQN(t)+"."+newName, t.FilePath, line, displayQN(t))
		}
	}
	return out
}

// sameRenameScope reports whether a declaration in file shares the renamed
// member's scope: the same directory (package) for Go, the same file
// otherwise.
func sameRenameScope(m grove.SymbolRecord, file string) bool {
	a, b := normalizePath(m.FilePath), normalizePath(file)
	if m.Language == "go" || strings.HasSuffix(a, ".go") {
		return filepath.Dir(a) == filepath.Dir(b)
	}
	return a == b
}

// linesMatching returns the line numbers in [from, to] of path that match re.
func linesMatching(path string, from, to int, re *regexp.Regexp) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		if n < from {
			continue
		}
		if n > to {
			break
		}
		if re.MatchString(sc.Text()) {
			out = append(out, n)
		}
	}
	return out
}

// restoreEditIndent puts back the leading whitespace a declaration edit
// lost: the engine takes the line from the symbol's text, which starts at
// the symbol's column, so `    def total(self):` printed as
// `def total(self):` (BUG-13). Only whitespace is restored, and only when
// the file line ends with the edit's text.
func restoreEditIndent(root string, edits []grove.RenameEdit) {
	files := map[string][]string{}
	for i := range edits {
		e := &edits[i]
		lines, ok := files[e.FilePath]
		if !ok {
			data, err := os.ReadFile(filepath.Join(root, e.FilePath))
			if err == nil {
				lines = strings.Split(string(data), "\n")
			}
			files[e.FilePath] = lines
		}
		if e.Line < 1 || e.Line > len(lines) {
			continue
		}
		line := strings.TrimRight(lines[e.Line-1], "\r")
		if line == e.Before || !strings.HasSuffix(line, e.Before) {
			continue
		}
		prefix := line[:len(line)-len(e.Before)]
		if strings.TrimSpace(prefix) != "" {
			continue
		}
		e.Before, e.After = line, prefix+e.After
	}
}
