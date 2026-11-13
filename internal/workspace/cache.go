package workspace

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// A cache clone is a dependency's source fetched by Prism itself, for a
// repository that is not checked out on this machine. Each clone is a git
// worktree at one commit, named by that commit, so its index can never go
// stale: a branch or tag is resolved to a commit when fetched, and fetching
// again adds a new directory instead of moving an old one.
//
// Clones are made only by an explicit command, with the user's own git
// credentials, and nothing is ever pushed. They are reference source: the
// agent may read them, and has no reason to edit them.

// CacheLink records one cache clone linked to a primary repository. It is
// per-machine state and lives outside every repository.
type CacheLink struct {
	Primary string `json:"primary"`
	ID      string `json:"id"`
	URL     string `json:"url"`
	Ref     string `json:"ref"` // as asked: a branch, tag or commit; "" is the remote's default branch
	SHA     string `json:"sha"`
	Path    string `json:"path"`
}

// CacheDir is where cache clones live: $PRISM_CACHE_DIR, else the user
// cache directory.
func CacheDir() string {
	if d := os.Getenv("PRISM_CACHE_DIR"); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "prism", "repos")
}

func linksPath() string {
	d := StateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "links.json")
}

func readLinks() []CacheLink {
	p := linksPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var links []CacheLink
	_ = json.Unmarshal(data, &links)
	return links
}

func writeLinks(links []CacheLink) error {
	p := linksPath()
	if p == "" {
		return fmt.Errorf("no user state directory")
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Primary != links[j].Primary {
			return links[i].Primary < links[j].Primary
		}
		return links[i].ID < links[j].ID
	})
	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// CacheLinks returns the cache clones linked to primary.
func CacheLinks(primary string) []CacheLink {
	primary = resolve(primary)
	var out []CacheLink
	for _, l := range readLinks() {
		if l.Primary == primary {
			out = append(out, l)
		}
	}
	return out
}

// IsGitURL reports whether a `workspace add` argument names a remote
// repository rather than a directory.
func IsGitURL(arg string) bool {
	return strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") || strings.HasSuffix(strings.SplitN(arg, "@", 2)[0], ".git")
}

// SplitURLRef separates "url@ref". An scp-style "git@host:org/repo" keeps
// its own "@".
func SplitURLRef(arg string) (repo, ref string) {
	i := strings.LastIndexByte(arg, '@')
	if i <= 0 || strings.ContainsAny(arg[i:], "/:") || (strings.HasPrefix(arg, "git@") && strings.Count(arg, "@") == 1) {
		return arg, ""
	}
	return arg[:i], arg[i+1:]
}

// cacheSlug is the directory a remote is cached under: host/org/repo.
func cacheSlug(repo string) string {
	s := repo
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		s = u.Host + "/" + strings.TrimPrefix(u.Path, "/")
	} else if u != nil && u.Scheme == "file" {
		s = "local/" + strings.TrimPrefix(u.Path, "/")
	} else if rest, ok := strings.CutPrefix(repo, "git@"); ok {
		s = strings.Replace(rest, ":", "/", 1)
	}
	s = strings.TrimSuffix(s, ".git")
	clean := make([]string, 0, 4)
	for _, part := range strings.Split(filepath.ToSlash(s), "/") {
		if part != "" && part != "." && part != ".." {
			clean = append(clean, part)
		}
	}
	return filepath.Join(clean...)
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Never prompt: a missing credential must fail, not hang a command an
	// agent may be running.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchClone makes sure the commit that ref names in the remote is checked
// out in the cache, and returns its directory and commit. An empty ref is
// the remote's default branch. Full history objects are fetched: a
// blob-filtered clone downloads file by file on first read, which stalls an
// indexer that reads every file.
func FetchClone(repo, ref string) (dir, sha string, err error) {
	base := CacheDir()
	if base == "" {
		return "", "", fmt.Errorf("no user cache directory")
	}
	home := filepath.Join(base, cacheSlug(repo))
	bare := filepath.Join(home, "repo.git")
	if _, statErr := os.Stat(bare); statErr != nil {
		if err := os.MkdirAll(home, 0o755); err != nil {
			return "", "", err
		}
		if _, err := gitRun(home, "clone", "--bare", "--quiet", repo, bare); err != nil {
			return "", "", err
		}
	} else if _, err := gitRun(bare, "fetch", "--quiet", "--prune", "--tags", repo, "+refs/heads/*:refs/heads/*"); err != nil {
		return "", "", err
	}
	target := ref
	if target == "" {
		target = "HEAD"
	}
	sha, err = gitRun(bare, "rev-parse", "--verify", "--quiet", target+"^{commit}")
	if err != nil || sha == "" {
		return "", "", fmt.Errorf("%s has no commit named %q", repo, target)
	}
	dir = filepath.Join(home, "@"+sha)
	if _, statErr := os.Stat(dir); statErr == nil {
		return dir, sha, nil
	}
	if _, err := gitRun(bare, "worktree", "add", "--detach", "--quiet", dir, sha); err != nil {
		return "", "", err
	}
	return dir, sha, nil
}

// LinkClone records that primary uses the cache clone at dir. A link with
// the same id is replaced: fetching a moved branch points the id at the new
// commit's directory and leaves the old directory for other users of it.
func LinkClone(primary string, link CacheLink) error {
	link.Primary = resolve(primary)
	links := readLinks()
	kept := links[:0]
	for _, l := range links {
		if !(l.Primary == link.Primary && l.ID == link.ID) {
			kept = append(kept, l)
		}
	}
	return writeLinks(append(kept, link))
}

// UnlinkClone removes primary's cache link with this id. The cached
// directory stays; PruneClones removes directories nothing links to.
func UnlinkClone(primary, id string) bool {
	primary = resolve(primary)
	links := readLinks()
	kept := links[:0]
	removed := false
	for _, l := range links {
		if l.Primary == primary && l.ID == id {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	if removed {
		_ = writeLinks(kept)
	}
	return removed
}

// PruneClones deletes cached worktrees that no link refers to and returns
// their directories. Bare clones are kept: they make the next fetch cheap.
func PruneClones() []string {
	base := CacheDir()
	if base == "" {
		return nil
	}
	used := map[string]bool{}
	for _, l := range readLinks() {
		used[filepath.Clean(l.Path)] = true
	}
	var removed []string
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == "repo.git" {
			return filepath.SkipDir
		}
		if strings.HasPrefix(d.Name(), "@") {
			if !used[filepath.Clean(p)] {
				bare := filepath.Join(filepath.Dir(p), "repo.git")
				if _, err := gitRun(bare, "worktree", "remove", "--force", p); err != nil {
					_ = os.RemoveAll(p)
					_, _ = gitRun(bare, "worktree", "prune")
				}
				removed = append(removed, p)
			}
			return filepath.SkipDir
		}
		return nil
	})
	return removed
}
