package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

func indexedFixture(t *testing.T, files map[string]string) *Handler {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		planWrite(t, dir, name, body)
	}
	gc := grove.NewClient().WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return h
}

// BUG-01: a package qualifier that names none of the declarations is not
// found; it used to answer for the real symbol, closed.
func TestChangeImpactRejectsWrongQualifier(t *testing.T) {
	h := indexedFixture(t, map[string]string{
		"go.mod":     "module example.com/q\n\ngo 1.26\n",
		"store/c.go": "package store\n\ntype Client struct{}\n\nfunc (Client) Status() int { return 1 }\n\nfunc Use() int { return Client{}.Status() }\n",
	})
	for _, q := range []string{"fictitious.Client.Status", "github.com/fake/x.Client.Status"} {
		if _, err := h.Invoke("prism_change_impact", map[string]any{"query": q}); err == nil || !strings.Contains(err.Error(), "no symbol named") {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{"store.Client.Status", "Client.Status", "example.com/q/store.Client.Status"} {
		if _, err := h.Invoke("prism_change_impact", map[string]any{"query": q}); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// BUG-02: an exact path:line selects that declaration; it used to be
// reported as ambiguous with its same-named twin in another package.
func TestChangeImpactPathLineSelectsOneDeclaration(t *testing.T) {
	h := indexedFixture(t, map[string]string{
		"go.mod":     "module example.com/p\n\ngo 1.26\n",
		"alpha/s.go": "package alpha\n\ntype Server struct{}\n\nfunc (Server) Handler() {}\n\nfunc A() { Server{}.Handler() }\n",
		"beta/s.go":  "package beta\n\ntype Server struct{}\n\nfunc (Server) Handler() {}\n\nfunc B() { Server{}.Handler() }\n",
	})
	out, err := h.Invoke("prism_change_impact", map[string]any{"query": "alpha/s.go:5"})
	if err != nil {
		t.Fatal(err)
	}
	sites := fmt.Sprint(out.(map[string]any)["editSites"])
	if !strings.Contains(sites, "alpha/s.go") || strings.Contains(sites, "beta/s.go") {
		t.Fatalf("sites: %s", sites)
	}
}

// BUG-03: after a member is added, a Go type that implemented the old
// interface but is not used as it is listed apart, and the partial
// type-check of the package is flagged.
func TestInterfaceGrowthFormerImplementersAndPartialCheck(t *testing.T) {
	h := indexedFixture(t, map[string]string{
		"go.mod": "module example.com/b\n\ngo 1.26\n",
		"s.go": "package b\n\ntype Provider interface {\n\tChat()\n\tClose()\n}\n\ntype Used struct{}\n\nfunc (Used) Chat() {}\n\nfunc (Used) Close() {}\n\n" +
			"type Broken struct{}\n\nfunc (Broken) Chat() {}\n\ntype Implicit struct{}\n\nfunc (Implicit) Chat() {}\n\nfunc Run(p Provider) { p.Chat() }\n\nfunc Main() { Run(Used{}); Run(Broken{}) }\n",
	})
	out, err := h.Invoke("prism_missing_implementations", map[string]any{"query": "Provider.Close"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	missing, former := fmt.Sprint(m["missing"]), fmt.Sprint(m["formerlyImplementing"])
	if !strings.Contains(missing, "Broken") || strings.Contains(missing, "Implicit") || !strings.Contains(former, "Implicit") {
		t.Fatalf("missing=%s former=%s", missing, former)
	}
	ci, err := h.Invoke("prism_change_impact", map[string]any{"query": "Provider.Chat"})
	if err != nil {
		t.Fatal(err)
	}
	c := ci.(map[string]any)
	if c["partialTypeCheck"] == nil || c["safeToClaimComplete"] == true {
		t.Fatalf("partial type-check not flagged: partialTypeCheck=%v safe=%v", c["partialTypeCheck"], c["safeToClaimComplete"])
	}
}
