package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// A cache directory name never holds a character Windows refuses: a
// file:C:\ URL used to produce "file:C:" as a component.
func TestCacheSlugIsAValidPathEverywhere(t *testing.T) {
	for _, repo := range []string{
		`file:C:\Users\runner\AppData\Local\Temp\x\app`,
		"file:///home/u/app",
		"https://github.com/org/repo.git",
		"git@github.com:org/repo.git",
		"ssh://git@host:2222/org/repo",
	} {
		slug := cacheSlug(repo)
		if slug == "" {
			t.Errorf("%s: empty slug", repo)
		}
		for _, part := range strings.Split(filepath.ToSlash(slug), "/") {
			if strings.ContainsAny(part, `:*?"<>|\`) || part == ".." {
				t.Errorf("%s: invalid component %q in %q", repo, part, slug)
			}
		}
	}
	if got := filepath.ToSlash(cacheSlug("https://github.com/org/repo.git")); got != "github.com/org/repo" {
		t.Errorf("https slug = %q", got)
	}
	// A local repository gets a short name, not its whole path.
	long := `file:C:\Users\runneradmin\AppData\Local\Temp\TestWorkspaceCacheClone203184237\001\upstream\lib`
	if got := filepath.ToSlash(cacheSlug(long)); !strings.HasPrefix(got, "local/lib-") || len(got) > 24 {
		t.Errorf("local slug = %q", got)
	}
	if cacheSlug("file:/a/lib") == cacheSlug("file:/b/lib") {
		t.Error("two local repositories with one name share a cache directory")
	}
}
