package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/textsearch"
)

// writeInventoryFixture reproduces the jackson pr5977 shape: many dense
// files that fill the 25-line sample, and one sparse file late in path order
// that only an inventory can name.
func writeInventoryFixture(t *testing.T, root string) string {
	t.Helper()
	pad := strings.Repeat(" padding", 30)
	for i := 0; i < 30; i++ {
		dir := filepath.Join(root, "src", "dense")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var body strings.Builder
		body.WriteString("class Dense {\n")
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&body, "  boolean invStaticTyping%d = true; //%s\n", j, pad)
		}
		body.WriteString("}\n")
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("Dense%02d.java", i)), []byte(body.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join("src", "zz", "jdk", "HiddenArraySerializer.java")
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(target)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target),
		[]byte("class HiddenArraySerializer {\n  protected final boolean _invStaticTyping;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "HiddenArraySerializer.java"
}

func sampledTextFromSearch(t *testing.T, h *Handler, args map[string]any) (map[string]any, string) {
	t.Helper()
	out, err := h.Invoke("prism_search", args)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	text, ok := RenderSearchText(m)
	if !ok {
		t.Fatalf("search did not render as text: %#v", m)
	}
	return m, text
}

func TestSearchSampledTextNamesEveryMatchingFile(t *testing.T) {
	h := newTestHandler(t)
	target := writeInventoryFixture(t, h.Root)
	m, text := sampledTextFromSearch(t, h, map[string]any{"query": "invStaticTyping", "scope": "text"})
	if m["truncated"] != true {
		t.Fatalf("fixture must produce a sampled result: %#v", m["warning"])
	}
	sample := text
	if i := strings.Index(text, "matching files"); i >= 0 {
		sample = text[:i]
	}
	if strings.Contains(sample, target) {
		t.Fatalf("fixture invalid: target already visible in the sampled lines")
	}
	if !strings.Contains(text, "// all 31 matching files, hits per file; the lines above are a SAMPLE:") {
		t.Fatalf("missing file inventory header:\n%s", text)
	}
	if !strings.Contains(text, "  "+target+" (1)") || !strings.Contains(text, "src/zz/jdk/\n") {
		t.Fatalf("sparse matching file not named by the inventory:\n%s", text)
	}
	if !strings.Contains(text, "  Dense29.java (5)") {
		t.Fatalf("inventory lacks per-file hit counts:\n%s", text)
	}
	if w, _ := m["warning"].(string); !strings.Contains(w, fileInventoryWarning) {
		t.Fatalf("sample warning does not point at the file list: %q", w)
	}
}

func TestSearchSampledTextInventoryInBatchedCompactResponse(t *testing.T) {
	h := newTestHandler(t)
	target := writeInventoryFixture(t, h.Root)
	out, err := h.Invoke("prism_search", map[string]any{
		"query": []any{"HiddenNothing", "invStaticTyping"}, "scope": "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := h.RenderCompactSearchText(t.Context(), out.(map[string]any), map[string]any{
		"query": []string{"HiddenNothing", "invStaticTyping"}, "scope": "text",
	})
	if !ok {
		t.Fatal("batched result did not render")
	}
	if !strings.Contains(text, "  "+target+" (1)") {
		t.Fatalf("batched compact response hides a matching file:\n%s", text)
	}
}

func TestSearchSampledBothScopeAndExplicitLimitNameEveryFile(t *testing.T) {
	dir := t.TempDir()
	target := writeInventoryFixture(t, dir)
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	defer gc.Shutdown()
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	// scope=both: the text half is sampled the same way.
	_, text := sampledTextFromSearch(t, h, map[string]any{"query": "invStaticTyping"})
	if !strings.Contains(text, "  "+target+" (1)") {
		t.Fatalf("scope=both text half hides a matching file:\n%s", text)
	}
	// An explicit limit disables the adaptive count pass; the inventory
	// comes from its own bounded count instead.
	m, text := sampledTextFromSearch(t, h, map[string]any{"query": "invStaticTyping", "scope": "text", "limit": 10})
	if m["countComplete"] == true {
		t.Fatalf("explicit limit unexpectedly ran the adaptive count")
	}
	if !strings.Contains(text, "// all 31 matching files") || !strings.Contains(text, "  "+target+" (1)") {
		t.Fatalf("non-adaptive sample hides a matching file:\n%s", text)
	}
}

func TestSearchCompleteTextCarriesNoFileInventory(t *testing.T) {
	h := newTestHandler(t)
	if err := os.WriteFile(filepath.Join(h.Root, "one.go"), []byte("package x\n// SmallInvNeedle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, text := sampledTextFromSearch(t, h, map[string]any{"query": "SmallInvNeedle", "scope": "text"})
	if _, has := m["fileInventory"]; has || strings.Contains(text, "matching files") {
		t.Fatalf("unsampled result gained an inventory:\n%s", text)
	}
}

func TestBuildFileInventoryRollsUpPastCap(t *testing.T) {
	var counts []textsearch.FileCount
	for i := 0; i < 120; i++ {
		counts = append(counts, textsearch.FileCount{File: fmt.Sprintf("d%d/f%03d.go", i%7, i), Count: 1 + i%3})
	}
	inv := buildFileInventory(counts, true)
	var b strings.Builder
	if !renderFileInventory(&b, inv) {
		t.Fatal("inventory did not render")
	}
	text := b.String()
	if !strings.Contains(text, "// all 120 matching files: the 80 with most hits, then the other 40 by directory") {
		t.Fatalf("missing capped header:\n%s", text)
	}
	if got := strings.Count(text, ".go ("); got != 80 {
		t.Fatalf("listed %d files, want 80", got)
	}
	rolled := 0
	for _, raw := range anySlice(inv["dirs"]) {
		rolled += raw.(map[string]any)["files"].(int)
	}
	if rolled != 40 {
		t.Fatalf("directory rollup covers %d files, want 40:\n%s", rolled, text)
	}
}

func TestBoundSearchPresentationNamesFilesItHides(t *testing.T) {
	long := strings.Repeat("x", 240)
	var groups []map[string]any
	for i := 0; i < 75; i++ {
		var hits []any
		for j := 0; j < 4; j++ {
			hits = append(hits, map[string]any{"line": j + 1, "text": long})
		}
		groups = append(groups, map[string]any{"file": fmt.Sprintf("pkg/f%03d.go", i), "hits": hits})
	}
	out := map[string]any{
		"textHits": groups, "truncated": false, "resultsComplete": true,
		"countComplete": true, "totalHits": 300, "filesMatched": 75,
	}
	boundSearchPresentation(out)
	text, ok := renderSearchAsText(out)
	if !ok {
		t.Fatal("bounded result did not render")
	}
	if strings.Contains(text, "pkg/f074.go:1:") {
		t.Fatalf("fixture invalid: the budget displayed every file")
	}
	if !strings.Contains(text, "  f074.go (4)") || !strings.Contains(text, "// all 75 matching files, hits per file") {
		t.Fatalf("budget-trimmed files are not named:\n%.3000s", text)
	}
}
