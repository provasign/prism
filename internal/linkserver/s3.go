package linkserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// S3Config selects an S3-compatible bucket (AWS S3, MinIO, Ceph, GCS
// interoperability). Credentials come from the environment:
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN, or web
// identity (AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE, as EKS IRSA sets).
type S3Config struct {
	URL       string // s3://bucket[/prefix]
	Endpoint  string // default https://s3.<region>.amazonaws.com
	Region    string // default $AWS_REGION, else us-east-1
	PathStyle bool   // bucket in the path (MinIO), not the host name
}

// S3Blob is a Blob on an S3-compatible bucket. PutIf uses conditional
// writes (If-Match, If-None-Match: *), so several server replicas can
// share one bucket.
type S3Blob struct {
	bucket, prefix, region string
	base                   *url.URL
	pathStyle              bool
	creds                  credentialSource
	client                 *http.Client
}

// OpenS3 checks the bucket is reachable with the credentials.
func OpenS3(ctx context.Context, c S3Config) (*S3Blob, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return nil, fmt.Errorf("--s3 needs s3://bucket[/prefix], got %q", c.URL)
	}
	region := c.Region
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}
	ep := c.Endpoint
	if ep == "" {
		ep = "https://s3." + region + ".amazonaws.com"
	}
	base, err := url.Parse(strings.TrimRight(ep, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("bad --s3-endpoint %q", ep)
	}
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	creds, err := envCredentials(region)
	if err != nil {
		return nil, err
	}
	b := &S3Blob{bucket: u.Host, prefix: prefix, region: region, base: base, pathStyle: c.PathStyle, creds: creds,
		client: &http.Client{Timeout: 2 * time.Minute}}
	if _, err := b.List(ctx, "\x00probe"); err != nil {
		return nil, fmt.Errorf("S3 bucket %s: %w", u.Host, err)
	}
	return b, nil
}

func (b *S3Blob) Describe() string {
	return fmt.Sprintf("s3://%s/%s at %s", b.bucket, b.prefix, b.base.Host)
}

func (b *S3Blob) objectURL(key string, q url.Values) *url.URL {
	u := *b.base
	p := "/" + b.prefix + key
	if key == "" {
		p = "/"
	}
	if b.pathStyle {
		p = "/" + b.bucket + p
	} else {
		u.Host = b.bucket + "." + u.Host
	}
	u.Path = p
	u.RawPath = uriEncodePath(p)
	if q != nil {
		u.RawQuery = canonicalQuery(q)
	}
	return &u
}

func (b *S3Blob) do(ctx context.Context, method string, u *url.URL, body []byte, hdr map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	cred, err := b.creds.get(ctx)
	if err != nil {
		return nil, nil, err
	}
	signV4(req, body, cred, b.region, time.Now().UTC())
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	return resp, data, err
}

func s3Error(resp *http.Response, data []byte) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(data, &e)
	if e.Code != "" {
		return fmt.Errorf("S3 %s: %s: %s", resp.Status, e.Code, e.Message)
	}
	return fmt.Errorf("S3 %s", resp.Status)
}

func (b *S3Blob) Get(ctx context.Context, key, ifNoneMatch string) ([]byte, string, error) {
	hdr := map[string]string{}
	if ifNoneMatch != "" {
		hdr["If-None-Match"] = ifNoneMatch
	}
	resp, data, err := b.do(ctx, http.MethodGet, b.objectURL(key, nil), nil, hdr)
	if err != nil {
		return nil, "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return data, resp.Header.Get("ETag"), nil
	case http.StatusNotModified:
		return nil, ifNoneMatch, ErrNotModified
	case http.StatusNotFound:
		return nil, "", ErrNotFound
	}
	return nil, "", s3Error(resp, data)
}

func (b *S3Blob) Put(ctx context.Context, key string, data []byte) error {
	resp, body, err := b.do(ctx, http.MethodPut, b.objectURL(key, nil), data, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return s3Error(resp, body)
	}
	return nil
}

func (b *S3Blob) PutIf(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	hdr := map[string]string{"Content-Type": "application/json"}
	if ifMatch == "" {
		hdr["If-None-Match"] = "*"
	} else {
		hdr["If-Match"] = ifMatch
	}
	resp, body, err := b.do(ctx, http.MethodPut, b.objectURL(key, nil), data, hdr)
	if err != nil {
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Header.Get("ETag"), nil
	case http.StatusPreconditionFailed, http.StatusConflict:
		return "", ErrConflict
	}
	return "", s3Error(resp, body)
}

