package mcp

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// verifyFixture builds a git repo with a method S.Do and six call sites
// across two files, committed as base. Returns the handler, the repo dir,
// and the call-site line of each caller index (0..5).
func verifyFixture(t *testing.T) (*Handler, string, map[int]struct {
	file string
	line int
}) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/v\n\ngo 1.26\n")
	write("core/core.go", coreSrc(false))
	write("use1/a.go", callerSrc("use1", []int{0, 1, 2}, nil))
	write("use2/b.go", callerSrc("use2", []int{3, 4, 5}, nil))

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir,
			"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)

	// Call-site lines: callerSrc puts each caller on its own line —
	// package(1) blank(2) import(3) blank(4) then one func per line.
	sites := map[int]struct {
		file string
		line int
	}{}
	for i := 0; i < 3; i++ {
		sites[i] = struct {
			file string
			line int
		}{"use1/a.go", 5 + i}
	}
	for i := 3; i < 6; i++ {
		sites[i] = struct {
			file string
			line int
		}{"use2/b.go", 5 + (i - 3)}
	}
	return h, dir, sites
}

func coreSrc(newSig bool) string {
	if newSig {
		return "package core\n\ntype S struct{}\n\nfunc (S) Do(x string, n int) string { return x }\n"
	}
	return "package core\n\ntype S struct{}\n\nfunc (S) Do(x string) string { return x }\n"
}

// callerSrc renders a caller package; indexes in updated get the new
// two-argument call form.
func callerSrc(pkg string, ids []int, updated map[int]bool) string {
	src := "package " + pkg + "\n\nimport \"example.com/v/core\"\n\n"
	for _, id := range ids {
		call := fmt.Sprintf("s.Do(%q)", string(rune('a'+id)))
		if updated[id] {
			call = fmt.Sprintf("s.Do(%q, %d)", string(rune('a'+id)), id)
		}
		src += fmt.Sprintf("func C%d() string { var s core.S; return %s }\n", id, call)
	}
	return src
}

// applyAgentChange simulates an agent's edit: the signature changes, and
// only the callers in updated are fixed.
func applyAgentChange(t *testing.T, h *Handler, dir string, updated map[int]bool) {
	t.Helper()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("core/core.go", coreSrc(true))
	write("use1/a.go", callerSrc("use1", []int{0, 1, 2}, updated))
	write("use2/b.go", callerSrc("use2", []int{3, 4, 5}, updated))
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
}

func missedSet(t *testing.T, out any) map[string]bool {
	t.Helper()
	m := out.(map[string]any)
	got := map[string]bool{}
	for _, ms := range mustJSON(t, m["missedSites"]) {
		got[fmt.Sprintf("%s:%v", ms["file"], ms["line"])] = true
	}
	return got
}

func TestToolVerify_CatchesMissedCallers(t *testing.T) {
	h, dir, sites := verifyFixture(t)
	// The "agent" updates callers 0,1,3 and misses 2,4,5.
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 3: true})

	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "incomplete" {
		t.Fatalf("verdict = %v, want incomplete (3 callers missed)", m["verdict"])
	}
	got := missedSet(t, out)
	want := map[string]bool{}
	for _, id := range []int{2, 4, 5} {
		want[fmt.Sprintf("%s:%d", sites[id].file, sites[id].line)] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("MISSED site not reported: %s (got %v)", k, got)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("FALSE ACCUSATION: %s reported missed but was not required-and-untouched", k)
		}
	}
	// The signature change itself must be identified.
	if sigs := mustJSON(t, m["signatureChanges"]); len(sigs) == 0 {
		t.Fatal("signature change not detected")
	}
}

func TestToolVerify_CompleteChangePasses(t *testing.T) {
	h, dir, _ := verifyFixture(t)
	applyAgentChange(t, h, dir, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true})
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "complete" {
		t.Fatalf("verdict = %v, want complete; missed=%v", m["verdict"], m["missedSites"])
	}
}

func TestToolVerify_CleanTree(t *testing.T) {
	h, _, _ := verifyFixture(t)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if v := out.(map[string]any)["verdict"]; v != "clean" {
		t.Fatalf("verdict = %v, want clean", v)
	}
}

