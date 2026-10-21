package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	<-done
	return buf.String()
}

// Malformed flag values are usage errors (exit 2) reported before any index
// is opened, never a silently different request that exits 0 (stack audit
// 2026-10-20, finding 8). Each case runs in an empty directory: the assertion
// that no .grove appears proves the parser stopped before the engine.
func TestMalformedFlagValuesAreUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"read reversed range", []string{"read", "README.md", "--from", "10", "--to", "5"}, "--to 5 is before --from 10"},
		{"read non-numeric from", []string{"read", "README.md", "--from", "banana"}, `--from requires a positive integer, got "banana"`},
		{"read zero from", []string{"read", "README.md", "--from", "0"}, "--from requires a positive integer"},
		{"read negative limit", []string{"read", "README.md", "--limit", "-3"}, "--limit requires a positive integer"},
		{"read missing to", []string{"read", "README.md", "--to"}, "--to requires a positive integer"},
		{"read range and window", []string{"read", "README.md", "--from", "2", "--limit", "3"}, "not both"},
		{"read flag before file", []string{"read", "--from", "2", "README.md"}, "the file comes first"},
		{"read bad format", []string{"read", "README.md", "--format", "yaml"}, "--format wants text|lean|json"},
		{"read missing dir", []string{"read", "README.md", "--dir"}, "--dir requires a value"},
		{"search missing path", []string{"search", "Needle", "--path"}, "--path requires a value"},
		{"search path eats next flag", []string{"search", "Needle", "--path", "--limit", "3"}, "--path requires a value"},
		{"search non-numeric limit", []string{"search", "Needle", "--limit", "banana"}, `--limit requires a positive integer, got "banana"`},
		{"search missing glob", []string{"search", "Needle", "--glob"}, "--glob requires a value"},
		{"search missing scope", []string{"search", "Needle", "--scope"}, "--scope requires text|symbols|both"},
		{"search missing dir", []string{"search", "Needle", "--dir"}, "--dir requires a value"},
		{"search bad context", []string{"search", "Needle", "--context", "x"}, "--context requires a non-negative integer"},
		{"query bad limit", []string{"query", "--terms", "a", "--limit", "0"}, "--limit requires a positive integer"},
		{"query empty terms", []string{"query", "--terms", ","}, "--terms requires a comma-separated list"},
		{"query bad delivery", []string{"query", "--terms", "a", "--delivery", "all"}, "--delivery wants source|symbols"},
		{"query bad max-files", []string{"query", "--terms", "a", "--max-files", "many"}, "--max-files requires a positive integer"},
		{"query two dirs", []string{"query", "--terms", "a", "x", "y"}, "takes one directory"},
		{"lookup missing file", []string{"lookup", "Foo", "--file"}, "--file requires a value"},
		{"lookup missing fields", []string{"lookup", "Foo", "--fields"}, "--fields requires a comma-separated list"},
		{"lookup bad format", []string{"lookup", "Foo", "--format", "xml"}, "--format wants"},
		{"change-impact missing file", []string{"change-impact", "Foo.bar", "--file"}, "--file requires a value"},
		{"change-impact two dirs", []string{"change-impact", "Foo.bar", "x", "y"}, "takes one directory"},
		{"node missing file", []string{"node", "Foo", "--file"}, "--file requires a value"},
		{"resolve bad format", []string{"resolve", "Foo", "--format", "csv"}, "--format wants"},
		{"edges bad direction", []string{"edges", "Foo", "--direction", "up"}, "--direction wants out|in|both"},
		{"edges missing kinds", []string{"edges", "Foo", "--kinds"}, "--kinds requires a comma-separated list"},
		{"references bad format", []string{"references", "Foo", "--format"}, "--format requires"},
		{"rename-plan bad format", []string{"rename-plan", "Foo", "Bar", "--format", "x"}, "--format wants"},
		{"missing-implementations bad format", []string{"missing-implementations", "Foo", "--format", "x"}, "--format wants"},
		{"dead-code missing roots", []string{"dead-code", "--roots"}, "--roots requires a comma-separated list"},
		{"verify missing removed", []string{"verify", "--removed"}, "--removed requires a comma-separated list"},
		{"verify missing base", []string{"verify", "--base"}, "--base requires a value"},
		{"verify bad format", []string{"verify", "--format", "yaml"}, "--format wants"},
		{"map bad depth", []string{"map", "--depth", "deep"}, "--depth requires a positive integer"},
		{"map bad expand", []string{"map", "--expand", "a->"}, "--expand wants 'from->to'"},
		{"cycles bad depth", []string{"cycles", "--depth", "x"}, "--depth requires a positive integer"},
		{"arch missing deny", []string{"arch", "--deny"}, "--deny requires a value"},
		{"feedback bad rating", []string{"feedback", "--rating", "great"}, "--rating wants an integer 0-5"},
		{"feedback missing tool", []string{"feedback", "--rating", "3", "--tool"}, "--tool requires a value"},
		{"serve bad port", []string{"serve", "--port", "http"}, "--port requires a positive integer"},
		{"serve port out of range", []string{"serve", "--port", "70000"}, "--port must be 1-65535"},
		{"watch bad debounce", []string{"watch", "--debounce", "soon"}, "--debounce wants a positive duration"},
		{"index unknown flag", []string{"index", "--allow-heurstic"}, "unknown flag"},
		{"status unknown flag", []string{"status", "--short"}, "unknown flag"},
		{"doctor two dirs", []string{"doctor", "a", "b"}, "takes one directory"},
		{"init missing print-config", []string{"init", "--print-config"}, "--print-config requires a value"},
		{"init unknown flag", []string{"init", "--harnes", "claude"}, "unknown flag"},
		{"assist missing model", []string{"assist", "--model"}, "--model requires a value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCLIFile(t, dir, "README.md", "line 1 Needle\n")
			t.Chdir(dir)
			var rc int
			stderr := captureStderr(func() {
				_ = captureStdout(func() { rc = Run(tc.args) })
			})
			if rc != 2 {
				t.Fatalf("rc=%d, want 2; stderr:\n%s", rc, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr does not name the problem %q:\n%s", tc.want, stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, ".grove")); !os.IsNotExist(err) {
				t.Fatalf("index opened before the usage error (.grove exists)")
			}
		})
	}
}

// Well-formed values still reach the command: the validation only rejects.
func TestWellFormedFlagValuesStillWork(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, dir, "README.md", "one\ntwo\nthree\nfour\n")
	t.Chdir(dir)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"read", "README.md", "--from", "2", "--to", "3"}, "lines 2-3 of 4"},
		{[]string{"read", "README.md", "--from=3"}, "lines 3-4 of 4"},
		{[]string{"read", "README.md", "--offset", "2", "--limit", "1"}, "lines 2-2 of 4"},
		{[]string{"read", "README.md", "--to", "2"}, "lines 1-2 of 4"},
	}
	for _, tc := range cases {
		var rc int
		out := captureStdout(func() { rc = Run(tc.args) })
		if rc != 0 || !strings.Contains(out, tc.want) {
			t.Errorf("%v: rc=%d, want 0 and %q in:\n%s", tc.args, rc, tc.want, out)
		}
	}
}
