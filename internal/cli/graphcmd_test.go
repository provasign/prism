package cli

import (
	"strings"
	"testing"
)

func TestGraphFlagErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bad port", []string{"graph", "--port", "http"}, "--port requires a positive integer"},
		{"port out of range", []string{"graph", "--port", "70000"}, "--port must be 1-65535"},
		{"unknown flag", []string{"graph", "--browser"}, "unknown flag"},
		{"two dirs", []string{"graph", "a", "b"}, "takes one directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var rc int
			stderr := captureStderr(func() {
				_ = captureStdout(func() { rc = Run(tc.args) })
			})
			if rc != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("rc=%d stderr=%q, want rc 2 naming %q", rc, stderr, tc.want)
			}
		})
	}
}

func TestGraphHelpDoesNotStartAServer(t *testing.T) {
	out := captureStdout(func() {
		if rc := Run([]string{"graph", "--help"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "prism graph [dir]") || strings.Contains(out, "prism map") {
		t.Errorf("graph --help printed:\n%s", out)
	}
}

func TestGraphURL(t *testing.T) {
	for _, tc := range []struct {
		sub   string
		tests bool
		want  string
	}{
		{"", false, "http://127.0.0.1:8787/"},
		{"internal/cli", false, "http://127.0.0.1:8787/#s=d%3Ainternal%2Fcli"},
		{"", true, "http://127.0.0.1:8787/#t=1"},
	} {
		if got := graphURL(8787, tc.sub, tc.tests); got != tc.want {
			t.Errorf("graphURL(%q, %v) = %q, want %q", tc.sub, tc.tests, got, tc.want)
		}
	}
}
