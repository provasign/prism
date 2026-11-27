package linkserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
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

// AzureConfig selects an Azure Blob Storage container (Azure, Azure Stack
// Hub, Azurite). URL is the container URL with an optional prefix:
// https://<account>.blob.core.windows.net/<container>[/<prefix>]. With
// PathStyle the account is the first path segment, as the Azurite emulator
// and some private endpoints use: http://host:10000/<account>/<container>.
//
// Credentials, from the environment, first match wins:
//   - AZURE_STORAGE_KEY: the account key (Shared Key signing)
//   - AZURE_STORAGE_SAS_TOKEN: a SAS token for the container
//   - AZURE_FEDERATED_TOKEN_FILE + AZURE_CLIENT_ID + AZURE_TENANT_ID: AKS
//     workload identity
//   - AZURE_CLIENT_SECRET + AZURE_CLIENT_ID + AZURE_TENANT_ID: a service
//     principal
//   - AZURE_USE_MANAGED_IDENTITY=1 (AZURE_CLIENT_ID optional): the VM's or
//     App Service's managed identity through the instance metadata service
type AzureConfig struct {
	URL       string
	PathStyle bool
}

// AzureBlob is a Blob on an Azure Blob Storage container. PutIf uses
// If-Match / If-None-Match: *, so several replicas can share it.
type AzureBlob struct {
	account, container, prefix string
	base                       *url.URL // scheme://host[/account]
	auth                       azureAuth
	client                     *http.Client
}

const azureVersion = "2021-08-06"

// OpenAzure checks the container is reachable with the credentials.
func OpenAzure(ctx context.Context, c AzureConfig) (*AzureBlob, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("--azure needs https://<account>.blob.core.windows.net/<container>[/prefix], got %q", c.URL)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	var account string
	base := &url.URL{Scheme: u.Scheme, Host: u.Host}
	if c.PathStyle {
		if len(segs) < 2 || segs[0] == "" {
			return nil, fmt.Errorf("--azure with --azure-path-style needs http(s)://host/<account>/<container>[/prefix], got %q", c.URL)
		}
		account, segs = segs[0], segs[1:]
		base.Path = "/" + account
	} else {
		account, _, _ = strings.Cut(u.Host, ".")
		if len(segs) < 1 || segs[0] == "" {
			return nil, fmt.Errorf("--azure needs the container in the path: %q", c.URL)
		}
	}
	if a := os.Getenv("AZURE_STORAGE_ACCOUNT"); a != "" {
		account = a
	}
	prefix := strings.Join(segs[1:], "/")
	if prefix != "" {
		prefix += "/"
	}
	auth, err := azureAuthFromEnv(account)
	if err != nil {
		return nil, err
	}
	b := &AzureBlob{account: account, container: segs[0], prefix: prefix, base: base, auth: auth,
		client: &http.Client{Timeout: 2 * time.Minute}}
	if _, err := b.List(ctx, probePrefix); err != nil {
		return nil, fmt.Errorf("Azure container %s: %w", segs[0], err)
	}
	return b, nil
}

func (b *AzureBlob) Describe() string {
	return fmt.Sprintf("azure %s/%s/%s at %s", b.account, b.container, b.prefix, b.base.Host)
}

func (b *AzureBlob) blobURL(key string, q url.Values) *url.URL {
	u := *b.base
	p := u.Path + "/" + b.container
	if key != "" {
		p += "/" + b.prefix + key
	}
	u.Path = p
	u.RawPath = uriEncodePath(p)
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return &u
}

func (b *AzureBlob) do(ctx context.Context, method string, u *url.URL, body []byte, hdr map[string]string) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("x-ms-version", azureVersion)
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	if err := b.auth.authorize(ctx, req, b.account); err != nil {
		return nil, nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	return resp, data, err
}

func azureError(resp *http.Response, data []byte) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(data, &e)
	if e.Code != "" {
		msg, _, _ := strings.Cut(e.Message, "\n")
		return fmt.Errorf("Azure %s: %s: %s", resp.Status, e.Code, msg)
	}
	if c := resp.Header.Get("x-ms-error-code"); c != "" {
		return fmt.Errorf("Azure %s: %s", resp.Status, c)
	}
	return fmt.Errorf("Azure %s", resp.Status)
}

func (b *AzureBlob) Get(ctx context.Context, key, ifNoneMatch string) ([]byte, string, error) {
	hdr := map[string]string{}
	if ifNoneMatch != "" {
		hdr["If-None-Match"] = ifNoneMatch
	}
	resp, data, err := b.do(ctx, http.MethodGet, b.blobURL(key, nil), nil, hdr)
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
	return nil, "", azureError(resp, data)
}

