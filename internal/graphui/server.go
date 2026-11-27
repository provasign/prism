package graphui

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/provasign/prism/internal/lru"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/httpguard"
)

//go:embed index.html
var indexHTML []byte

// Snapshot loads the current graph. Reindex, when set, brings the index up
// to date with the working tree first (the page's Reload button).
type Snapshot func(ctx context.Context) ([]grove.SymbolRecord, []grove.Edge, error)

// Server is the explorer's HTTP surface: one page and a small read-only
// JSON API over the current Model.
type Server struct {
	root     string
	version  string
	snapshot Snapshot
	reindex  func(ctx context.Context) error
	impact   ImpactFunc

	impactMu    sync.Mutex
	impactCache *lru.Cache[string, map[string]any] // symbol id -> change-impact answer; cleared on reload

	model    atomic.Pointer[Model]
	loadedAt atomic.Int64
	reloadMu sync.Mutex

	// token, when set, is required on /api/* (X-Prism-Token): another local
	// user could otherwise read source through the loopback port (SEC-09).
	token string

	// cross, when set, draws other repositories on the root view.
	cross      CrossRepoFunc
	crossMu    sync.Mutex
	crossCache map[string]crossCached
}

// SetToken requires tok on every /api/* request.
func (s *Server) SetToken(tok string) { s.token = tok }

// impactCacheLimit caps the cached change-impact answers. One answer for a
// widely used symbol can list thousands of sites, and a long explorer session
// otherwise keeps every symbol ever clicked.
const impactCacheLimit = 256

// NewServer loads the first snapshot and returns a ready server.
func NewServer(ctx context.Context, root, version string, snapshot Snapshot, reindex func(context.Context) error, impact ImpactFunc) (*Server, error) {
	s := &Server{root: root, version: version, snapshot: snapshot, reindex: reindex, impact: impact,
		impactCache: lru.New[string, map[string]any](impactCacheLimit)}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) load(ctx context.Context) error {
	syms, edges, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	s.model.Store(BuildModel(s.root, syms, edges))
	s.impactMu.Lock()
	s.impactCache = lru.New[string, map[string]any](impactCacheLimit)
	s.impactMu.Unlock()
	s.loadedAt.Store(time.Now().Unix())
	return nil
}

// Model returns the model currently served.
func (s *Server) Model() *Model { return s.model.Load() }

// Handler returns the HTTP handler. Every request must address the server
// by a loopback name: a page on another origin cannot read responses (no
// CORS headers are sent), and the Host check stops a DNS-rebinding page
// from turning its own hostname into this server and reading source.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/graph", s.graph)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/source", s.source)
	mux.HandleFunc("GET /api/impact", s.impactView)
	mux.HandleFunc("POST /api/reload", s.reload)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpguard.LoopbackHost(r.Host) {
			http.Error(w, "prism graph answers on localhost only", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if s.token != "" && strings.HasPrefix(r.URL.Path, "/api/") &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Prism-Token")), []byte(s.token)) != 1 {
			http.Error(w, "missing or wrong token: open the address prism graph printed", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:")
	_, _ = w.Write(indexHTML)
}

func (s *Server) meta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Meta
		Version   string `json:"version"`
		LoadedAt  int64  `json:"loadedAt"`
		CanIndex  bool   `json:"canIndex"`
		CanImpact bool   `json:"canImpact"`
	}{s.Model().Meta(), s.version, s.loadedAt.Load(), s.reindex != nil, s.impact != nil})
}

func boolParam(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	tests := boolParam(r, "tests")
	g, err := s.Model().Graph(scope, Options{
		IncludeTests: tests,
		Flat:         boolParam(r, "flat"),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if s.cross != nil && (scope == "" || scope == "d:") && !boolParam(r, "norepos") {
		cr, err := s.crossRepo(r.Context(), tests)
		if err != nil {
			g.CrossRepoCoverage = "other repositories NOT READ: " + err.Error()
		} else {
			addCrossRepo(g, cr)
		}
	}
	writeJSON(w, http.StatusOK, g)
}

// SetCrossRepo enables other repositories' nodes on the root view.
func (s *Server) SetCrossRepo(f CrossRepoFunc) { s.cross = f }

// crossRepo caches the answer per loaded index and tests setting.
func (s *Server) crossRepo(ctx context.Context, tests bool) (map[string]any, error) {
	key := fmt.Sprintf("%d/%v", s.loadedAt.Load(), tests)
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	if v, ok := s.crossCache[key]; ok && time.Since(v.at) < 30*time.Second {
		return v.val, nil
	}
	val, err := s.cross(ctx, tests)
	if err != nil {
		return nil, err
	}
	if s.crossCache == nil {
		s.crossCache = map[string]crossCached{}
	}
	s.crossCache[key] = crossCached{val: val, at: time.Now()}
	return val, nil
}

type crossCached struct {
	val map[string]any
	at  time.Time
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	hits := s.Model().Search(r.URL.Query().Get("q"), limit)
	if hits == nil {
		hits = []Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	src, err := s.Model().Source(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// reload re-indexes the working tree (delta) and swaps in a new snapshot.
// It changes server state, so it requires a header a cross-origin form or
// image request cannot set.
func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Prism-Graph") == "" {
		writeErr(w, http.StatusForbidden, errors.New("missing X-Prism-Graph header"))
		return
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if s.reindex != nil {
		if err := s.reindex(r.Context()); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.load(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.meta(w, r)
}

// impactView answers /api/impact?id=<symbol>&depth=1..3&tests=0|1 with the
// change-impact answer for that symbol laid onto the graph.
func (s *Server) impactView(w http.ResponseWriter, r *http.Request) {
	if s.impact == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("change impact is not available in this server"))
		return
	}
	m := s.Model()
	id := r.URL.Query().Get("id")
	query, file, err := m.ImpactQuery(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	s.impactMu.Lock()
	raw, ok := s.impactCache.Get(id)
	s.impactMu.Unlock()
	if !ok {
		raw, err = s.impact(r.Context(), query, file)
		// One candidate offered back for file scoping: take it.
		if err == nil && raw["status"] == "needs_file_scope" {
			if alts := asList(raw["alternatives"]); len(alts) == 1 {
				if a, _ := alts[0].(map[string]any); a != nil && str(a["filePath"]) != "" {
					raw, err = s.impact(r.Context(), query, str(a["filePath"]))
				}
			}
		}
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		if st := str(raw["status"]); st != "" && raw["declarations"] == nil && raw["callers"] == nil {
			msg := firstNonEmpty(str(raw["ambiguityNote"]), str(raw["note"]), "change impact returned status "+st)
			writeErr(w, http.StatusUnprocessableEntity, errors.New(msg))
			return
		}
		s.impactMu.Lock()
		s.impactCache.Put(id, raw)
		s.impactMu.Unlock()
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	out, err := m.Impact(id, raw, depth, boolParam(r, "tests"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
