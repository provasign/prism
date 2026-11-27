package linkserver

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AKS workload identity: the federated token file is sent as the client
// assertion, and the storage request carries the bearer token.
func TestAzureWorkloadIdentityToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "azure-identity-token")
	if err := os.WriteFile(tokenFile, []byte("federated.jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/tenant-1/oauth2/v2.0/token" || r.Form.Get("client_assertion") != "federated.jwt" ||
			r.Form.Get("client_id") != "client-1" || r.Form.Get("scope") != azureStorageScope ||
			r.Form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
			http.Error(w, "bad request "+r.URL.Path+" "+r.Form.Encode(), 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"entra-token","expires_in":3600}`)
	}))
	defer idp.Close()
	for _, k := range []string{"AZURE_STORAGE_KEY", "AZURE_STORAGE_SAS_TOKEN", "AZURE_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", tokenFile)
	t.Setenv("AZURE_CLIENT_ID", "client-1")
	t.Setenv("AZURE_TENANT_ID", "tenant-1")
	t.Setenv("AZURE_AUTHORITY_HOST", idp.URL)
	auth, err := azureAuthFromEnv("acct")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://acct.blob.core.windows.net/c?comp=list", nil)
	if err := auth.authorize(context.Background(), req, "acct"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer entra-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// App Service / Container Apps managed identity: IDENTITY_ENDPOINT with the
// IDENTITY_HEADER secret.
func TestAzureManagedIdentityToken(t *testing.T) {
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-IDENTITY-HEADER") != "id-secret" || r.URL.Query().Get("resource") != "https://storage.azure.com/" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"mi-token","expires_on":"4102444800"}`)
	}))
	defer ep.Close()
	for _, k := range []string{"AZURE_STORAGE_KEY", "AZURE_STORAGE_SAS_TOKEN", "AZURE_CLIENT_SECRET", "AZURE_FEDERATED_TOKEN_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("AZURE_USE_MANAGED_IDENTITY", "1")
	t.Setenv("IDENTITY_ENDPOINT", ep.URL)
	t.Setenv("IDENTITY_HEADER", "id-secret")
	auth, err := azureAuthFromEnv("acct")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://acct.blob.core.windows.net/c", nil)
	if err := auth.authorize(context.Background(), req, "acct"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer mi-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestAzureSASTokenIsAppended(t *testing.T) {
	for _, k := range []string{"AZURE_STORAGE_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("AZURE_STORAGE_SAS_TOKEN", "?sv=2021-08-06&sig=abc")
	auth, err := azureAuthFromEnv("acct")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://acct.blob.core.windows.net/c?comp=list", nil)
	_ = auth.authorize(context.Background(), req, "acct")
	if req.URL.RawQuery != "comp=list&sv=2021-08-06&sig=abc" || req.Header.Get("Authorization") != "" {
		t.Fatalf("query %q, auth %q", req.URL.RawQuery, req.Header.Get("Authorization"))
	}
}

// A GCS service-account key: the JWT is RS256-signed with the key and
// verifies against its public half.
func TestGCSServiceAccountToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	var srvErr error
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			srvErr = fmt.Errorf("form %v", r.Form)
			http.Error(w, "bad", 400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			srvErr = err
			http.Error(w, "bad signature", 400)
			return
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		_ = json.Unmarshal(claims, &c)
		if c["iss"] != "svc@proj.iam.gserviceaccount.com" || c["scope"] != gcsScope {
			srvErr = fmt.Errorf("claims %v", c)
			http.Error(w, "bad claims", 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"gcs-token","expires_in":3600}`)
	}))
	defer oauth.Close()
	sa, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "svc@proj.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": oauth.URL})
	file := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(file, sa, 0o600); err != nil {
		t.Fatal(err)
	}
	get, err := serviceAccountTokens(file)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := get(context.Background())
	if err != nil || tok != "gcs-token" {
		t.Fatalf("token %q, err %v, server %v", tok, err, srvErr)
	}
}

// GKE workload identity / GCE: the metadata server with Metadata-Flavor.
func TestGCSMetadataToken(t *testing.T) {
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" || r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/token" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"md-token","expires_in":3599}`)
	}))
	defer md.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(md.URL, "http://"))
	if tok, err := metadataTokens()(context.Background()); err != nil || tok != "md-token" {
		t.Fatalf("token %q, err %v", tok, err)
	}
}

// EKS IRSA: AssumeRoleWithWebIdentity with the projected token.
func TestS3WebIdentityCredentials(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tokenFile, []byte("eks.jwt"), 0o600)
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "eks.jwt" ||
			r.Form.Get("RoleArn") != "arn:aws:iam::1:role/links" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprint(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>
<AccessKeyId>ASIAX</AccessKeyId><SecretAccessKey>sek</SecretAccessKey><SessionToken>sess</SessionToken>
<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
	}))
	defer sts.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::1:role/links")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenFile)
	t.Setenv("AWS_STS_ENDPOINT", sts.URL)
	src, err := envCredentials("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := src.get(context.Background())
	if err != nil || c.id != "ASIAX" || c.secret != "sek" || c.session != "sess" {
		t.Fatalf("credentials %+v, err %v", c, err)
	}
}
