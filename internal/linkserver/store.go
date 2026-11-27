package linkserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps one bundle per repository and commit, and a small refs object
// per repository mapping each branch to the commit last uploaded from it.
// A commit's bundle never changes meaning; only refs move.
//
// Queries are answered from memory: the catalog of every repository's refs
// is loaded at open and refreshed (Refresh, or Run in the background), and
// the bundles queries read are cached by commit. A replica sees its own
// uploads at once and other replicas' after the next refresh.
type Store struct {
	blob Blob
	keep int

	mu      sync.RWMutex
	repos   map[string]*repoEntry // slug -> refs
	bundles map[string]*indexed   // object key -> parsed bundle

	// refsMu serialises refs edits inside this process, so one replica is
	// safe even on a store without conditional writes; across replicas,
	// compare-and-swap on the store does it.
	refsMu sync.Map // slug -> *sync.Mutex
}

func (s *Store) lockRefs(repo string) func() {
	m, _ := s.refsMu.LoadOrStore(slug(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type repoEntry struct {
	refs refsFile
	etag string
}

// indexed is a parsed bundle with lookup maps holding positions in its
// Refs and Defs, not copies: a 1,000-repository store held each site three
// times before (2.7 GB for 198 MB of bundles in the load test).
type indexed struct {
	size   int
	bundle Bundle
	byKey  map[string][]int32 // site key -> Refs positions
	defs   map[string][]int32 // declaration key -> Defs positions
	byName map[string][]int32 // qualified name, and its last segment -> Defs positions
}

func (ix *indexed) sites(key string) []Site {
	pos := ix.byKey[key]
	out := make([]Site, len(pos))
	for i, p := range pos {
		out[i] = ix.bundle.Refs[p]
	}
	return out
}

func (ix *indexed) defsAt(pos []int32) []Def {
	out := make([]Def, len(pos))
	for i, p := range pos {
		out[i] = ix.bundle.Defs[p]
	}
	return out
}

// Options tune a Store.
type Options struct {
	// Keep is how many commits per repository retention keeps, newest
	// first; commits a branch points at are always kept. 0 keeps all.
	Keep int
}

// DefaultKeep is the retention default: 20 commits per repository.
const DefaultKeep = 20

// Open returns a store on a filesystem directory, creating it, with the
// default retention.
func Open(dir string) (*Store, error) {
	b, err := OpenFS(dir)
	if err != nil {
		return nil, err
	}
	return OpenStore(context.Background(), b, Options{Keep: DefaultKeep})
}

// OpenStore returns a store on any backend and loads its catalog.
func OpenStore(ctx context.Context, b Blob, o Options) (*Store, error) {
	s := &Store{blob: b, keep: o.Keep, repos: map[string]*repoEntry{}, bundles: map[string]*indexed{}}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func slug(repo string) string {
	var b strings.Builder
	for _, r := range repo {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('~')
		}
	}
	return b.String()
}

func refsKey(repo string) string        { return slug(repo) + "/refs.json" }
func bundleKey(repo, sha string) string { return slug(repo) + "/" + slug(sha) + ".json" }

type refsFile struct {
	Repo   string            `json:"repo"`
	Refs   map[string]string `json:"refs"`   // branch -> sha
	Latest string            `json:"latest"` // sha of the most recent upload
	// History lists uploaded commits, newest first; retention trims it.
	History []string `json:"history,omitempty"`
}

// Describe names the backend and the number of repositories.
func (s *Store) Describe() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("%d repositories in %s", len(s.repos), s.blob.Describe())
}

// Refresh re-reads every repository's refs from the backend. Unchanged
// refs cost a conditional read; cached bundles no ref points at any more
// are dropped.
func (s *Store) Refresh(ctx context.Context) error {
	keys, err := s.blob.List(ctx, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !strings.HasSuffix(k, "/refs.json") || strings.Count(k, "/") != 1 {
			continue
		}
		sl := strings.TrimSuffix(k, "/refs.json")
		seen[sl] = true
		s.mu.RLock()
		old := s.repos[sl]
		s.mu.RUnlock()
		tag := ""
		if old != nil {
			tag = old.etag
		}
		data, etag, err := s.blob.Get(ctx, k, tag)
		if errors.Is(err, ErrNotModified) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		var r refsFile
		if json.Unmarshal(data, &r) != nil {
			continue
		}
		s.mu.Lock()
		s.repos[sl] = &repoEntry{refs: r, etag: etag}
		s.mu.Unlock()
	}
	s.mu.Lock()
	for sl := range s.repos {
		if !seen[sl] {
			delete(s.repos, sl)
		}
	}
	s.dropUnreferencedLocked()
	s.mu.Unlock()
	return nil
}

// Warm loads every repository's default bundle into memory, eight at a
// time, so the first queries after a start or a refresh do not wait on the
// backend (2.7 s for 1,000 cold bundles from S3 in the load test).
func (s *Store) Warm(ctx context.Context) {
	s.mu.RLock()
	var keys []string
	for _, e := range s.repos {
		sha := e.refs.Refs["main"]
		if sha == "" {
			sha = e.refs.Refs["master"]
		}
		if sha == "" {
			sha = e.refs.Latest
		}
		if k := bundleKey(e.refs.Repo, sha); sha != "" && s.bundles[k] == nil {
			keys = append(keys, k)
		}
	}
	s.mu.RUnlock()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(k string) {
			defer func() { <-sem; wg.Done() }()
			s.load(ctx, k)
		}(k)
	}
	wg.Wait()
}

// Run warms the cache, then refreshes the catalog (and warms what changed)
// every interval until ctx ends. Errors are passed to onErr (a log) and the
// next tick retries.
func (s *Store) Run(ctx context.Context, every time.Duration, onErr func(error)) {
	s.Warm(ctx)
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(ctx); err != nil && onErr != nil {
				onErr(err)
			}
			s.Warm(ctx)
		}
	}
}

