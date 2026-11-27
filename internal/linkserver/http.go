package linkserver

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// Handler serves the store over HTTP with one shared token (the v1.3
// model); an empty token serves everyone. Server builds the full handler
// with per-token scopes, audit, limits and metrics.
func (s *Store) Handler(token string) http.Handler {
	var auth Authenticator = OpenAuth{}
	if token != "" {
		auth = SharedToken(token)
	}
	return NewServer(s, ServerOptions{Auth: auth}).Handler()
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// constantEq compares secrets without leaking their length through time.
func constantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