// TestVerify_IncompletenessBenchmark is the engine-ceiling measurement for
// the verifier: T seeded trials, each simulating an agent that changes a
// method signature and updates a random subset of the six required call
// sites. The verifier must report exactly the complement — every missed
// site found (recall 1.0), nothing falsely accused (precision 1.0),
// line-precise. This is the change-impact benchmark's oracle, flipped into
// a production gate: the four-tier study measured grep agents finding 5 of
// 8 sites; the sites they miss are exactly what this catches.
func TestVerify_IncompletenessBenchmark(t *testing.T) {
	h, dir, sites := verifyFixture(t)
	rng := rand.New(rand.NewSource(7))
	const trials = 10
	var totalMissed, totalCaught, falseAccusations int
	var wall time.Duration
	for trial := 0; trial < trials; trial++ {
		updated := map[int]bool{}
		for id := 0; id < 6; id++ {
			if rng.Intn(2) == 1 {
				updated[id] = true
			}
		}
		applyAgentChange(t, h, dir, updated)
		start := time.Now()
		out, err := h.Invoke("prism_verify", map[string]any{})
		wall += time.Since(start)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		got := missedSet(t, out)
		for id := 0; id < 6; id++ {
			key := fmt.Sprintf("%s:%d", sites[id].file, sites[id].line)
			switch {
			case !updated[id]:
				totalMissed++
				if got[key] {
					totalCaught++
				} else {
					t.Errorf("trial %d: missed site %s not caught (updated=%v)", trial, key, updated)
				}
				delete(got, key)
			case got[key]:
				falseAccusations++
				t.Errorf("trial %d: updated site %s falsely accused", trial, key)
				delete(got, key)
			}
		}
		for k := range got {
			falseAccusations++
			t.Errorf("trial %d: unexpected missed site %s", trial, k)
		}
	}
	t.Logf("incompleteness benchmark: %d trials, %d/%d missed sites caught, %d false accusations, mean verify wall %v",
		trials, totalCaught, totalMissed, falseAccusations, wall/time.Duration(trials))
}

// TestToolVerify_PythonMissedCallers is the case where no compiler exists to
// save you: a Python method's signature changes, one caller file is updated,
// the other is forgotten. `python -m py_compile` passes on all of it — the
// bug only surfaces at runtime. verify must catch it statically.
func TestToolVerify_PythonMissedCallers(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("svc/core.py", "class Store:\n    def put(self, key):\n        return key\n")
	write("app/writer.py", "from svc.core import Store\n\ndef save(k):\n    s = Store()\n    return s.put(k)\n")
	write("app/backup.py", "from svc.core import Store\n\ndef mirror(k):\n    s = Store()\n    return s.put(k)\n")

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir,
			"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")

	// The "agent" adds a ttl parameter and updates writer.py — backup.py
	// is forgotten. Every file still compiles.
	write("svc/core.py", "class Store:\n    def put(self, key, ttl):\n        return key\n")
	write("app/writer.py", "from svc.core import Store\n\ndef save(k):\n    s = Store()\n    return s.put(k, 60)\n")

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] == "complete" || m["verdict"] == "clean" {
		t.Fatalf("verdict = %v — the forgotten backup.py caller must not pass", m["verdict"])
	}
	if m["verdict"] == "incomplete" {
		found := false
		for _, ms := range mustJSON(t, m["missedSites"]) {
			if ms["file"] == "app/backup.py" {
				found = true
			}
			if ms["file"] == "app/writer.py" {
				t.Errorf("updated caller writer.py falsely accused: %v", ms)
			}
		}
		if !found {
			t.Fatalf("backup.py missed site not reported: %v", m["missedSites"])
		}
	}
	// verdict "review" (unverified seed) is acceptable fail-closed behavior;
	// "incomplete" with the exact site is the target.
	t.Logf("python verdict: %v missed=%v unverified=%v", m["verdict"], m["missedSites"], m["unverifiedSeeds"])
}

// A bare function (not a method) must be verified through the callers
// fallback, not silently passed.
func TestToolVerify_BareFunctionFallback(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/f\n\ngo 1.26\n")
	write("lib/fmt.go", "package lib\n\nfunc Render(x string) string { return x }\n")
	write("app/a.go", "package app\n\nimport \"example.com/f/lib\"\n\nfunc Use() string { return lib.Render(\"a\") }\n")
	write("app/b.go", "package app\n\nimport \"example.com/f/lib\"\n\nfunc Other() string { return lib.Render(\"b\") }\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir,
			"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")

	// Signature change + only a.go updated.
	write("lib/fmt.go", "package lib\n\nfunc Render(x string, w int) string { return x }\n")
	write("app/a.go", "package app\n\nimport \"example.com/f/lib\"\n\nfunc Use() string { return lib.Render(\"a\", 3) }\n")

	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatalf("grove ensure: %v", err)
	}
	t.Cleanup(gc.Shutdown)
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	out, err := h.Invoke("prism_verify", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["verdict"] != "incomplete" {
		t.Fatalf("verdict = %v, want incomplete (b.go caller forgotten); missed=%v unverified=%v",
			m["verdict"], m["missedSites"], m["unverifiedSeeds"])
	}
	foundB := false
	for _, ms := range mustJSON(t, m["missedSites"]) {
		if ms["file"] == "app/b.go" {
			foundB = true
		}
		if ms["file"] == "app/a.go" {
			t.Errorf("updated caller a.go falsely accused: %v", ms)
		}
	}
	if !foundB {
		t.Fatalf("b.go missed site not reported: %v", m["missedSites"])
	}
}

var _ = sort.Strings // keep sort import if assertions change