func (s *Store) dropUnreferencedLocked() {
	live := map[string]bool{}
	for _, e := range s.repos {
		for _, sha := range e.refs.Refs {
			live[bundleKey(e.refs.Repo, sha)] = true
		}
		live[bundleKey(e.refs.Repo, e.refs.Latest)] = true
	}
	for k := range s.bundles {
		if !live[k] {
			delete(s.bundles, k)
		}
	}
}

// Put stores a bundle and moves its branch to it. The refs update is a
// compare-and-swap, retried when another writer moved them first.
func (s *Store) Put(b Bundle) error { return s.PutContext(context.Background(), b) }

// PutContext is Put with a context.
func (s *Store) PutContext(ctx context.Context, b Bundle) error {
	if b.Repo == "" || b.SHA == "" {
		return errors.New("bundle needs repo and sha")
	}
	if b.Dirty {
		return errors.New("bundle was exported from a tree with uncommitted changes; its sha does not describe it")
	}
	if b.Format > FormatVersion {
		return fmt.Errorf("bundle format %d is newer than this server accepts (up to %d); upgrade the link server", b.Format, FormatVersion)
	}
	key := bundleKey(b.Repo, b.SHA)
	if b.Ref == "" {
		// A detached-HEAD upload (most CI checkouts) of a commit already
		// stored from a branch keeps that branch: the commit is the same,
		// and answers name the branch they come from.
		if data, _, err := s.blob.Get(ctx, key, ""); err == nil {
			var prev struct {
				Ref string `json:"ref"`
			}
			if json.Unmarshal(data, &prev) == nil {
				b.Ref = prev.Ref
			}
		}
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := s.blob.Put(ctx, key, data); err != nil {
		return err
	}
	var refs refsFile
	var etag string
	unlock, locked := s.lockRefs(b.Repo), true
	defer func() {
		if locked {
			unlock()
		}
	}()
	for attempt := 0; ; attempt++ {
		cur, tag, err := s.blob.Get(ctx, refsKey(b.Repo), "")
		refs = refsFile{}
		switch {
		case errors.Is(err, ErrNotFound):
			tag = ""
		case err != nil:
			return err
		default:
			if json.Unmarshal(cur, &refs) != nil {
				return fmt.Errorf("refs for %s are unreadable; run prism link-server check", b.Repo)
			}
			if refs.Repo != "" && refs.Repo != b.Repo {
				return fmt.Errorf("repository name %q is stored under the same key as %q; rename one with --repo", b.Repo, refs.Repo)
			}
		}
		refs.Repo = b.Repo
		if refs.Refs == nil {
			refs.Refs = map[string]string{}
		}
		if b.Ref != "" {
			refs.Refs[b.Ref] = b.SHA
		}
		refs.Latest = b.SHA
		refs.History = append([]string{b.SHA}, without(refs.History, b.SHA)...)
		out, _ := json.MarshalIndent(refs, "", "  ")
		etag, err = s.blob.PutIf(ctx, refsKey(b.Repo), out, tag)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrConflict) || attempt >= 20 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	s.mu.Lock()
	s.repos[slug(b.Repo)] = &repoEntry{refs: refs, etag: etag}
	delete(s.bundles, key)
	s.dropUnreferencedLocked()
	s.mu.Unlock()
	unlock() // retention edits refs under the same lock
	locked = false
	if s.keep > 0 {
		if _, err := s.Prune(ctx, b.Repo, s.keep); err != nil {
			return fmt.Errorf("stored, but retention failed: %w", err)
		}
	}
	return nil
}

func without(list []string, x string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// refsOf returns a repository's refs from the catalog.
func (s *Store) refsOf(repo string) refsFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := s.repos[slug(repo)]; e != nil {
		return e.refs
	}
	return refsFile{}
}

// Prune deletes a repository's commits beyond the newest keep that no
// branch points at, and returns the commits it deleted. Bundles stored
// before history was recorded (v1.3.x) are listed from the backend and
// counted as oldest.
func (s *Store) Prune(ctx context.Context, repo string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	refs := s.refsOf(repo)
	if refs.Repo == "" {
		return nil, fmt.Errorf("no repository %q", repo)
	}
	pinned := map[string]bool{}
	for _, sha := range refs.Refs {
		pinned[sha] = true
	}
	pinned[refs.Latest] = true
	order := append([]string{}, refs.History...)
	inHistory := map[string]bool{}
	for _, sha := range order {
		inHistory[sha] = true
	}
	keys, err := s.blob.List(ctx, slug(repo)+"/")
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		name := strings.TrimPrefix(k, slug(repo)+"/")
		if name == "refs.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		if sha := strings.TrimSuffix(name, ".json"); !inHistory[sha] {
			order = append(order, sha) // pre-history bundle: oldest
		}
	}
	var deleted []string
	kept := 0
	for _, sha := range order {
		if pinned[sha] {
			continue
		}
		if kept < keep-countPinned(pinned) {
			kept++
			continue
		}
		if err := s.blob.Delete(ctx, bundleKey(repo, sha)); err != nil {
			return deleted, err
		}
		deleted = append(deleted, sha)
	}
	if len(deleted) > 0 {
		if err := s.editRefs(ctx, repo, func(r *refsFile) {
			for _, sha := range deleted {
				r.History = without(r.History, sha)
			}
		}); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func countPinned(p map[string]bool) int {
	n := 0
	for sha := range p {
		if sha != "" {
			n++
		}
	}
	return n
}

// editRefs applies fn to a repository's refs with compare-and-swap.
func (s *Store) editRefs(ctx context.Context, repo string, fn func(*refsFile)) error {
	defer s.lockRefs(repo)()
	for attempt := 0; ; attempt++ {
		cur, tag, err := s.blob.Get(ctx, refsKey(repo), "")
		if err != nil {
			return err
		}
		var r refsFile
		if err := json.Unmarshal(cur, &r); err != nil {
			return err
		}
		fn(&r)
		out, _ := json.MarshalIndent(r, "", "  ")
		etag, err := s.blob.PutIf(ctx, refsKey(repo), out, tag)
		if err == nil {
			s.mu.Lock()
			s.repos[slug(repo)] = &repoEntry{refs: r, etag: etag}
			s.dropUnreferencedLocked()
			s.mu.Unlock()
			return nil
		}
		if !errors.Is(err, ErrConflict) || attempt >= 20 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
}

// DeleteRepo removes everything stored for a repository.
func (s *Store) DeleteRepo(ctx context.Context, repo string) (int, error) {
	keys, err := s.blob.List(ctx, slug(repo)+"/")
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, fmt.Errorf("no repository %q", repo)
	}
	// refs last, so a crash leaves a repository that still lists commits.
	sort.SliceStable(keys, func(i, j int) bool {
		return !strings.HasSuffix(keys[i], "/refs.json") && strings.HasSuffix(keys[j], "/refs.json")
	})
	for _, k := range keys {
		if err := s.blob.Delete(ctx, k); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	delete(s.repos, slug(repo))
	s.dropUnreferencedLocked()
	s.mu.Unlock()
	return len(keys), nil
}

// DeleteCommit removes one commit's bundle. Branches that pointed at it
// are removed too, so answers fall back to the repository's other branches
// or its latest remaining upload.
func (s *Store) DeleteCommit(ctx context.Context, repo, sha string) error {
	if _, _, err := s.blob.Get(ctx, bundleKey(repo, sha), ""); err != nil {
		return fmt.Errorf("no commit %s in %q", sha, repo)
	}
	if err := s.editRefs(ctx, repo, func(r *refsFile) {
		for ref, v := range r.Refs {
			if v == sha {
				delete(r.Refs, ref)
			}
		}
		r.History = without(r.History, sha)
		if r.Latest == sha {
			r.Latest = ""
			if len(r.History) > 0 {
				r.Latest = r.History[0]
			}
		}
	}); err != nil {
		return err
	}
	return s.blob.Delete(ctx, bundleKey(repo, sha))
}

// Problem is one integrity finding from Check.
type Problem struct {
	Repo, Key, Issue string
}

// Check reads every stored object and reports refs that point at missing
// or unreadable bundles, unreadable refs, and bundles stored under the
// wrong repository.
func (s *Store) Check(ctx context.Context) (repos, bundles int, problems []Problem, err error) {
	keys, err := s.blob.List(ctx, "")
	if err != nil {
		return 0, 0, nil, err
	}
	byRepo := map[string][]string{}
	for _, k := range keys {
		sl, name, ok := strings.Cut(k, "/")
		if !ok || strings.Contains(name, "/") {
			problems = append(problems, Problem{Key: k, Issue: "object outside the store layout"})
			continue
		}
		byRepo[sl] = append(byRepo[sl], name)
	}
	for sl, names := range byRepo {
		data, _, gerr := s.blob.Get(ctx, sl+"/refs.json", "")
		var r refsFile
		if gerr != nil || json.Unmarshal(data, &r) != nil || r.Repo == "" {
			problems = append(problems, Problem{Key: sl + "/refs.json", Issue: "refs missing or unreadable"})
			continue
		}
		repos++
		stored := map[string]bool{}
		for _, n := range names {
			if n == "refs.json" {
				continue
			}
			bundles++
			sha := strings.TrimSuffix(n, ".json")
			stored[sha] = true
			bd, _, berr := s.blob.Get(ctx, sl+"/"+n, "")
			var b Bundle
			switch {
			case berr != nil || json.Unmarshal(bd, &b) != nil:
				problems = append(problems, Problem{Repo: r.Repo, Key: sl + "/" + n, Issue: "bundle unreadable"})
			case b.Repo != r.Repo || slug(b.SHA) != sha:
				problems = append(problems, Problem{Repo: r.Repo, Key: sl + "/" + n, Issue: fmt.Sprintf("bundle is %s@%s", b.Repo, b.SHA)})
			}
		}
		for ref, sha := range r.Refs {
			if !stored[slug(sha)] {
				problems = append(problems, Problem{Repo: r.Repo, Key: sl + "/refs.json", Issue: fmt.Sprintf("branch %s points at missing commit %s", ref, sha)})
			}
		}
		if r.Latest != "" && !stored[slug(r.Latest)] {
			problems = append(problems, Problem{Repo: r.Repo, Key: sl + "/refs.json", Issue: "latest points at missing commit " + r.Latest})
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return repos, bundles, problems, nil
}

// Copy copies every object from src to dst: backup, restore, and moving a
// store between backends. It returns the number of objects copied.
func Copy(ctx context.Context, src, dst Blob) (int, error) {
	keys, err := src.List(ctx, "")
	if err != nil {
		return 0, err
	}
	// Bundles before refs, so a partial copy never has refs pointing at
	// commits it lacks.
	sort.SliceStable(keys, func(i, j int) bool {
		return !strings.HasSuffix(keys[i], "/refs.json") && strings.HasSuffix(keys[j], "/refs.json")
	})
	n := 0
	for _, k := range keys {
		data, _, err := src.Get(ctx, k, "")
		if err != nil {
			return n, fmt.Errorf("%s: %w", k, err)
		}
		if err := dst.Put(ctx, k, data); err != nil {
			return n, fmt.Errorf("%s: %w", k, err)
		}
		n++
	}
	return n, nil
}

// Stats is what the metrics endpoint reports about the store.
type Stats struct {
	Repositories, CachedBundles, CachedBytes int
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Repositories: len(s.repos), CachedBundles: len(s.bundles)}
	for _, ix := range s.bundles {
		st.CachedBytes += ix.size
	}
	return st
}

// Allow reports whether a caller may read a repository; nil allows all.
type Allow func(repo string) bool

// current returns, for every readable repository, the bundle a query
// should read: the commit of the branch named for it in want, else "main",
// "master", else the most recent upload. It also counts the repositories
// allow hid.
func (s *Store) current(ctx context.Context, want map[string]string, allow Allow) ([]*indexed, int) {
	s.mu.RLock()
	entries := make([]refsFile, 0, len(s.repos))
	for _, e := range s.repos {
		entries = append(entries, e.refs)
	}
	s.mu.RUnlock()
	var out []*indexed
	hidden := 0
	for _, refs := range entries {
		if allow != nil && !allow(refs.Repo) {
			hidden++
			continue
		}
		sha := ""
		if ref := want[refs.Repo]; ref != "" {
			sha = refs.Refs[ref]
			if sha == "" {
				continue // asked for a branch this repository never uploaded
			}
		}
		for _, def := range []string{"main", "master"} {
			if sha == "" {
				sha = refs.Refs[def]
			}
		}
		if sha == "" {
			sha = refs.Latest
		}
		if sha == "" {
			continue
		}
		if ix := s.load(ctx, bundleKey(refs.Repo, sha)); ix != nil {
			out = append(out, ix)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].bundle.Repo < out[j].bundle.Repo })
	return out, hidden
}

func (s *Store) load(ctx context.Context, key string) *indexed {
	s.mu.RLock()
	ix := s.bundles[key]
	s.mu.RUnlock()
	if ix != nil {
		return ix
	}
	data, _, err := s.blob.Get(ctx, key, "")
	if err != nil {
		return nil
	}
	ix = &indexed{size: len(data), byKey: map[string][]int32{}, defs: map[string][]int32{}, byName: map[string][]int32{}}
	if json.Unmarshal(data, &ix.bundle) != nil {
		return nil
	}
	// Repeated strings (keys, files, enclosing symbols) share one copy.
	intern := map[string]string{}
	in := func(v string) string {
		if c, ok := intern[v]; ok {
			return c
		}
		intern[v] = v
		return v
	}
	for i := range ix.bundle.Refs {
		r := &ix.bundle.Refs[i]
		r.Key, r.File, r.From = in(r.Key), in(r.File), in(r.From)
		ix.byKey[r.Key] = append(ix.byKey[r.Key], int32(i))
	}
	for i := range ix.bundle.Defs {
		d := &ix.bundle.Defs[i]
		d.File, d.Kind = in(d.File), in(d.Kind)
		ix.defs[d.Key] = append(ix.defs[d.Key], int32(i))
		ix.byName[d.Name] = append(ix.byName[d.Name], int32(i))
		if j := strings.LastIndexByte(d.Name, '.'); j >= 0 {
			ix.byName[d.Name[j+1:]] = append(ix.byName[d.Name[j+1:]], int32(i))
		}
	}
	s.mu.Lock()
	s.bundles[key] = ix
	s.mu.Unlock()
	return ix
}

// ConsumersRequest asks who references keys.
type ConsumersRequest struct {
	Keys []string `json:"keys"`
	// ExcludePackages: "ecosystem:name" of packages the asker already
	// covers locally (its own, and its linked checkouts'). A repository
	// declaring one is skipped, so a site is not reported twice.
	ExcludePackages []string `json:"excludePackages,omitempty"`
	// Refs picks a branch per repository; others use their default.
	Refs map[string]string `json:"refs,omitempty"`
	// Packages are the asker's own packages ("ecosystem:name", as its
	// manifests declare them). A consumer's requirement on one of them is
	// its pin, also where the symbol key's package and the manifest's
	// artifact are named differently (jvm:com.acme.lib vs maven:com.acme:lib).
	Packages []string `json:"packages,omitempty"`
}

// RepoSites is one repository's answer.
type RepoSites struct {
	Repo     string `json:"repo"`
	SHA      string `json:"sha"`
	Ref      string `json:"ref,omitempty"`
	Created  string `json:"created"`
	Requires string `json:"requires,omitempty"` // the pin on the asked-about package, as written
	Sites    []Site `json:"sites"`
}

// ConsumersResponse lists the repositories that reference the keys, and how
// many repositories were searched, so "none found" is distinguishable from
// "none uploaded". Unreadable counts repositories the caller's credential
// may not read: they were not searched, and the answer is incomplete.
type ConsumersResponse struct {
	Repos      []RepoSites `json:"repos"`
	Searched   int         `json:"searched"`
	Unreadable int         `json:"unreadable,omitempty"`
}

// Consumers answers a ConsumersRequest from every stored repository.
func (s *Store) Consumers(req ConsumersRequest) ConsumersResponse {
	return s.ConsumersFor(context.Background(), req, nil)
}

// ConsumersFor answers from the repositories allow permits.
func (s *Store) ConsumersFor(ctx context.Context, req ConsumersRequest, allow Allow) ConsumersResponse {
	exclude := map[string]bool{}
	for _, p := range req.ExcludePackages {
		exclude[p] = true
	}
	resp := ConsumersResponse{Repos: []RepoSites{}}
	cur, hidden := s.current(ctx, req.Refs, allow)
	resp.Unreadable = hidden
bundles:
	for _, ix := range cur {
		for _, p := range ix.bundle.Packages {
			if exclude[p.Ecosystem+":"+p.Name] {
				continue bundles
			}
		}
		resp.Searched++
		var sites []Site
		for _, k := range req.Keys {
			sites = append(sites, ix.sites(k)...)
		}
		if len(sites) == 0 {
			continue
		}
		sort.SliceStable(sites, func(i, j int) bool {
			if sites[i].File != sites[j].File {
				return sites[i].File < sites[j].File
			}
			return sites[i].Line < sites[j].Line
		})
		resp.Repos = append(resp.Repos, RepoSites{
			Repo: ix.bundle.Repo, SHA: ix.bundle.SHA, Ref: ix.bundle.Ref, Created: ix.bundle.Created,
			Requires: pinFor(ix.bundle, req.Keys, req.Packages), Sites: sites,
		})
	}
	return resp
}

func pinFor(b Bundle, keys, askerPackages []string) string {
	own := map[string]bool{}
	for _, p := range askerPackages {
		own[p] = true
	}
	for _, req := range b.Requires {
		match := own[req.Ecosystem+":"+req.Name]
		for _, k := range keys {
			eco, rest, _ := strings.Cut(k, ":")
			pkg, _, _ := strings.Cut(rest, "#")
			if eco == req.Ecosystem && (pkg == req.Name || (eco == "go" && strings.HasPrefix(pkg, req.Name+"/"))) {
				match = true
			}
		}
		if match {
			pin := strings.TrimSpace(req.Name + " " + req.Version)
			if req.Local {
				pin += ", built against a local checkout when uploaded"
			}
			return pin
		}
	}
	return ""
}

// RepoDefs is where one repository declares a key.
type RepoDefs struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
	Ref  string `json:"ref,omitempty"`
	Defs []Def  `json:"defs"`
}

// Define returns the declarations of key across repositories.
func (s *Store) Define(key string) []RepoDefs {
	out, _ := s.DefineFor(context.Background(), key, "", nil)
	return out
}

// DefineName returns the declarations whose qualified name, or last name
// segment, is name: for a caller that has a name but no package for it.
func (s *Store) DefineName(name string) []RepoDefs {
	out, _ := s.DefineFor(context.Background(), "", name, nil)
	return out
}

// DefineFor answers Define (key) or DefineName (name) from the
// repositories allow permits, and counts those it hid.
func (s *Store) DefineFor(ctx context.Context, key, name string, allow Allow) ([]RepoDefs, int) {
	out := []RepoDefs{}
	cur, hidden := s.current(ctx, nil, allow)
	for _, ix := range cur {
		pos := ix.defs[key]
		if key == "" {
			pos = ix.byName[name]
		}
		if defs := ix.defsAt(pos); len(defs) > 0 {
			out = append(out, RepoDefs{Repo: ix.bundle.Repo, SHA: ix.bundle.SHA, Ref: ix.bundle.Ref, Defs: defs})
		}
	}
	return out, hidden
}
