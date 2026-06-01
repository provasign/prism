package grove

import (
	"context"
	"testing"
)

// TestClient_AllEndpoints_ErrorPaths covers the error return of every query
// method by pointing the client at a server that always replies 500.
func TestClient_AllEndpoints_ErrorPaths(t *testing.T) {
	srv := newFake(t, 500, map[string]any{"error": "boom"})
	defer srv.Close()
	c := NewClient(srv.URL, "")
	ctx := context.Background()

	if _, err := c.QueryByIntent(ctx, "q", 5); err == nil {
		t.Error("QueryByIntent: expected error on 500")
	}
	if _, err := c.SearchSymbols(ctx, "x", 5); err == nil {
		t.Error("SearchSymbols: expected error on 500")
	}
	if _, err := c.Deps(ctx, "f.go"); err == nil {
		t.Error("Deps: expected error on 500")
	}
	if _, err := c.Impact(ctx, "x", 3); err == nil {
		t.Error("Impact: expected error on 500")
	}
	if _, err := c.Semantic(ctx, "q", 5); err == nil {
		t.Error("Semantic: expected error on 500")
	}
	if _, err := c.Tests(ctx, "q"); err == nil {
		t.Error("Tests: expected error on 500")
	}
	if _, err := c.Status(ctx); err == nil {
		t.Error("Status: expected error on 500")
	}
	if _, err := c.Index(ctx, "."); err == nil {
		t.Error("Index: expected error on 500")
	}
}
