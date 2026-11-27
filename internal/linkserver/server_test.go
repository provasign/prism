package linkserver

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func tokenServer(t *testing.T, o ServerOptions) (*Store, *Server, *httptest.Server, string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tf := filepath.Join(t.TempDir(), "tokens.json")
	if _, _, err := CreateToken(tf, TokenSpec{Name: "seed", Read: []string{"none"}}); err != nil {
		t.Fatal(err)
	}
	auth, err := OpenTokenFile(tf)
	if err != nil {
		t.Fatal(err)
	}
	o.Auth = auth
	srv := NewServer(s, o)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return s, srv, hs, tf
}

func mustToken(t *testing.T, tf string, spec TokenSpec) string {
	t.Helper()
	secret, _, err := CreateToken(tf, spec)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // the server re-reads the file at most once a second
	return secret
}

func bundleJSON(repo, sha string) string {
	b, _ := json.Marshal(Bundle{Format: 1, Repo: repo, SHA: sha, Ref: "main", Created: "2026-11-27T00:00:00Z",
		Refs: []Site{{Key: "go:example.com/lib#F", File: "a.go", Line: 1, Text: "lib.F()"}}})
	return string(b)
}

// A CI token uploads only its own repository; a team token reads only its
// repositories, and the answer counts what it could not read.
func TestTokenScopes(t *testing.T) {
	_, _, hs, tf := tokenServer(t, ServerOptions{})
	ciApp := mustToken(t, tf, TokenSpec{Name: "ci-app", Upload: []string{"example.org/team-a/app"}})
	ciSvc := mustToken(t, tf, TokenSpec{Name: "ci-svc", Upload: []string{"example.org/team-b/*"}})
	readA := mustToken(t, tf, TokenSpec{Name: "team-a", Read: []string{"example.org/team-a/*"}})

	if code, out := postJSON(t, hs.URL+"/link/bundle", ciApp, bundleJSON("example.org/team-a/app", "c1")); code != 200 {
		t.Fatalf("own upload: %d %s", code, out)
	}
	if code, out := postJSON(t, hs.URL+"/link/bundle", ciApp, bundleJSON("example.org/team-a/other", "c1")); code != http.StatusForbidden {
		t.Fatalf("upload of another repository: %d %s", code, out)
	}
	if code, _ := postJSON(t, hs.URL+"/link/bundle", ciSvc, bundleJSON("example.org/team-b/svc", "c2")); code != 200 {
		t.Fatalf("glob upload: %d", code)
	}
	// An upload-only token reads nothing.
	code, out := postJSON(t, hs.URL+"/link/consumers", ciApp, `{"keys":["go:example.com/lib#F"]}`)
	if code != 200 || !strings.Contains(out, `"repos":[]`) || !strings.Contains(out, `"unreadable":2`) {
		t.Fatalf("upload-only token reading: %d %s", code, out)
	}
	code, out = postJSON(t, hs.URL+"/link/consumers", readA, `{"keys":["go:example.com/lib#F"]}`)
	if code != 200 || !strings.Contains(out, "example.org/team-a/app") || strings.Contains(out, "team-b") || !strings.Contains(out, `"unreadable":1`) {
		t.Fatalf("team-a read: %d %s", code, out)
	}
	code, out = postJSON(t, hs.URL+"/link/define", readA, `{"name":"nothing"}`)
	if code != 200 || !strings.Contains(out, `"unreadable":1`) {
		t.Fatalf("define: %d %s", code, out)
	}
	// Admin endpoints need admin.
	if code, _ := postJSON(t, hs.URL+"/link/delete", readA, `{"repo":"example.org/team-a/app"}`); code != http.StatusForbidden {
		t.Fatalf("delete without admin: %d", code)
	}
}

