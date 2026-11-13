package linkserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store keeps bundles on disk, one file per repository and commit, and a
// small refs file per repository mapping each branch to the commit last
// uploaded from it. A commit's bundle is immutable; only refs move.
type Store struct {
	dir string

	mu     sync.Mutex
	loaded map[string]*indexed // bundle file -> parsed, by modification time
}

type indexed struct {
	mtime  int64
	bundle Bundle
	byKey  map[string][]Site
	defs   map[string][]Def
	byName map[string][]Def // qualified name, and its last segment
}

// Open returns a store rooted at dir, creating it.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, loaded: map[string]*indexed{}}, nil
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

type refsFile struct {
	Repo   string            `json:"repo"`
	Refs   map[string]string `json:"refs"`   // branch -> sha
	Latest string            `json:"latest"` // sha of the most recent upload
}

// Put stores a bundle and moves its branch to it.
func (s *Store) Put(b Bundle) error {
	if b.Repo == "" || b.SHA == "" {
		return errors.New("bundle needs repo and sha")
	}
	if b.Dirty {
		return errors.New("bundle was exported from a tree with uncommitted changes; its sha does not describe it")
	}
	dir := filepath.Join(s.dir, slug(b.Repo))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, slug(b.SHA)+".json"), data); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := s.readRefs(dir)
	refs.Repo = b.Repo
	if refs.Refs == nil {
		refs.Refs = map[string]string{}
	}
	if b.Ref != "" {
		refs.Refs[b.Ref] = b.SHA
	}
	refs.Latest = b.SHA
	out, _ := json.MarshalIndent(refs, "", "  ")
	return writeAtomic(filepath.Join(dir, "refs.json"), out)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) readRefs(dir string) refsFile {
	var r refsFile
	if data, err := os.ReadFile(filepath.Join(dir, "refs.json")); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

// current returns, for every repository, the bundle a query should read:
// the commit of the branch named for it in want, else "main", "master",
// else the most recent upload.
func (s *Store) current(want map[string]string) []*indexed {
	entries, _ := os.ReadDir(s.dir)
	var out []*indexed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.dir, e.Name())
		refs := s.readRefs(dir)
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
		if ix := s.load(filepath.Join(dir, slug(sha)+".json")); ix != nil {
			out = append(out, ix)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].bundle.Repo < out[j].bundle.Repo })
	return out
}

func (s *Store) load(path string) *indexed {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ix := s.loaded[path]; ix != nil && ix.mtime == fi.ModTime().UnixNano() {
		return ix
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	ix := &indexed{mtime: fi.ModTime().UnixNano(), byKey: map[string][]Site{}, defs: map[string][]Def{}, byName: map[string][]Def{}}
	if json.Unmarshal(data, &ix.bundle) != nil {
		return nil
	}
	for _, site := range ix.bundle.Refs {
		ix.byKey[site.Key] = append(ix.byKey[site.Key], site)
	}
	for _, d := range ix.bundle.Defs {
		ix.defs[d.Key] = append(ix.defs[d.Key], d)
		ix.byName[d.Name] = append(ix.byName[d.Name], d)
		if i := strings.LastIndexByte(d.Name, '.'); i >= 0 {
			ix.byName[d.Name[i+1:]] = append(ix.byName[d.Name[i+1:]], d)
		}
	}
	s.loaded[path] = ix
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
// "none uploaded".
type ConsumersResponse struct {
	Repos    []RepoSites `json:"repos"`
	Searched int         `json:"searched"`
}

// Consumers answers a ConsumersRequest from the stored bundles.
func (s *Store) Consumers(req ConsumersRequest) ConsumersResponse {
	exclude := map[string]bool{}
	for _, p := range req.ExcludePackages {
		exclude[p] = true
	}
	var resp ConsumersResponse
bundles:
	for _, ix := range s.current(req.Refs) {
		for _, p := range ix.bundle.Packages {
			if exclude[p.Ecosystem+":"+p.Name] {
				continue bundles
			}
		}
		resp.Searched++
		var sites []Site
		for _, k := range req.Keys {
			sites = append(sites, ix.byKey[k]...)
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
			Requires: pinFor(ix.bundle, req.Keys), Sites: sites,
		})
	}
	return resp
}

func pinFor(b Bundle, keys []string) string {
	for _, req := range b.Requires {
		for _, k := range keys {
			eco, rest, _ := strings.Cut(k, ":")
			pkg, _, _ := strings.Cut(rest, "#")
			if eco == req.Ecosystem && (pkg == req.Name || (eco == "go" && strings.HasPrefix(pkg, req.Name+"/"))) {
				return strings.TrimSpace(req.Name + " " + req.Version)
			}
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
	var out []RepoDefs
	for _, ix := range s.current(nil) {
		if defs := ix.defs[key]; len(defs) > 0 {
			out = append(out, RepoDefs{Repo: ix.bundle.Repo, SHA: ix.bundle.SHA, Ref: ix.bundle.Ref, Defs: defs})
		}
	}
	return out
}

// DefineName returns the declarations whose qualified name, or last name
// segment, is name: for a caller that has a name but no package for it.
func (s *Store) DefineName(name string) []RepoDefs {
	var out []RepoDefs
	for _, ix := range s.current(nil) {
		if defs := ix.byName[name]; len(defs) > 0 {
			out = append(out, RepoDefs{Repo: ix.bundle.Repo, SHA: ix.bundle.SHA, Ref: ix.bundle.Ref, Defs: defs})
		}
	}
	return out
}

// Handler serves the store over HTTP. When token is not empty every
// request must carry it as a bearer token; an open server on a shared
// network would hand out every uploaded repository's file names and call
// lines to anyone who asks.
func (s *Store) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]string{"status": "ok", "service": "prism-link"})
	})
	mux.HandleFunc("POST /link/bundle", func(w http.ResponseWriter, r *http.Request) {
		var b Bundle
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<20)).Decode(&b); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.Put(b); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]any{"stored": b.Repo + "@" + b.SHA, "refs": len(b.Refs), "defs": len(b.Defs)})
	})
	mux.HandleFunc("POST /link/consumers", func(w http.ResponseWriter, r *http.Request) {
		var req ConsumersRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, s.Consumers(req))
	})
	mux.HandleFunc("POST /link/define", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if req.Key == "" && req.Name != "" {
			reply(w, http.StatusOK, map[string]any{"repos": s.DefineName(req.Name)})
			return
		}
		reply(w, http.StatusOK, map[string]any{"repos": s.Define(req.Key)})
	})
	if token == "" {
		return mux
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			reply(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong bearer token"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Describe is a one-line summary for the server's start-up message.
func (s *Store) Describe() string {
	return fmt.Sprintf("%d repositories in %s", len(s.current(nil)), s.dir)
}
