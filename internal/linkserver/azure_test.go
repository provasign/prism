package linkserver

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Against Azurite or a real account when PRISM_TEST_AZURE names a
// container URL (account key in AZURE_STORAGE_KEY; PRISM_TEST_AZURE_PATH_STYLE=1
// for Azurite): the Blob contract, and two stores sharing the container.
func TestAzureBlobAgainstARealServer(t *testing.T) {
	u := os.Getenv("PRISM_TEST_AZURE")
	if u == "" {
		t.Skip("PRISM_TEST_AZURE not set")
	}
	ctx := context.Background()
	pathStyle := os.Getenv("PRISM_TEST_AZURE_PATH_STYLE") == "1"
	ensureAzureContainer(t, u, pathStyle)
	prefix := strings.TrimRight(u, "/") + "/" + t.Name() + "-" + time.Now().Format("150405.000000")
	b, err := OpenAzure(ctx, AzureConfig{URL: prefix, PathStyle: pathStyle})
	if err != nil {
		t.Fatal(err)
	}
	blobContract(t, b)
	s1, err := OpenStore(ctx, b, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(ctx, b, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	replicaContract(t, s1, s2)
}

// ensureAzureContainer creates the container (an existing one is fine).
func ensureAzureContainer(t *testing.T, raw string, pathStyle bool) {
	t.Helper()
	u, _ := url.Parse(raw)
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	account, container := strings.Split(u.Host, ".")[0], segs[0]
	base := &url.URL{Scheme: u.Scheme, Host: u.Host}
	if pathStyle {
		account, container = segs[0], segs[1]
		base.Path = "/" + account
	}
	auth, err := azureAuthFromEnv(account)
	if err != nil {
		t.Fatal(err)
	}
	b := &AzureBlob{account: account, container: container, base: base, auth: auth, client: http.DefaultClient}
	resp, data, err := b.do(context.Background(), http.MethodPut, b.blobURL("", url.Values{"restype": {"container"}}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		t.Fatalf("create container: %v", azureError(resp, data))
	}
}
