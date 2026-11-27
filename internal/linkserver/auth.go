package linkserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Principal is the caller a credential stands for.
type Principal struct {
	ID     string // token id; "shared" for PRISM_LINK_TOKEN, "anonymous" with --no-auth
	Name   string
	Upload []string // repository-name globs it may upload
	Read   []string // repository-name globs it may read
	Admin  bool     // may delete repositories and commits, and run retention
}

func matchAny(globs []string, repo string) bool {
	for _, g := range globs {
		if g == "*" || g == repo {
			return true
		}
		if ok, err := path.Match(g, repo); err == nil && ok {
			return true
		}
	}
	return false
}

// CanUpload reports whether p may store bundles under repo.
func (p *Principal) CanUpload(repo string) bool { return p != nil && matchAny(p.Upload, repo) }

// CanRead reports whether p may read repo's sites and declarations.
func (p *Principal) CanRead(repo string) bool { return p != nil && matchAny(p.Read, repo) }

var everything = []string{"*"}

// ErrUnauthenticated is a missing, unknown, revoked or expired credential.
var ErrUnauthenticated = errors.New("missing, unknown, revoked or expired token")

// Authenticator turns a request's credential into a Principal.
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
	Describe() string
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// OpenAuth lets every request in with full rights (--no-auth).
type OpenAuth struct{}

func (OpenAuth) Authenticate(*http.Request) (*Principal, error) {
	return &Principal{ID: "anonymous", Name: "no authentication", Upload: everything, Read: everything, Admin: true}, nil
}
func (OpenAuth) Describe() string { return "NO AUTHENTICATION (--no-auth)" }

// SharedToken is the v1.3 model: one secret with every right.
type SharedToken string

func (t SharedToken) Authenticate(r *http.Request) (*Principal, error) {
	if b := bearer(r); b != "" && constantEq(b, string(t)) {
		return &Principal{ID: "shared", Name: "PRISM_LINK_TOKEN", Upload: everything, Read: everything, Admin: true}, nil
	}
	return nil, ErrUnauthenticated
}
func (SharedToken) Describe() string { return "one shared token (PRISM_LINK_TOKEN)" }

// Token is one entry of a token file. The secret itself is never stored.
type Token struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Hash    string   `json:"hash"` // "sha256:<hex>" of the secret
	Upload  []string `json:"upload,omitempty"`
	Read    []string `json:"read,omitempty"`
	Admin   bool     `json:"admin,omitempty"`
	Created string   `json:"created"`
	Expires string   `json:"expires,omitempty"` // RFC 3339; empty never expires
}

type tokenFile struct {
	Tokens []Token `json:"tokens"`
}

// TokenFileAuth authenticates against a token file, re-reading it when it
// changes, so a created or revoked token takes effect without a restart.
type TokenFileAuth struct {
	path string
	// Fallback, when set, also accepts the shared PRISM_LINK_TOKEN.
	Fallback Authenticator

	mu      sync.Mutex
	checked time.Time
	mod     time.Time
	size    int64
	byID    map[string]Token
	loadErr error
}

// OpenTokenFile reads a token file; it must exist and parse.
func OpenTokenFile(p string) (*TokenFileAuth, error) {
	a := &TokenFileAuth{path: p}
	if err := a.reload(true); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *TokenFileAuth) Describe() string {
	a.mu.Lock()
	n := len(a.byID)
	a.mu.Unlock()
	d := fmt.Sprintf("token file %s (%d tokens)", a.path, n)
	if a.Fallback != nil {
		d += " and " + a.Fallback.Describe()
	}
	return d
}

func (a *TokenFileAuth) reload(force bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !force && time.Since(a.checked) < time.Second {
		return a.loadErr
	}
	a.checked = time.Now()
	fi, err := os.Stat(a.path)
	if err != nil {
		a.loadErr = err
		return err
	}
	if !force && fi.ModTime().Equal(a.mod) && fi.Size() == a.size {
		return a.loadErr
	}
	f, err := readTokenFile(a.path)
	if err != nil {
		// Keep serving the last good file: a half-written edit must not
		// lock everyone out.
		a.loadErr = err
		if a.byID == nil {
			return err
		}
		return nil
	}
	a.byID = map[string]Token{}
	for _, t := range f.Tokens {
		a.byID[t.ID] = t
	}
	a.mod, a.size, a.loadErr = fi.ModTime(), fi.Size(), nil
	return nil
}

