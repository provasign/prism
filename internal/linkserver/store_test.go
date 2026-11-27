package linkserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// A requirement satisfied by a local directory (a Go replace, go.work, npm
// file:) carries the exporting machine's absolute path in Grove's manifest
// data. The uploaded bundle keeps only that it was local.
func TestUploadRequiresDropMachineLocalParts(t *testing.T) {
	got := uploadRequires([]groveeng.ManifestRequire{
		{Manifest: "go.mod", Ecosystem: "go", Name: "example.com/lib", Version: "v0.1.0",
			LocalPath: "/home/runner/work/lib", Evidence: "go.mod:7 replace example.com/lib => ../lib (required v0.1.0)"},
		{Manifest: "go.mod", Ecosystem: "go", Name: "example.com/dep", Version: "v1.2.0"},
		{Manifest: "go.work", Ecosystem: "go", Name: "example.com/sibling", LocalPath: "/home/runner/work/sibling",
			Evidence: "go.work:3 use /home/runner/work/sibling", CoMember: true},
	})
	b, _ := json.Marshal(got)
	for _, leak := range []string{"/home/runner", "replace", "evidence", "localPath", "sibling"} {
		if bytes.Contains(b, []byte(leak)) {
			t.Fatalf("uploaded requires contain %q: %s", leak, b)
		}
	}
	if len(got) != 2 || !got[0].Local || got[1].Local {
		t.Fatalf("requires = %+v; want lib (local) and dep (not local), no go.work co-member", got)
	}
}

// A bundle from an older client still carries localPath and evidence; the
// server does not store them.
func TestServerDropsLocalPathsFromOlderClients(t *testing.T) {
	s, srv := newServer(t, "tok")
	old := `{"repo":"example.org/app","sha":"abc123","ref":"main","created":"2026-11-27T00:00:00Z",
	  "requires":[{"manifest":"go.mod","ecosystem":"go","name":"example.com/lib","version":"v0.1.0",
	    "localPath":"/Users/someone/src/lib","evidence":"go.mod:7 replace example.com/lib => /Users/someone/src/lib"}],
	  "refs":[{"key":"go:example.com/lib#F","file":"main.go","from":"main","line":3,"text":"lib.F()"}]}`
	if code, body := postJSON(t, srv.URL+"/link/bundle", "tok", old); code != http.StatusOK {
		t.Fatalf("upload: %d %s", code, body)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, slug("example.org/app"), "abc123.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("/Users/someone")) || bytes.Contains(data, []byte("evidence")) {
		t.Fatalf("stored bundle keeps a machine-local path: %s", data)
	}
}

// Most CI checkouts are a detached HEAD. Re-uploading a commit that was
// stored from main must not erase the branch it came from.
func TestDetachedReuploadKeepsTheCommitsBranch(t *testing.T) {
	s, _ := newServer(t, "")
	b := Bundle{Repo: "example.org/app", SHA: "abc123", Ref: "main", Created: "2026-11-27T00:00:00Z",
		Refs: []Site{{Key: "go:example.com/lib#F", File: "main.go", Line: 3}}}
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	b.Ref = ""
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	resp := s.Consumers(ConsumersRequest{Keys: []string{"go:example.com/lib#F"}})
	if len(resp.Repos) != 1 || resp.Repos[0].Ref != "main" {
		t.Fatalf("after a detached re-upload: %+v; want the commit still answered as main", resp.Repos)
	}
	refs := s.readRefs(filepath.Join(s.dir, slug(b.Repo)))
	if refs.Refs["main"] != "abc123" || refs.Latest != "abc123" {
		t.Fatalf("refs.json = %+v", refs)
	}
}

// An answer with nothing in it is an empty list, not null, for every
// endpoint a client reads repos from.
func TestEmptyAnswersAreEmptyLists(t *testing.T) {
	_, srv := newServer(t, "tok")
	for path, body := range map[string]string{
		"/link/consumers": `{"keys":["go:example.com/lib#F"]}`,
		"/link/define":    `{"key":"go:example.com/lib#F"}`,
	} {
		code, out := postJSON(t, srv.URL+path, "tok", body)
		if code != http.StatusOK || !strings.Contains(out, `"repos":[]`) {
			t.Fatalf("%s: %d %s; want repos as []", path, code, out)
		}
	}
	if code, out := postJSON(t, srv.URL+"/link/define", "tok", `{"name":"F"}`); code != http.StatusOK || !strings.Contains(out, `"repos":[]`) {
		t.Fatalf("define by name: %d %s", code, out)
	}
}

// The pin a server answer shows says when the consumer was built against a
// local checkout, so its version is not what it was built with.
func TestPinNamesALocalBuild(t *testing.T) {
	b := Bundle{Requires: []Require{{Ecosystem: "go", Name: "example.com/lib", Version: "v0.1.0", Local: true}}}
	if got := pinFor(b, []string{"go:example.com/lib#F"}); got != "example.com/lib v0.1.0, built against a local checkout when uploaded" {
		t.Fatalf("pin = %q", got)
	}
	b.Requires[0].Local = false
	if got := pinFor(b, []string{"go:example.com/lib/sub#F"}); got != "example.com/lib v0.1.0" {
		t.Fatalf("pin = %q", got)
	}
}

func TestTokenIsRequiredExceptForHealth(t *testing.T) {
	_, srv := newServer(t, "tok")
	if code, _ := postJSON(t, srv.URL+"/link/consumers", "", `{"keys":[]}`); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := postJSON(t, srv.URL+"/link/consumers", "wrong", `{"keys":[]}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	r, err := http.Get(srv.URL + "/health")
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("health: %v %v", r, err)
	}
	r.Body.Close()
}

func newServer(t *testing.T, token string) (*Store, *httptest.Server) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler(token))
	t.Cleanup(srv.Close)
	return s, srv
}

func postJSON(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(out)
}
