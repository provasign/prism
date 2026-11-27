package linkserver

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GCSConfig selects a Google Cloud Storage bucket: gs://bucket[/prefix].
// Credentials, first match wins:
//   - STORAGE_EMULATOR_HOST: an emulator (fake-gcs-server), no credentials
//   - GOOGLE_APPLICATION_CREDENTIALS: a service-account key file
//   - otherwise the metadata server (GCE, GKE workload identity, Cloud Run)
type GCSConfig struct {
	URL      string
	Endpoint string // default https://storage.googleapis.com, or STORAGE_EMULATOR_HOST
}

// GCSBlob is a Blob on a GCS bucket through the JSON API. The ETag is the
// object generation; PutIf uses ifGenerationMatch (0: must not exist), so
// several replicas can share the bucket.
type GCSBlob struct {
	bucket, prefix string
	base           string
	token          func(ctx context.Context) (string, error)
	client         *http.Client
}

// OpenGCS checks the bucket is reachable with the credentials.
func OpenGCS(ctx context.Context, c GCSConfig) (*GCSBlob, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "gs" || u.Host == "" {
		return nil, fmt.Errorf("--gcs needs gs://bucket[/prefix], got %q", c.URL)
	}
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	b := &GCSBlob{bucket: u.Host, prefix: prefix, client: &http.Client{Timeout: 2 * time.Minute}}
	switch emu := os.Getenv("STORAGE_EMULATOR_HOST"); {
	case c.Endpoint != "":
		b.base = strings.TrimRight(c.Endpoint, "/")
	case emu != "":
		if !strings.Contains(emu, "://") {
			emu = "http://" + emu
		}
		b.base = strings.TrimRight(emu, "/")
	default:
		b.base = "https://storage.googleapis.com"
	}
	switch {
	case os.Getenv("STORAGE_EMULATOR_HOST") != "":
		b.token = func(context.Context) (string, error) { return "", nil }
	case os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "":
		ts, err := serviceAccountTokens(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
		if err != nil {
			return nil, err
		}
		b.token = ts
	default:
		b.token = metadataTokens()
	}
	if _, err := b.List(ctx, probePrefix); err != nil {
		return nil, fmt.Errorf("GCS bucket %s: %w", u.Host, err)
	}
	return b, nil
}

func (b *GCSBlob) Describe() string {
	return fmt.Sprintf("gs://%s/%s at %s", b.bucket, b.prefix, b.base)
}

func (b *GCSBlob) do(ctx context.Context, method, rawURL string, body []byte, hdr map[string]string) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	tok, err := b.token(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("GCS token: %w", err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	return resp, data, err
}

func gcsError(resp *http.Response, data []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("GCS %s: %s", resp.Status, e.Error.Message)
	}
	return fmt.Errorf("GCS %s", resp.Status)
}

func (b *GCSBlob) objectPath(key string) string {
	return b.base + "/storage/v1/b/" + url.PathEscape(b.bucket) + "/o/" + url.PathEscape(b.prefix+key)
}

func (b *GCSBlob) Get(ctx context.Context, key, ifNoneMatch string) ([]byte, string, error) {
	q := url.Values{"alt": {"media"}}
	if ifNoneMatch != "" {
		q.Set("ifGenerationNotMatch", ifNoneMatch)
	}
	resp, data, err := b.do(ctx, http.MethodGet, b.objectPath(key)+"?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		gen := resp.Header.Get("X-Goog-Generation")
		if ifNoneMatch != "" && gen == ifNoneMatch {
			// Servers that ignore ifGenerationNotMatch on media reads.
			return nil, gen, ErrNotModified
		}
		return data, gen, nil
	case http.StatusNotModified:
		return nil, ifNoneMatch, ErrNotModified
	case http.StatusNotFound:
		return nil, "", ErrNotFound
	}
	return nil, "", gcsError(resp, data)
}

func (b *GCSBlob) upload(ctx context.Context, key string, data []byte, q url.Values) (*http.Response, []byte, error) {
	q.Set("uploadType", "media")
	q.Set("name", b.prefix+key)
	u := b.base + "/upload/storage/v1/b/" + url.PathEscape(b.bucket) + "/o?" + q.Encode()
	return b.do(ctx, http.MethodPost, u, data, map[string]string{"Content-Type": "application/json"})
}

func generationOf(data []byte) string {
	var o struct {
		Generation json.Number `json:"generation"`
	}
	_ = json.Unmarshal(data, &o)
	return o.Generation.String()
}

func (b *GCSBlob) Put(ctx context.Context, key string, data []byte) error {
	resp, body, err := b.upload(ctx, key, data, url.Values{})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return gcsError(resp, body)
	}
	return nil
}

