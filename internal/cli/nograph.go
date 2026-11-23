package cli

import (
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/mcp"
)

// noGraphSummary counts the code files in root whose language has no graph
// (GAP-09), e.g. "no graph support: 412 Ruby files, 38 Vue files"; "" when
// there are none. Tracked files only in a git repository.
func noGraphSummary(root string) string {
	counts := map[string]int{}
	add := func(rel string) {
		if lang := mcp.NoGraphLanguage(filepath.Ext(rel)); lang != "" {
			counts[lang]++
		}
	}
	if out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				add(f)
			}
		}
	} else {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".grove", "node_modules", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			add(p)
			return nil
		})
	}
	if len(counts) == 0 {
		return ""
	}
	langs := make([]string, 0, len(counts))
	for l := range counts {
		langs = append(langs, l)
	}
	sort.Slice(langs, func(i, j int) bool { return counts[langs[i]] > counts[langs[j]] })
	parts := make([]string, 0, len(langs))
	for _, l := range langs {
		parts = append(parts, fmt.Sprintf("%d %s file(s)", counts[l], l))
	}
	return "no graph support: " + strings.Join(parts, ", ") + " (text search only; graph answers do not cover them)"
}
