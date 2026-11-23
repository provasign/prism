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

	"github.com/provasign/prism/internal/config"
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
	// Kind is "" for a cache clone and "reverse" for the other end of a
	// link a `prism workspace` command made from Path's prism.yaml.
	Kind string `json:"kind,omitempty"`
}

const kindReverse = "reverse"

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
		if l.Primary == primary && l.Kind == "" {
			out = append(out, l)
		}
	}
	return out
}

// A link is written in one repository's prism.yaml but is useful from both
// ends: the library wants its consumers' call sites, the consumer wants the
// library's source, and a Java consumer is only analysed against the
// library's sources if it knows where they are. So the command that makes a
// link also records its other end, per machine, outside both repositories.
// Only an explicit `prism workspace` command records one; merely having a
// prism.yaml that points at a repository does not make that repository
// trust it.

// LinkReverse records that linker's prism.yaml links target, so target's
// workspace includes linker.
func LinkReverse(linker, target string) {
	linker, target = resolve(linker), resolve(target)
	if linker == target {
		return
	}
	links := readLinks()
	for _, l := range links {
		if l.Kind == kindReverse && l.Primary == target && l.Path == linker {
			return
		}
	}
	_ = writeLinks(append(links, CacheLink{Primary: target, ID: filepath.Base(linker), Path: linker, Kind: kindReverse}))
}

// UnlinkReverse removes the record LinkReverse made.
func UnlinkReverse(linker, target string) {
	linker, target = resolve(linker), resolve(target)
	links := readLinks()
	kept := links[:0]
	changed := false
	for _, l := range links {
		if l.Kind == kindReverse && l.Primary == target && l.Path == linker {
			changed = true
			continue
		}
		kept = append(kept, l)
	}
	if changed {
		_ = writeLinks(kept)
	}
}

// reverseLinks returns the repositories whose prism.yaml links primary, as
// recorded on this machine. A record is honoured only while that prism.yaml
// still declares the link: editing the file by hand unlinks both ends.
func reverseLinks(primary string) []CacheLink {
	var out []CacheLink
	for _, l := range readLinks() {
		if l.Kind != kindReverse || l.Primary != primary {
			continue
		}
		cfg, err := config.LoadFromDir(l.Path)
		if err != nil {
			continue
		}
		for _, r := range cfg.WorkspaceRepos {
			p := r.Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(l.Path, filepath.FromSlash(p))
			}
			if resolve(p) == primary {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// ConfirmLinks records the other end of every link in root's prism.yaml
// whose directory exists. It runs from explicit `prism workspace` and
// `prism init` commands, so a repository cloned with links already written
// becomes visible from the repositories it links once the user has run one
// of them there.
func ConfirmLinks(root string, cfg *config.Config) {
	if cfg == nil || untrusted(root) {
		return
	}
	confirm := func(base string, repos []config.WorkspaceRepo) {
		for _, r := range repos {
			p := r.Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(base, filepath.FromSlash(p))
			}
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				LinkReverse(base, p)
			}
		}
	}
	confirm(root, cfg.WorkspaceRepos)
	// A workspace file in a folder above this repository: the same command
	// shows its links, so it confirms them too.
	if dir, ok := ancestorConfigDir(resolve(root)); ok {
		if parent, err := config.LoadFromDir(dir); err == nil {
			confirm(dir, parent.WorkspaceRepos)
		}
	}
}

// RedactURL removes a user name and password from a URL for display.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
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
	cmd := exec.Command("git", append([]string{"-c", "protocol.ext.allow=never"}, args...)...)
	cmd.Dir = dir
	// Never prompt: a missing credential must fail, not hang a command an
	// agent may be running.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		shown := make([]string, len(args))
		for i, a := range args {
			shown[i] = RedactURL(a)
		}
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(shown, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchClone makes sure the commit that ref names in the remote is checked
// out in the cache, and returns its directory and commit. An empty ref is
// the remote's default branch. Full history objects are fetched: a
// blob-filtered clone downloads file by file on first read, which stalls an
// indexer that reads every file.
func FetchClone(repo, ref string) (dir, sha string, err error) {
	// The URL and ref reach git's command line. Neither may be read as an
	// option, and the ext:: transport runs an arbitrary command.
	if strings.HasPrefix(repo, "-") || strings.HasPrefix(ref, "-") || strings.HasPrefix(strings.ToLower(repo), "ext::") {
		return "", "", fmt.Errorf("refusing repository %q at ref %q", RedactURL(repo), ref)
	}
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
		if _, err := gitRun(home, "clone", "--bare", "--quiet", "--", repo, bare); err != nil {
			return "", "", err
		}
	} else if _, err := gitRun(bare, "fetch", "--quiet", "--prune", "--tags", "--", repo, "+refs/heads/*:refs/heads/*"); err != nil {
		return "", "", err
	}
	target := ref
	if target == "" {
		target = "HEAD"
	}
	sha, err = gitRun(bare, "rev-parse", "--verify", "--quiet", "--end-of-options", target+"^{commit}")
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