func (b *GCSBlob) PutIf(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	gen := ifMatch
	if gen == "" {
		gen = "0" // the object must not exist
	}
	if _, err := strconv.ParseInt(gen, 10, 64); err != nil {
		return "", ErrConflict
	}
	resp, body, err := b.upload(ctx, key, data, url.Values{"ifGenerationMatch": {gen}})
	if err != nil {
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return generationOf(body), nil
	case http.StatusPreconditionFailed, http.StatusConflict:
		return "", ErrConflict
	}
	return "", gcsError(resp, body)
}

func (b *GCSBlob) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	page := ""
	for {
		q := url.Values{"prefix": {b.prefix + prefix}, "fields": {"items(name),nextPageToken"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		resp, data, err := b.do(ctx, http.MethodGet, b.base+"/storage/v1/b/"+url.PathEscape(b.bucket)+"/o?"+q.Encode(), nil, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, gcsError(resp, data)
		}
		var r struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			out = append(out, strings.TrimPrefix(it.Name, b.prefix))
		}
		if r.NextPageToken == "" {
			break
		}
		page = r.NextPageToken
	}
	sort.Strings(out)
	return out, nil
}

func (b *GCSBlob) Delete(ctx context.Context, key string) error {
	resp, data, err := b.do(ctx, http.MethodDelete, b.objectPath(key), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return gcsError(resp, data)
	}
	return nil
}

// --- credentials

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

type cachedToken struct {
	mu    sync.Mutex
	tok   string
	exp   time.Time
	fetch func(ctx context.Context) (string, time.Time, error)
}

func (c *cachedToken) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok == "" || time.Until(c.exp) < 5*time.Minute {
		t, exp, err := c.fetch(ctx)
		if err != nil {
			return "", err
		}
		c.tok, c.exp = t, exp
	}
	return c.tok, nil
}

// serviceAccountTokens signs a JWT with a service-account key and trades it
// for an access token (OAuth 2.0 JWT bearer grant).
func serviceAccountTokens(file string) (func(context.Context) (string, error), error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var sa struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	if sa.Type != "service_account" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS: type %q is not supported (use a service_account key, or workload identity through the metadata server)", sa.Type)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("GOOGLE_APPLICATION_CREDENTIALS: no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GOOGLE_APPLICATION_CREDENTIALS: the key is not RSA")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	ct := &cachedToken{fetch: func(ctx context.Context) (string, time.Time, error) {
		now := time.Now()
		enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
		head := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
		claims := enc(map[string]any{"iss": sa.ClientEmail, "scope": gcsScope, "aud": sa.TokenURI,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
		sum := sha256.Sum256([]byte(head + "." + claims))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if err != nil {
			return "", time.Time{}, err
		}
		form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion": {head + "." + claims + "." + base64.RawURLEncoding.EncodeToString(sig)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, strings.NewReader(form.Encode()))
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return "", time.Time{}, err
		}
		return tokenReply(resp)
	}}
	return ct.get, nil
}

// metadataTokens gets the attached service account's token from the
// metadata server (GCE, GKE workload identity, Cloud Run).
func metadataTokens() func(context.Context) (string, error) {
	host := os.Getenv("GCE_METADATA_HOST")
	if host == "" {
		host = "metadata.google.internal"
	}
	ct := &cachedToken{fetch: func(ctx context.Context) (string, time.Time, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"http://"+host+"/computeMetadata/v1/instance/service-accounts/default/token?scopes="+url.QueryEscape(gcsScope), nil)
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("no GCS credentials: set GOOGLE_APPLICATION_CREDENTIALS, or run where the metadata server is reachable (%v)", err)
		}
		return tokenReply(resp)
	}}
	return ct.get
}
