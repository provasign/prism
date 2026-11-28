package graphui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenRepoEndpoint(t *testing.T) {
	do := func(s *Server, header bool, query string) (int, map[string]string) {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1/api/open-repo?"+query, nil)
		if header {
			r.Header.Set("X-Prism-Graph", "1")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if c, _ := do(&Server{}, true, "id=grove"); c != http.StatusNotImplemented {
		t.Fatalf("no launcher: %d", c)
	}
	var gotID string
	var gotTests bool
	s := &Server{}
	s.SetOpenRepo(func(_ context.Context, id string, tests bool) (string, error) {
		gotID, gotTests = id, tests
		if id == "remote" {
			return "", errors.New("no local copy")
		}
		return "http://127.0.0.1:9/#k=x", nil
	})
	if c, _ := do(s, false, "id=grove"); c != http.StatusForbidden {
		t.Fatalf("without X-Prism-Graph: %d", c)
	}
	c, body := do(s, true, "id=grove&tests=1")
	if c != http.StatusOK || body["url"] != "http://127.0.0.1:9/#k=x" || gotID != "grove" || !gotTests {
		t.Fatalf("open: %d %v id=%q tests=%v", c, body, gotID, gotTests)
	}
	if c, body := do(s, true, "id=remote"); c != http.StatusUnprocessableEntity || body["error"] != "no local copy" {
		t.Fatalf("error: %d %v", c, body)
	}
}