func TestRevokedAndExpiredTokensAreRefused(t *testing.T) {
	_, _, hs, tf := tokenServer(t, ServerOptions{})
	secret, tok, err := CreateToken(tf, TokenSpec{Name: "short", Read: []string{"*"}, Expires: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if code, _ := postJSON(t, hs.URL+"/link/consumers", secret, `{"keys":[]}`); code != 200 {
		t.Fatalf("fresh token: %d", code)
	}
	time.Sleep(2 * time.Second)
	if code, _ := postJSON(t, hs.URL+"/link/consumers", secret, `{"keys":[]}`); code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", code)
	}
	other := mustToken(t, tf, TokenSpec{Name: "other", Read: []string{"*"}})
	if err := RevokeToken(tf, tok.ID); err != nil {
		t.Fatal(err)
	}
	ts, _ := ListTokens(tf)
	var otherID string
	for _, x := range ts {
		if x.Name == "other" {
			otherID = x.ID
		}
	}
	if err := RevokeToken(tf, otherID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if code, _ := postJSON(t, hs.URL+"/link/consumers", other, `{"keys":[]}`); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
	if data, _ := os.ReadFile(tf); bytes.Contains(data, []byte(other)) || bytes.Contains(data, []byte(secret)) {
		t.Fatal("the token file holds a secret")
	}
	if fi, _ := os.Stat(tf); fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", fi.Mode().Perm())
	}
}

// One audit line per request, naming the token but never its secret.
func TestAuditLog(t *testing.T) {
	var audit syncBuf
	_, _, hs, tf := tokenServer(t, ServerOptions{Audit: &audit})
	ci := mustToken(t, tf, TokenSpec{Name: "ci-app", Upload: []string{"example.org/app"}})
	postJSON(t, hs.URL+"/link/bundle", ci, bundleJSON("example.org/app", "c1"))
	postJSON(t, hs.URL+"/link/consumers", "plk_bogus_x", `{"keys":[]}`)
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit lines: %q", audit.String())
	}
	var up, bad auditLine
	if json.Unmarshal([]byte(lines[0]), &up) != nil || json.Unmarshal([]byte(lines[1]), &bad) != nil {
		t.Fatalf("audit lines are not JSON: %q", lines)
	}
	if up.Endpoint != "upload" || up.Status != 200 || up.TokenName != "ci-app" || up.Repo != "example.org/app" || up.SHA != "c1" || up.BytesIn == 0 || up.Request == "" {
		t.Fatalf("upload line: %+v", up)
	}
	if bad.Status != 401 || bad.TokenID != "" {
		t.Fatalf("auth failure line: %+v", bad)
	}
	if strings.Contains(audit.String(), ci) {
		t.Fatal("the audit log holds a secret")
	}
}

func TestRateLimit(t *testing.T) {
	_, _, hs, tf := tokenServer(t, ServerOptions{Rate: 2})
	tok := mustToken(t, tf, TokenSpec{Name: "r", Read: []string{"*"}})
	limited := 0
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/link/consumers", strings.NewReader(`{"keys":[]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode == http.StatusTooManyRequests {
			limited++
			if r.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
		r.Body.Close()
	}
	if limited < 5 { // burst 4, then refusals
		t.Fatalf("only %d of 10 rapid requests were limited", limited)
	}
}

