// Package httpguard protects Prism's local HTTP listeners (prism serve,
// prism graph) from other origins on the same machine: DNS rebinding,
// cross-site form posts, and other local users (SEC-02).
package httpguard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// MaxBody bounds a request body.
const MaxBody = 1 << 20

// LoopbackHost reports whether a Host header names a loopback address.
func LoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewToken returns 32 random bytes, hex-encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Options configures Wrap.
type Options struct {
	// Token is required as "Authorization: Bearer <Token>" on every route
	// except Open. Empty disables the check.
	Token string
	// AllowHost, when set, accepts a non-loopback Host (a --listen address
	// the operator chose). Loopback names are always accepted.
	AllowHost func(host string) bool
	// Open lists paths served without a token (still Host-checked).
	Open map[string]bool
}

// Wrap rejects requests that did not come from this machine's own client:
//   - Host must be loopback (or allowed): defeats DNS rebinding;
//   - Origin, when present, must be http://<Host>: defeats cross-site calls;
//   - POST bodies must be application/json: a cross-site form or text/plain
//     POST needs no preflight, a JSON one does;
//   - the bearer token must match: other local users and processes;
//   - bodies are capped at MaxBody.
func Wrap(next http.Handler, o Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !LoopbackHost(r.Host) && (o.AllowHost == nil || !o.AllowHost(r.Host)) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
			if ct != "application/json" {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		if o.Token != "" && !o.Open[r.URL.Path] {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(o.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
				return
			}
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		}
		next.ServeHTTP(w, r)
	})
}
