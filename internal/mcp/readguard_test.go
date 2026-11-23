package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// SEC-03: a match in a sensitive file is reported with its text withheld;
// read refuses the file unless include_sensitive is set.
func TestSensitiveFilesWithheld(t *testing.T) {
	h := newTextSearchHandler(t)
	mustWrite(t, h.Root, ".env", "API_TOKEN=supersecret123\n")
	out, err := h.Invoke("prism_search", map[string]any{"query": "API_TOKEN", "scope": "text"})
	if err != nil {
		t.Fatal(err)
	}
	txt, _ := renderSearchAsText(out.(map[string]any))
	if strings.Contains(txt, "supersecret") || !strings.Contains(txt, ".env") || !strings.Contains(txt, "withheld") {
		t.Fatalf("search:\n%s", txt)
	}
	out, err = h.Invoke("prism_search", map[string]any{"query": "API_TOKEN", "scope": "text", "include_sensitive": true})
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := renderSearchAsText(out.(map[string]any)); !strings.Contains(txt, "supersecret") {
		t.Fatalf("include_sensitive search:\n%s", txt)
	}
	r, err := h.Invoke("prism_read", map[string]any{"file": ".env"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r) == "" || strings.Contains(fmt.Sprint(r), "supersecret") {
		t.Fatalf("read .env: %v", r)
	}
	r, err = h.Invoke("prism_read", map[string]any{"file": ".env", "offset": 1, "limit": 5, "include_sensitive": true})
	if err != nil || !strings.Contains(fmt.Sprint(r), "supersecret") {
		t.Fatalf("read .env include_sensitive: %v %v", r, err)
	}
}

// SEC-04: read returns promptly for a FIFO, a binary file and a large file.
func TestReadGuards(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		g, err := readGuarded(fifo, "pipe", false, 0, 0)
		if err != nil || g.refused != "not a regular file" {
			t.Fatalf("fifo: %+v %v", g, err)
		}
	}
	bin := filepath.Join(root, "blob")
	if err := os.WriteFile(bin, []byte("ab\x00cd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, _ := readGuarded(bin, "blob", false, 0, 0); !strings.HasPrefix(g.refused, "binary file") {
		t.Fatalf("binary: %+v", g)
	}
	big := filepath.Join(root, "big.txt")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 300000; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()
	g, err := readGuarded(big, "big.txt", false, 250000, 250002)
	if err != nil || g.totalLines != 300000 {
		t.Fatalf("large range: total=%d err=%v", g.totalLines, err)
	}
	lines := strings.Split(string(g.data), "\n")
	if lines[249999] != "line 250000" || lines[250001] != "line 250002" {
		t.Fatalf("large range content: %q %q", lines[249999], lines[250001])
	}
	if whole, _ := readGuarded(big, "big.txt", false, 0, 0); len(whole.data) > readWholeLimit || whole.note == "" {
		t.Fatalf("large whole read: %d bytes, note %q", len(whole.data), whole.note)
	}
}
