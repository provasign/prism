package graphui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SEC-09: with a token set, /api/* needs it; the page itself does not.
func TestAPITokenRequired(t *testing.T) {
	s := &Server{token: "secret"}
	h := s.Handler()
	do := func(path, tok string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:1"+path, nil)
		if tok != "" {
			r.Header.Set("X-Prism-Token", tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("/api/meta", ""); c != http.StatusUnauthorized {
		t.Fatalf("no token: %d", c)
	}
	if c := do("/api/meta", "wrong"); c != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do("/", ""); c != http.StatusOK {
		t.Fatalf("page without token: %d", c)
	}
}