func TestMetricsAndHealth(t *testing.T) {
	_, srv, hs, tf := tokenServer(t, ServerOptions{})
	tok := mustToken(t, tf, TokenSpec{Name: "ci", Upload: []string{"*"}})
	postJSON(t, hs.URL+"/link/bundle", tok, bundleJSON("example.org/app", "c1"))
	postJSON(t, hs.URL+"/link/bundle", "nope", bundleJSON("example.org/app", "c1"))
	ops := httptest.NewServer(srv.OpsHandler())
	defer ops.Close()
	r, err := http.Get(ops.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	for _, want := range []string{
		`prism_link_requests_total{endpoint="upload",code="200"} 1`,
		`prism_link_requests_total{endpoint="upload",code="401"} 1`,
		`prism_link_uploads_total{result="ok"} 1`,
		`prism_link_auth_failures_total 1`,
		`prism_link_repositories 1`,
		`prism_link_request_seconds_count{endpoint="upload"} 2`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	r, _ = http.Get(hs.URL + "/health")
	hb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(hb), `"formats":[0,1]`) {
		t.Fatalf("health: %s", hb)
	}
}

func TestFormatVersionAndUploadLimit(t *testing.T) {
	_, _, hs, tf := tokenServer(t, ServerOptions{MaxBundleBytes: 1 << 10})
	tok := mustToken(t, tf, TokenSpec{Name: "ci", Upload: []string{"*"}})
	newer := strings.Replace(bundleJSON("example.org/app", "c1"), `"format":1`, fmt.Sprintf(`"format":%d`, FormatVersion+1), 1)
	if code, out := postJSON(t, hs.URL+"/link/bundle", tok, newer); code != 400 || !strings.Contains(out, "upgrade the link server") {
		t.Fatalf("newer format: %d %s", code, out)
	}
	legacy := strings.Replace(bundleJSON("example.org/app", "c1"), `"format":1,`, ``, 1)
	if code, out := postJSON(t, hs.URL+"/link/bundle", tok, legacy); code != 200 {
		t.Fatalf("v1.3 bundle (no format): %d %s", code, out)
	}
	big := `{"repo":"example.org/app","sha":"c2","refs":[{"text":"` + strings.Repeat("x", 2048) + `"}]}`
	if code, _ := postJSON(t, hs.URL+"/link/bundle", tok, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: %d", code)
	}
}

func TestRetentionKeepsBranchesAndTheNewest(t *testing.T) {
	s, err := OpenStore(context.Background(), mustFS(t), Options{Keep: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		ref := ""
		if i == 1 {
			ref = "release" // an old commit a branch still points at
		}
		if err := s.Put(Bundle{Format: 1, Repo: "example.org/app", SHA: fmt.Sprintf("c%d", i), Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := s.blob.List(context.Background(), slug("example.org/app")+"/")
	got := strings.Join(keys, " ")
	for _, want := range []string{"c1.json", "c6.json", "c5.json"} {
		if !strings.Contains(got, want) {
			t.Fatalf("retention dropped %s: %s", want, got)
		}
	}
	for _, gone := range []string{"c2.json", "c3.json", "c4.json"} {
		if strings.Contains(got, gone) {
			t.Fatalf("retention kept %s: %s", gone, got)
		}
	}
	if _, _, problems, _ := s.Check(context.Background()); len(problems) != 0 {
		t.Fatalf("check after retention: %+v", problems)
	}
}

func TestDeleteCheckBackupRestore(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(ctx, mustFS(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"c1", "c2"} {
		if err := s.Put(Bundle{Format: 1, Repo: "example.org/app", SHA: sha, Ref: "main", Refs: []Site{{Key: "go:x#F"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put(Bundle{Format: 1, Repo: "example.org/svc", SHA: "s1", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	// Deleting the commit main points at falls back to the latest remaining.
	if err := s.DeleteCommit(ctx, "example.org/app", "c2"); err != nil {
		t.Fatal(err)
	}
	if got := s.Consumers(ConsumersRequest{Keys: []string{"go:x#F"}}); len(got.Repos) != 1 || got.Repos[0].SHA != "c1" {
		t.Fatalf("after deleting c2: %+v", got.Repos)
	}
	backup := t.TempDir()
	dst, _ := OpenFS(backup)
	if n, err := Copy(ctx, s.blob, dst); err != nil || n != 4 {
		t.Fatalf("backup: %d %v", n, err)
	}
	if _, err := s.DeleteRepo(ctx, "example.org/app"); err != nil {
		t.Fatal(err)
	}
	if got := s.Consumers(ConsumersRequest{Keys: []string{"go:x#F"}}); len(got.Repos) != 0 {
		t.Fatalf("after deleting the repository: %+v", got.Repos)
	}
	restored := mustFS(t)
	if _, err := Copy(ctx, dst, restored); err != nil {
		t.Fatal(err)
	}
	r, _ := OpenStore(ctx, restored, Options{})
	repos, bundles, problems, err := r.Check(ctx)
	if err != nil || repos != 2 || bundles != 2 || len(problems) != 0 {
		t.Fatalf("check of the restore: %d %d %+v %v", repos, bundles, problems, err)
	}
	// Check finds a ref pointing at a missing commit.
	_ = restored.Delete(ctx, bundleKey("example.org/svc", "s1"))
	if _, _, problems, _ := r.Check(ctx); len(problems) == 0 || !strings.Contains(problems[0].Issue, "missing commit") {
		t.Fatalf("check missed a dangling ref: %+v", problems)
	}
}

func mustFS(t *testing.T) *FSBlob {
	b, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TLS with a client certificate required: a client without one is
// refused at the handshake, one with a certificate from the CA gets in.
func TestMTLS(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := writeCA(t, dir)
	serverCert, serverKey := writeLeaf(t, dir, "server", caCert, caKey, true)
	clientCert, clientKey := writeLeaf(t, dir, "client", caCert, caKey, false)
	s, _ := Open(t.TempDir())
	srv := NewServer(s, ServerOptions{Auth: SharedToken("tok")})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, srv, ListenConfig{Addr: addr, TLSCert: serverCert, TLSKey: serverKey, ClientCA: filepath.Join(dir, "ca.pem")})
	}()
	defer func() { cancel(); <-done }()
	time.Sleep(200 * time.Millisecond)

	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	pool.AppendCertsFromPEM(pem)
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	if _, err := noCert.Get("https://" + addr + "/health"); err == nil {
		t.Fatal("a client without a certificate got in")
	}
	pair, err := tls.LoadX509KeyPair(clientCert, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	withCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}}}}
	r, err := withCert.Get("https://" + addr + "/health")
	if err != nil {
		t.Fatalf("client with a certificate: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("health over mTLS: %d", r.StatusCode)
	}

	// Prism's own client, configured through the environment.
	t.Setenv("PRISM_LINK_CA", filepath.Join(dir, "ca.pem"))
	t.Setenv("PRISM_LINK_CLIENT_CERT", clientCert)
	t.Setenv("PRISM_LINK_CLIENT_KEY", clientKey)
	t.Setenv("PRISM_LINK_TOKEN", "tok")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()
	if _, err := AskConsumers(context.Background(), "https://"+addr, ConsumersRequest{Keys: []string{"x"}}); err != nil {
		t.Fatalf("prism client over mTLS: %v", err)
	}
}

func writeCA(t *testing.T, dir string) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", der)
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func writeLeaf(t *testing.T, dir, name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, server bool) (string, string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, keyFile := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+"-key.pem")
	writePEM(t, cert, "CERTIFICATE", der)
	kb, _ := x509.MarshalECPrivateKey(key)
	writePEM(t, keyFile, "EC PRIVATE KEY", kb)
	return cert, keyFile
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
