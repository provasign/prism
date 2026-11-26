package core

import (
	"strings"
	"testing"
)

// A module-qualified path (werkzeug.utils.redirect) answers with the symbol's
// change set instead of "no type named utils" (Codex agent, 2026-10-17).
func TestChangeImpactResolvesModuleQualifiedPath(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"src/werkzeug/utils.py":             "def redirect(location, code=302):\n    return location\n",
		"src/werkzeug/routing.py":           "from werkzeug.utils import redirect\n\n\ndef go(target):\n    return redirect(target)\n",
		"src/werkzeug/wrappers/response.py": "class Response:\n    def get_data(self, as_text=False):\n        return b''\n\n\ndef drain(r: Response):\n    return r.get_data()\n",
		"src/other/utils.py":                "def helper():\n    return 1\n",
	})
	out := callCompact(t, srv, "change_impact", map[string]any{"name": "werkzeug.utils.redirect"})
	for _, want := range []string{"routing.py", "module path", "src/werkzeug/utils.py"} {
		if !strings.Contains(out, want) {
			t.Fatalf("module path answer lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no type named") {
		t.Fatalf("module path still reported as a missing type:\n%s", out)
	}

	out = callCompact(t, srv, "change_impact", map[string]any{"name": "werkzeug.wrappers.Response.get_data"})
	if !strings.Contains(out, "drain") || !strings.Contains(out, "Response.get_data") {
		t.Fatalf("module path with a type did not resolve to Response.get_data:\n%s", out)
	}

	// A path no file matches is not guessed from the bare leaf.
	errText := callCompactErr(t, srv, "change_impact", map[string]any{"name": "flask.helpers.redirect"})
	if !strings.Contains(errText, "no type named") || strings.Contains(errText, "routing.py") {
		t.Fatalf("unmatched module path must keep the error and not guess:\n%s", errText)
	}
}

func TestModuleMatchesFile(t *testing.T) {
	for _, tc := range []struct {
		file string
		mods []string
		want bool
	}{
		{"src/werkzeug/utils.py", []string{"werkzeug", "utils"}, true},
		{"werkzeug/utils/__init__.py", []string{"werkzeug", "utils"}, true},
		{"src/werkzeug/utils.py", []string{"utils"}, true},
		{"src/other/utils.py", []string{"werkzeug", "utils"}, false},
		{"pkg/router/index.ts", []string{"pkg", "router"}, true},
		{"internal/mcp/tools.go", []string{"mcp"}, false},
	} {
		if got := moduleMatchesFile(tc.file, tc.mods); got != tc.want {
			t.Errorf("moduleMatchesFile(%q, %v) = %v, want %v", tc.file, tc.mods, got, tc.want)
		}
	}
}

// A Go qualifier names a package: its directory, or the package clause of a
// module-root package. memfs.New is not memfs/storage.go's storage.New;
// build-tag twins (osfs/os.go, osfs/os_js.go) are one symbol; model.Time is
// the type, not its method Time.Time; expfmt.Format is not another package's
// Format. Each answer renders as text (2026-11-13: these fell back to JSON).
func TestChangeImpactResolvesGoPackageQualifiers(t *testing.T) {
	srv := compactFixture(t, map[string]string{
		"go.mod":           "module example.com/lib\n\ngo 1.22\n",
		"flag.go":          "package pflag\n\nfunc NewFlagSet() int { return 1 }\n\nfunc use() int { return NewFlagSet() }\n",
		"memfs/memory.go":  "package memfs\n\nfunc New() int { return 1 }\n",
		"memfs/storage.go": "package memfs\n\ntype storage struct{}\n\nfunc (s *storage) New() int { return New() }\n",
		"osfs/os.go":       "//go:build !js\n\npackage osfs\n\nfunc New() int { return 2 }\n",
		"osfs/os_js.go":    "//go:build js\n\npackage osfs\n\nfunc New() int { return 3 }\n",
		"model/time.go":    "package model\n\ntype Time int64\n\nfunc (t Time) Time() int64 { return int64(t) }\n\nfunc Now() Time { return 0 }\n",
		"expfmt/expfmt.go": "package expfmt\n\ntype Format string\n\nfunc Of() Format { return \"\" }\n",
		"promslog/slog.go": "package promslog\n\ntype Format struct{}\n\nfunc Make() *Format { return nil }\n",
	})
	for _, tc := range []struct{ name, file string }{
		{"pflag.NewFlagSet", "flag.go"},
		{"memfs.New", "memfs/memory.go"},
		{"osfs.New", "osfs/os.go"},
		{"model.Time", "model/time.go"},
		{"expfmt.Format", "expfmt/expfmt.go"},
	} {
		out := callCompact(t, srv, "change_impact", map[string]any{"name": tc.name})
		if !strings.Contains(out, "module path: resolved") || !strings.Contains(out, tc.file) {
			t.Errorf("%s did not resolve to %s:\n%s", tc.name, tc.file, out)
		}
		if strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Errorf("%s answered in JSON:\n%s", tc.name, out)
		}
	}
	if out := callCompact(t, srv, "change_impact", map[string]any{"name": "expfmt.Format"}); strings.Contains(out, "promslog/slog.go:3:6") || !strings.Contains(out, "expfmt/expfmt.go:3:6") {
		t.Errorf("expfmt.Format answered with promslog.Format:\n%s", out)
	}
	if out := callCompact(t, srv, "change_impact", map[string]any{"name": "model.Time"}); strings.Contains(out, "Time.Time — change-impact") {
		t.Errorf("model.Time answered as the method Time.Time:\n%s", out)
	}
}
