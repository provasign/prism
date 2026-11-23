package httpguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrapRejectsForeignRequests(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, MaxBody+10)
		n := 0
		for {
			m, err := r.Body.Read(buf[n:])
			n += m
			if err != nil {
				if !strings.Contains(err.Error(), "EOF") {
					http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
					return
				}
				break
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	h := Wrap(ok, Options{Token: "tok", Open: map[string]bool{"/health": true}})
	req := func(method, path, host, origin, ct, auth, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	cases := []struct {
		name                           string
		method, path, host, origin, ct string
		auth, body                     string
		want                           int
	}{
		{"good", "POST", "/prism_read", "127.0.0.1:8888", "", "application/json", "Bearer tok", "{}", 200},
		{"rebinding host", "POST", "/prism_read", "evil.example:8888", "", "application/json", "Bearer tok", "{}", 403},
		{"foreign origin", "POST", "/prism_read", "127.0.0.1:8888", "http://evil.example", "application/json", "Bearer tok", "{}", 403},
		{"text/plain post", "POST", "/prism_index", "127.0.0.1:8888", "", "text/plain", "Bearer tok", "{}", 415},
		{"no token", "POST", "/prism_read", "127.0.0.1:8888", "", "application/json", "", "{}", 401},
		{"wrong token", "POST", "/prism_read", "localhost:8888", "", "application/json", "Bearer nope", "{}", 401},
		{"oversized body", "POST", "/prism_read", "127.0.0.1:8888", "", "application/json", "Bearer tok", strings.Repeat("x", MaxBody+5), 413},
		{"health open", "GET", "/health", "127.0.0.1:8888", "", "", "", "", 200},
		{"health still host-checked", "GET", "/health", "evil.example", "", "", "", "", 403},
		{"same origin", "POST", "/prism_read", "127.0.0.1:8888", "http://127.0.0.1:8888", "application/json; charset=utf-8", "Bearer tok", "{}", 200},
	}
	for _, c := range cases {
		if got := req(c.method, c.path, c.host, c.origin, c.ct, c.auth, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