func (b *AzureBlob) put(ctx context.Context, key string, data []byte, cond map[string]string) (*http.Response, []byte, error) {
	hdr := map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "application/json"}
	for k, v := range cond {
		hdr[k] = v
	}
	return b.do(ctx, http.MethodPut, b.blobURL(key, nil), data, hdr)
}

func (b *AzureBlob) Put(ctx context.Context, key string, data []byte) error {
	resp, body, err := b.put(ctx, key, data, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated {
		return azureError(resp, body)
	}
	return nil
}

func (b *AzureBlob) PutIf(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	cond := map[string]string{"If-Match": ifMatch}
	if ifMatch == "" {
		cond = map[string]string{"If-None-Match": "*"}
	}
	resp, body, err := b.put(ctx, key, data, cond)
	if err != nil {
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusCreated:
		return resp.Header.Get("ETag"), nil
	case http.StatusPreconditionFailed, http.StatusConflict: // 409 BlobAlreadyExists for If-None-Match: *
		return "", ErrConflict
	}
	return "", azureError(resp, body)
}

func (b *AzureBlob) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "prefix": {b.prefix + prefix}}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, data, err := b.do(ctx, http.MethodGet, b.blobURL("", q), nil, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, azureError(resp, data)
		}
		var r struct {
			Blobs []struct {
				Name string `xml:"Name"`
			} `xml:"Blobs>Blob"`
			NextMarker string `xml:"NextMarker"`
		}
		if err := xml.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		for _, bl := range r.Blobs {
			out = append(out, strings.TrimPrefix(bl.Name, b.prefix))
		}
		if r.NextMarker == "" {
			break
		}
		marker = r.NextMarker
	}
	sort.Strings(out)
	return out, nil
}

func (b *AzureBlob) Delete(ctx context.Context, key string) error {
	resp, data, err := b.do(ctx, http.MethodDelete, b.blobURL(key, nil), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return azureError(resp, data)
	}
	return nil
}

// --- authorization

type azureAuth interface {
	authorize(ctx context.Context, req *http.Request, account string) error
}

func azureAuthFromEnv(account string) (azureAuth, error) {
	clientID, tenant := os.Getenv("AZURE_CLIENT_ID"), os.Getenv("AZURE_TENANT_ID")
	switch {
	case os.Getenv("AZURE_STORAGE_KEY") != "":
		key, err := base64.StdEncoding.DecodeString(os.Getenv("AZURE_STORAGE_KEY"))
		if err != nil {
			return nil, errors.New("AZURE_STORAGE_KEY is not base64")
		}
		return sharedKey(key), nil
	case os.Getenv("AZURE_STORAGE_SAS_TOKEN") != "":
		return sasToken(strings.TrimPrefix(os.Getenv("AZURE_STORAGE_SAS_TOKEN"), "?")), nil
	case os.Getenv("AZURE_FEDERATED_TOKEN_FILE") != "" && clientID != "" && tenant != "":
		return &entraToken{fetch: entraClientCredentials(tenant, clientID, "", os.Getenv("AZURE_FEDERATED_TOKEN_FILE"))}, nil
	case os.Getenv("AZURE_CLIENT_SECRET") != "" && clientID != "" && tenant != "":
		return &entraToken{fetch: entraClientCredentials(tenant, clientID, os.Getenv("AZURE_CLIENT_SECRET"), "")}, nil
	case os.Getenv("AZURE_USE_MANAGED_IDENTITY") == "1":
		return &entraToken{fetch: managedIdentity(clientID)}, nil
	}
	return nil, errors.New("no Azure credentials: set AZURE_STORAGE_KEY, AZURE_STORAGE_SAS_TOKEN, AZURE_FEDERATED_TOKEN_FILE (+ AZURE_CLIENT_ID, AZURE_TENANT_ID), AZURE_CLIENT_SECRET (+ AZURE_CLIENT_ID, AZURE_TENANT_ID), or AZURE_USE_MANAGED_IDENTITY=1")
}

type sasToken string

func (s sasToken) authorize(_ context.Context, req *http.Request, _ string) error {
	if req.URL.RawQuery == "" {
		req.URL.RawQuery = string(s)
	} else {
		req.URL.RawQuery += "&" + string(s)
	}
	return nil
}

// sharedKey signs with the account key (Shared Key authorization, version
// 2009-09-19 and later string-to-sign).
type sharedKey []byte

func (k sharedKey) authorize(_ context.Context, req *http.Request, account string) error {
	req.Header.Set("Authorization", "SharedKey "+account+":"+azureSignature(req, account, k))
	return nil
}

