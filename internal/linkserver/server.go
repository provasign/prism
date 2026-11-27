package linkserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerOptions configure the HTTP front of a Store.
type ServerOptions struct {
	Auth Authenticator
	// Audit receives one JSON line per request; nil discards.
	Audit io.Writer
	// Log receives server events; nil discards.
	Log *slog.Logger
	// Rate is requests per second per credential (burst 2x); 0 disables.
	Rate float64
	// MaxBundleBytes caps an upload; 0 means 64 MiB.
	MaxBundleBytes int64
	// TrustProxy takes the client address from X-Forwarded-For.
	TrustProxy bool
}

// Server is the link server's HTTP layer.
type Server struct {
	store   *Store
	opt     ServerOptions
	limiter *limiter
	Metrics *Metrics
	auditMu sync.Mutex
}

// NewServer wraps a store.
func NewServer(s *Store, o ServerOptions) *Server {
	if o.Auth == nil {
		o.Auth = OpenAuth{}
	}
	if o.MaxBundleBytes <= 0 {
		o.MaxBundleBytes = 64 << 20
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	srv := &Server{store: s, opt: o, Metrics: newMetrics(s)}
	if o.Rate > 0 {
		srv.limiter = newLimiter(o.Rate, math.Max(1, 2*o.Rate))
	}
	return srv
}

// AcceptedFormats lists the bundle formats this server stores.
func AcceptedFormats() []int {
	out := make([]int, 0, FormatVersion+1)
	for v := 0; v <= FormatVersion; v++ {
		out = append(out, v)
	}
	return out
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"status": "ok", "service": "prism-link", "formats": AcceptedFormats()})
}

// Handler is the public API.
func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.Handle("POST /link/bundle", srv.api("upload", srv.handleUpload))
	mux.Handle("POST /link/consumers", srv.api("consumers", srv.handleConsumers))
	mux.Handle("POST /link/define", srv.api("define", srv.handleDefine))
	mux.Handle("GET /link/repos", srv.api("repos", srv.handleRepos))
	mux.Handle("POST /link/delete", srv.api("delete", srv.handleDelete))
	mux.Handle("POST /link/prune", srv.api("prune", srv.handlePrune))
	return mux
}

// OpsHandler serves /health and /metrics for probes and scraping, without
// credentials: bind it to a private address.
func (srv *Server) OpsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /metrics", srv.Metrics.serve)
	return mux
}

// call is one request in flight, filled in by handlers for the audit line.
type call struct {
	p                 *Principal
	repo, sha         string
	repos, unreadable int
	bytesIn           int64
	note              string
}

type apiFunc func(w http.ResponseWriter, r *http.Request, c *call)

