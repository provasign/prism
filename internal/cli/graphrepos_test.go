package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/workspace"
)

func TestRepoGraphsRefusesReposWithoutALocalCopy(t *testing.T) {
	g := newRepoGraphs(func() []workspace.Root {
		return []workspace.Root{{ID: "gone", Path: "/nowhere", Missing: true}}
	})
	defer g.Close()
	if _, err := g.Open(context.Background(), "served", false); err == nil || !strings.Contains(err.Error(), "link server") {
		t.Fatalf("unknown repo: %v", err)
	}
	if _, err := g.Open(context.Background(), "gone", false); err == nil || !strings.Contains(err.Error(), "not checked out") {
		t.Fatalf("missing repo: %v", err)
	}
}