func (a *TokenFileAuth) Authenticate(r *http.Request) (*Principal, error) {
	_ = a.reload(false)
	secret := bearer(r)
	id, ok := tokenID(secret)
	if ok {
		a.mu.Lock()
		t, found := a.byID[id]
		a.mu.Unlock()
		if found && constantEq(t.Hash, hashSecret(secret)) {
			if t.Expires != "" {
				if exp, err := time.Parse(time.RFC3339, t.Expires); err != nil || time.Now().After(exp) {
					return nil, ErrUnauthenticated
				}
			}
			return &Principal{ID: t.ID, Name: t.Name, Upload: t.Upload, Read: t.Read, Admin: t.Admin}, nil
		}
		return nil, ErrUnauthenticated
	}
	if a.Fallback != nil {
		return a.Fallback.Authenticate(r)
	}
	return nil, ErrUnauthenticated
}

const secretPrefix = "plk_"

func tokenID(secret string) (string, bool) {
	rest, ok := strings.CutPrefix(secret, secretPrefix)
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, "_")
	return id, ok && id != ""
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readTokenFile(p string) (tokenFile, error) {
	var f tokenFile
	data, err := os.ReadFile(p)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("token file %s: %w", p, err)
	}
	return f, nil
}

func editTokenFile(p string, fn func(*tokenFile) error) error {
	unlock, err := lockFile(context.Background(), p+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	f, err := readTokenFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := fn(&f); err != nil {
		return err
	}
	sort.Slice(f.Tokens, func(i, j int) bool { return f.Tokens[i].Created < f.Tokens[j].Created })
	data, _ := json.MarshalIndent(f, "", "  ")
	return writeFileAtomic(p, append(data, '\n'))
}

// TokenSpec is what a new token may do.
type TokenSpec struct {
	Name    string
	Upload  []string
	Read    []string
	Admin   bool
	Expires time.Duration // 0: never
}

// CreateToken adds a token to the file (creating it) and returns the secret,
// which is shown once and stored only as a hash.
func CreateToken(p string, spec TokenSpec) (string, Token, error) {
	if spec.Name == "" {
		return "", Token{}, errors.New("a token needs --name")
	}
	if len(spec.Upload) == 0 && len(spec.Read) == 0 && !spec.Admin {
		return "", Token{}, errors.New("a token needs at least one of --upload, --read or --admin")
	}
	for _, g := range append(append([]string{}, spec.Upload...), spec.Read...) {
		if _, err := path.Match(g, ""); err != nil {
			return "", Token{}, fmt.Errorf("bad repository pattern %q: %v", g, err)
		}
	}
	idb := make([]byte, 6)
	sec := make([]byte, 32)
	if _, err := rand.Read(idb); err != nil {
		return "", Token{}, err
	}
	if _, err := rand.Read(sec); err != nil {
		return "", Token{}, err
	}
	id := hex.EncodeToString(idb)
	secret := secretPrefix + id + "_" + base64.RawURLEncoding.EncodeToString(sec)
	t := Token{ID: id, Name: spec.Name, Hash: hashSecret(secret), Upload: spec.Upload, Read: spec.Read,
		Admin: spec.Admin, Created: time.Now().UTC().Format(time.RFC3339)}
	if spec.Expires > 0 {
		// Whole seconds, rounded up: a token never expires before it was
		// asked to.
		exp := time.Now().Add(spec.Expires)
		if exp.Nanosecond() != 0 {
			exp = exp.Truncate(time.Second).Add(time.Second)
		}
		t.Expires = exp.UTC().Format(time.RFC3339)
	}
	err := editTokenFile(p, func(f *tokenFile) error {
		f.Tokens = append(f.Tokens, t)
		return nil
	})
	return secret, t, err
}

// ListTokens returns the file's tokens (no secrets: only hashes are stored).
func ListTokens(p string) ([]Token, error) {
	f, err := readTokenFile(p)
	return f.Tokens, err
}

// RevokeToken removes a token by id.
func RevokeToken(p, id string) error {
	return editTokenFile(p, func(f *tokenFile) error {
		for i, t := range f.Tokens {
			if t.ID == id {
				f.Tokens = append(f.Tokens[:i], f.Tokens[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no token with id %q", id)
	})
}