type recorder struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (r *recorder) WriteHeader(code int) { r.code = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (srv *Server) clientAddr(r *http.Request) string {
	if srv.opt.TrustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			first, _, _ := strings.Cut(f, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// api wraps a handler with request id, authentication, rate limiting,
// panic recovery, audit and metrics.
func (srv *Server) api(endpoint string, h apiFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		rec := &recorder{ResponseWriter: w}
		c := &call{}
		addr := srv.clientAddr(r)
		defer func() {
			if v := recover(); v != nil {
				srv.opt.Log.Error("panic", "request", id, "endpoint", endpoint, "panic", fmt.Sprint(v))
				if rec.code == 0 {
					reply(rec, http.StatusInternalServerError, map[string]string{"error": "internal error", "request": id})
				}
			}
			srv.finish(endpoint, id, addr, r, rec, c, time.Since(start))
		}()
		p, err := srv.opt.Auth.Authenticate(r)
		if err != nil {
			srv.Metrics.authFailure()
			// Unauthenticated callers share one bucket per address, so
			// guessing tokens is rate limited too.
			if srv.limiter != nil && !srv.limiter.allow("addr:"+addr) {
				srv.rateLimited(rec)
				return
			}
			reply(rec, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		c.p = p
		if srv.limiter != nil && !srv.limiter.allow("token:"+p.ID) {
			srv.rateLimited(rec)
			return
		}
		h(rec, r, c)
	})
}

func (srv *Server) rateLimited(w http.ResponseWriter) {
	srv.Metrics.rateLimited()
	w.Header().Set("Retry-After", "1")
	reply(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded; retry after 1 s"})
}

type auditLine struct {
	Time       string  `json:"time"`
	Request    string  `json:"request"`
	Endpoint   string  `json:"endpoint"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Status     int     `json:"status"`
	Client     string  `json:"client"`
	TokenID    string  `json:"tokenId,omitempty"`
	TokenName  string  `json:"tokenName,omitempty"`
	Repo       string  `json:"repo,omitempty"`
	SHA        string  `json:"sha,omitempty"`
	Repos      int     `json:"repos,omitempty"`
	Unreadable int     `json:"unreadable,omitempty"`
	BytesIn    int64   `json:"bytesIn,omitempty"`
	BytesOut   int64   `json:"bytesOut"`
	Millis     float64 `json:"ms"`
	Note       string  `json:"note,omitempty"`
}

func (srv *Server) finish(endpoint, id, addr string, r *http.Request, rec *recorder, c *call, d time.Duration) {
	code := rec.code
	if code == 0 {
		code = http.StatusOK
	}
	srv.Metrics.observe(endpoint, code, d)
	if endpoint == "upload" {
		srv.Metrics.upload(code)
	}
	if srv.opt.Audit == nil {
		return
	}
	line := auditLine{Time: time.Now().UTC().Format(time.RFC3339Nano), Request: id, Endpoint: endpoint,
		Method: r.Method, Path: r.URL.Path, Status: code, Client: addr, Repo: c.repo, SHA: c.sha,
		Repos: c.repos, Unreadable: c.unreadable, BytesIn: c.bytesIn, BytesOut: rec.bytes,
		Millis: math.Round(float64(d.Microseconds())/10) / 100, Note: c.note}
	if c.p != nil {
		line.TokenID, line.TokenName = c.p.ID, c.p.Name
	}
	data, _ := json.Marshal(line)
	srv.auditMu.Lock()
	_, err := srv.opt.Audit.Write(append(data, '\n'))
	srv.auditMu.Unlock()
	if err != nil {
		srv.opt.Log.Error("audit write failed", "err", err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, c *call, v any) bool {
	cr := &countingReader{r: http.MaxBytesReader(w, r.Body, limit)}
	err := json.NewDecoder(cr).Decode(v)
	c.bytesIn = cr.n
	if err != nil {
		code := http.StatusBadRequest
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			code = http.StatusRequestEntityTooLarge
		}
		reply(w, code, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func (srv *Server) handleUpload(w http.ResponseWriter, r *http.Request, c *call) {
	var b Bundle
	if !decode(w, r, srv.opt.MaxBundleBytes, c, &b) {
		return
	}
	c.repo, c.sha = b.Repo, b.SHA
	if !c.p.CanUpload(b.Repo) {
		reply(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("token %q may not upload %q", c.p.Name, b.Repo)})
		return
	}
	if err := srv.store.PutContext(r.Context(), b); err != nil {
		code := http.StatusBadRequest
		if !isClientError(err) {
			code = http.StatusInternalServerError
			srv.opt.Log.Error("upload failed", "repo", b.Repo, "sha", b.SHA, "err", err)
		}
		reply(w, code, map[string]string{"error": err.Error()})
		return
	}
	reply(w, http.StatusOK, map[string]any{"stored": b.Repo + "@" + b.SHA, "refs": len(b.Refs), "defs": len(b.Defs)})
}

// isClientError tells a bad bundle (400) from a storage failure (500).
func isClientError(err error) bool {
	msg := err.Error()
	for _, s := range []string{"bundle needs", "uncommitted changes", "newer than this server", "stored under the same key", "bundle field"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (srv *Server) handleConsumers(w http.ResponseWriter, r *http.Request, c *call) {
	var req ConsumersRequest
	if !decode(w, r, 8<<20, c, &req) {
		return
	}
	resp := srv.store.ConsumersFor(r.Context(), req, c.p.CanRead)
	c.repos, c.unreadable = len(resp.Repos), resp.Unreadable
	reply(w, http.StatusOK, resp)
}

func (srv *Server) handleDefine(w http.ResponseWriter, r *http.Request, c *call) {
	var req struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if !decode(w, r, 1<<20, c, &req) {
		return
	}
	repos, hidden := srv.store.DefineFor(r.Context(), req.Key, req.Name, c.p.CanRead)
	c.repos, c.unreadable = len(repos), hidden
	out := map[string]any{"repos": repos}
	if hidden > 0 {
		out["unreadable"] = hidden
	}
	reply(w, http.StatusOK, out)
}

// RepoSummary is one row of GET /link/repos.
type RepoSummary struct {
	Repo    string            `json:"repo"`
	Refs    map[string]string `json:"refs"`
	Latest  string            `json:"latest"`
	Commits int               `json:"commits"`
}

func (srv *Server) handleRepos(w http.ResponseWriter, _ *http.Request, c *call) {
	out := []RepoSummary{}
	for _, r := range srv.store.Repos() {
		if c.p.CanRead(r.Repo) {
			out = append(out, r)
		}
	}
	c.repos = len(out)
	reply(w, http.StatusOK, map[string]any{"repos": out})
}

func (srv *Server) handleDelete(w http.ResponseWriter, r *http.Request, c *call) {
	var req struct {
		Repo string `json:"repo"`
		SHA  string `json:"sha"`
	}
	if !decode(w, r, 1<<20, c, &req) {
		return
	}
	c.repo, c.sha = req.Repo, req.SHA
	if !c.p.Admin {
		reply(w, http.StatusForbidden, map[string]string{"error": "deleting needs an admin token"})
		return
	}
	if req.SHA != "" {
		if err := srv.store.DeleteCommit(r.Context(), req.Repo, req.SHA); err != nil {
			reply(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]any{"deleted": req.Repo + "@" + req.SHA})
		return
	}
	n, err := srv.store.DeleteRepo(r.Context(), req.Repo)
	if err != nil {
		reply(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	reply(w, http.StatusOK, map[string]any{"deleted": req.Repo, "objects": n})
}

func (srv *Server) handlePrune(w http.ResponseWriter, r *http.Request, c *call) {
	var req struct {
		Repo string `json:"repo"` // empty: every repository
		Keep int    `json:"keep"`
	}
	if !decode(w, r, 1<<20, c, &req) {
		return
	}
	c.repo = req.Repo
	if !c.p.Admin {
		reply(w, http.StatusForbidden, map[string]string{"error": "retention needs an admin token"})
		return
	}
	if req.Keep <= 0 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "keep must be at least 1"})
		return
	}
	repos := []string{req.Repo}
	if req.Repo == "" {
		repos = nil
		for _, s := range srv.store.Repos() {
			repos = append(repos, s.Repo)
		}
	}
	deleted := map[string][]string{}
	for _, repo := range repos {
		d, err := srv.store.Prune(r.Context(), repo, req.Keep)
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(d) > 0 {
			deleted[repo] = d
		}
	}
	c.note = "pruned " + strconv.Itoa(len(deleted)) + " repositories"
	reply(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// Repos lists the catalog.
func (s *Store) Repos() []RepoSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoSummary, 0, len(s.repos))
	for _, e := range s.repos {
		out = append(out, RepoSummary{Repo: e.refs.Repo, Refs: e.refs.Refs, Latest: e.refs.Latest, Commits: len(e.refs.History)})
	}
	sortRepoSummaries(out)
	return out
}

func sortRepoSummaries(s []RepoSummary) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Repo < s[j-1].Repo; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// limiter is a token bucket per key.
type limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.swept) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ReopenFile is an audit destination that reopens its file on Reopen (call
// it on SIGHUP after logrotate moves the file).
type ReopenFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// OpenAuditFile opens path for appending.
func OpenAuditFile(path string) (*ReopenFile, error) {
	r := &ReopenFile{path: path}
	return r, r.Reopen()
}

func (r *ReopenFile) Reopen() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old := r.f
	r.f = f
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

func (r *ReopenFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Write(p)
}