func (b *S3Blob) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {b.prefix + prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := b.objectURL("", q)
		resp, data, err := b.do(ctx, http.MethodGet, u, nil, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, s3Error(resp, data)
		}
		var r struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		for _, c := range r.Contents {
			out = append(out, strings.TrimPrefix(c.Key, b.prefix))
		}
		if !r.IsTruncated || r.NextContinuationToken == "" {
			break
		}
		token = r.NextContinuationToken
	}
	sort.Strings(out)
	return out, nil
}

func (b *S3Blob) Delete(ctx context.Context, key string) error {
	resp, data, err := b.do(ctx, http.MethodDelete, b.objectURL(key, nil), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return s3Error(resp, data)
	}
	return nil
}

// --- credentials

type credential struct {
	id, secret, session string
	expires             time.Time
}

type credentialSource interface {
	get(ctx context.Context) (credential, error)
}

type staticCreds credential

func (s staticCreds) get(context.Context) (credential, error) { return credential(s), nil }

func envCredentials(region string) (credentialSource, error) {
	if id := os.Getenv("AWS_ACCESS_KEY_ID"); id != "" {
		return staticCreds{id: id, secret: os.Getenv("AWS_SECRET_ACCESS_KEY"), session: os.Getenv("AWS_SESSION_TOKEN")}, nil
	}
	if role, tokenFile := os.Getenv("AWS_ROLE_ARN"), os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"); role != "" && tokenFile != "" {
		return &webIdentity{role: role, tokenFile: tokenFile, region: region, client: &http.Client{Timeout: 30 * time.Second}}, nil
	}
	return nil, errors.New("no S3 credentials: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or AWS_ROLE_ARN and AWS_WEB_IDENTITY_TOKEN_FILE")
}

// webIdentity exchanges a projected service-account token for temporary
// credentials with STS AssumeRoleWithWebIdentity, renewing them before
// they expire.
type webIdentity struct {
	role, tokenFile, region string
	client                  *http.Client
	mu                      sync.Mutex
	cur                     credential
}

func (w *webIdentity) get(ctx context.Context) (credential, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cur.id != "" && time.Until(w.cur.expires) > 5*time.Minute {
		return w.cur, nil
	}
	tok, err := os.ReadFile(w.tokenFile)
	if err != nil {
		return credential{}, err
	}
	q := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {w.role},
		"RoleSessionName": {"prism-link-server"}, "WebIdentityToken": {strings.TrimSpace(string(tok))}}
	ep := "https://sts." + w.region + ".amazonaws.com/"
	if e := os.Getenv("AWS_STS_ENDPOINT"); e != "" {
		ep = e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, strings.NewReader(q.Encode()))
	if err != nil {
		return credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := w.client.Do(req)
	if err != nil {
		return credential{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return credential{}, fmt.Errorf("STS AssumeRoleWithWebIdentity: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var r struct {
		Creds struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
	}
	if err := xml.Unmarshal(data, &r); err != nil {
		return credential{}, err
	}
	exp, _ := time.Parse(time.RFC3339, r.Creds.Expiration)
	w.cur = credential{id: r.Creds.AccessKeyID, secret: r.Creds.SecretAccessKey, session: r.Creds.SessionToken, expires: exp}
	return w.cur, nil
}

// --- Signature Version 4

func hashHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacSHA(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncode is AWS's URI encoding: every byte but unreserved characters.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func uriEncodePath(p string) string { return uriEncode(p, true) }

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string{}, q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// signV4 signs req for service s3 (AWS Signature Version 4, signed headers
// in the Authorization header, payload hash in x-amz-content-sha256).
func signV4(req *http.Request, body []byte, c credential, region string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payload := hashHex(body)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	if c.session != "" {
		req.Header.Set("X-Amz-Security-Token", c.session)
	}
	host := req.URL.Host
	names := []string{"host"}
	vals := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" || lk == "accept-encoding" {
			continue
		}
		names = append(names, lk)
		vals[lk] = strings.TrimSpace(strings.Join(v, ","))
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, ch.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hashHex([]byte(canonical))
	k := hmacSHA([]byte("AWS4"+c.secret), day)
	k = hmacSHA(k, region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.id+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}
