// Package httpapi exposes the Prism MCP tools over plain HTTP for clients
// that don't speak JSON-RPC stdio (e.g., curl, the VS Code extension's
// child-process fallback path).
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/tabladrum/grove-suite/prism/internal/mcp"
	"github.com/tabladrum/grove-suite/prism/internal/version"
)

// Server wraps a Prism MCP handler with HTTP routes.
type Server struct {
	h *mcp.Handler
}

// New constructs the HTTP server.
func New(h *mcp.Handler) *Server { return &Server{h: h} }

// Handler returns the http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /status", s.savings) // alias

	for _, name := range []string{
		"prism_query", "prism_read", "prism_search", "prism_lookup",
		"prism_index", "prism_compact", "prism_savings", "prism_feedback",
	} {
		n := name
		mux.HandleFunc("POST /"+n, func(w http.ResponseWriter, r *http.Request) {
			s.callTool(w, r, n)
		})
	}
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version.Version})
}

func (s *Server) savings(w http.ResponseWriter, _ *http.Request) {
	out, err := s.h.Invoke("prism_savings", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) callTool(w http.ResponseWriter, r *http.Request, name string) {
	var args map[string]any
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if r.ContentLength > 0 {
		if err := dec.Decode(&args); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	out, err := s.h.Invoke(name, args)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
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