func azureSignature(req *http.Request, account string, key []byte) string {
	h := req.Header
	length := ""
	if req.ContentLength > 0 {
		length = strconv.FormatInt(req.ContentLength, 10)
	}
	var ms []string
	for name := range h {
		if l := strings.ToLower(name); strings.HasPrefix(l, "x-ms-") {
			ms = append(ms, l)
		}
	}
	sort.Strings(ms)
	var ch strings.Builder
	for _, n := range ms {
		ch.WriteString(n + ":" + strings.TrimSpace(h.Get(n)) + "\n")
	}
	res := "/" + account + req.URL.EscapedPath()
	q := req.URL.Query()
	names := make([]string, 0, len(q))
	for n := range q {
		names = append(names, strings.ToLower(n))
	}
	sort.Strings(names)
	for _, n := range names {
		vals := append([]string{}, q[n]...)
		sort.Strings(vals)
		res += "\n" + n + ":" + strings.Join(vals, ",")
	}
	toSign := strings.Join([]string{req.Method, h.Get("Content-Encoding"), h.Get("Content-Language"), length,
		h.Get("Content-MD5"), h.Get("Content-Type"), "", h.Get("If-Modified-Since"), h.Get("If-Match"),
		h.Get("If-None-Match"), h.Get("If-Unmodified-Since"), h.Get("Range")}, "\n") + "\n" + ch.String() + res
	m := hmac.New(sha256.New, key)
	m.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// entraToken is a Microsoft Entra ID bearer token for Azure Storage,
// renewed five minutes before it expires.
type entraToken struct {
	fetch func(ctx context.Context) (string, time.Time, error)
	mu    sync.Mutex
	tok   string
	exp   time.Time
}

func (e *entraToken) authorize(ctx context.Context, req *http.Request, _ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tok == "" || time.Until(e.exp) < 5*time.Minute {
		t, exp, err := e.fetch(ctx)
		if err != nil {
			return fmt.Errorf("Azure token: %w", err)
		}
		e.tok, e.exp = t, exp
	}
	req.Header.Set("Authorization", "Bearer "+e.tok)
	return nil
}

const azureStorageScope = "https://storage.azure.com/.default"

func tokenReply(resp *http.Response) (string, time.Time, error) {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var r struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
		ExpiresOn   json.Number `json:"expires_on"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("no access token in the reply")
	}
	exp := time.Now().Add(time.Hour)
	if n, err := r.ExpiresIn.Int64(); err == nil && n > 0 {
		exp = time.Now().Add(time.Duration(n) * time.Second)
	} else if n, err := r.ExpiresOn.Int64(); err == nil && n > 0 {
		exp = time.Unix(n, 0)
	}
	return r.AccessToken, exp, nil
}

// entraClientCredentials gets a token for a service principal, with a
// client secret or a federated token file (workload identity).
func entraClientCredentials(tenant, clientID, secret, tokenFile string) func(context.Context) (string, time.Time, error) {
	authority := strings.TrimRight(os.Getenv("AZURE_AUTHORITY_HOST"), "/")
	if authority == "" {
		authority = "https://login.microsoftonline.com"
	}
	return func(ctx context.Context) (string, time.Time, error) {
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "scope": {azureStorageScope}}
		if tokenFile != "" {
			assertion, err := os.ReadFile(tokenFile)
			if err != nil {
				return "", time.Time{}, err
			}
			form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
			form.Set("client_assertion", strings.TrimSpace(string(assertion)))
		} else {
			form.Set("client_secret", secret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, authority+"/"+tenant+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return "", time.Time{}, err
		}
		return tokenReply(resp)
	}
}

// managedIdentity gets a token from the instance metadata service.
func managedIdentity(clientID string) func(context.Context) (string, time.Time, error) {
	return func(ctx context.Context) (string, time.Time, error) {
		endpoint := "http://169.254.169.254/metadata/identity/oauth2/token"
		q := url.Values{"api-version": {"2018-02-01"}, "resource": {"https://storage.azure.com/"}}
		if clientID != "" {
			q.Set("client_id", clientID)
		}
		hdr := "Metadata"
		val := "true"
		if e := os.Getenv("IDENTITY_ENDPOINT"); e != "" { // App Service, Functions, Container Apps
			endpoint = e
			q = url.Values{"api-version": {"2019-08-01"}, "resource": {"https://storage.azure.com/"}}
			if clientID != "" {
				q.Set("client_id", clientID)
			}
			hdr, val = "X-IDENTITY-HEADER", os.Getenv("IDENTITY_HEADER")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set(hdr, val)
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return "", time.Time{}, err
		}
		return tokenReply(resp)
	}
}
