package mcp

// TestVerify_VsGrepBaseline is the engine-ceiling A/B for prism_verify: same
// diff, same ground truth, two arms — prism_verify (type-resolved graph) and
// a grep baseline (the realistic non-graph check: flag every call site
// naming the changed method that the diff did not touch). No LLM in the
// loop — this isolates the mechanism (type resolution vs name matching),
// matching the standing methodology in provasign/research/harness/AB-CODEGRAPH.md.
//
// The fixture is built so the two arms are told apart by the ONE thing a
// graph has that grep does not: type information. Two unrelated types share
// a same-named, same-arity method (Handle). One (S) is the method that
// changed and must be verified; the other (Q, R — decoys) never changes and
// must never be flagged. Grep cannot see the receiver's type, so it either
// under-reports (a narrow regex tied to the old call shape) or over-reports
// (a name-only regex, the realistic "search for all .Handle( calls" a human
// or agent actually runs). This tests the over-report shape, because it is
// the one real tools use: a narrow regex requires already knowing the exact
// textual delta, which defeats the purpose of a completeness check.

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// handleFixture builds a repo with:
//   - type S, method Handle(string) -> Handle(string, int): the changed
//     contract. nCallers real call sites across multiple files.
//   - type Q, type R: unrelated types with their OWN stable Handle(string,
//     int) method (same name, same post-change arity, never modified). Their
//     call sites are decoys: same source text as an updated S caller,
//     textually indistinguishable without type resolution.
func handleFixture(t *testing.T, dir string, nCallers, nDecoys int) (files []string, realSites map[string]bool, decoySites map[string]bool) {
	t.Helper()
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
	write("go.mod", "module example.com/hb\n\ngo 1.26\n")
	write("core/types.go", "package core\n\n"+
		"type S struct{}\n\nfunc (S) Handle(x string) string { return x }\n\n"+
		"type Q struct{}\n\nfunc (Q) Handle(x string, opts int) string { return x }\n\n"+
		"type R struct{}\n\nfunc (R) Handle(x string, opts int) string { return x }\n")

	realSites = map[string]bool{}
	decoySites = map[string]bool{}
	files = []string{}
	// Spread real S callers and decoy Q/R callers across several files so
	// the check is repo-scale, not single-file.
	nFiles := 4
	for f := 0; f < nFiles; f++ {
		rel := fmt.Sprintf("app/f%d.go", f)
		files = append(files, rel)
		var body string
		body = "package app\n\nimport \"example.com/hb/core\"\n\n"
		line := 5
		for i := 0; i < nCallers/nFiles; i++ {
			id := f*100 + i
			body += fmt.Sprintf("func CallS%d() string { var s core.S; return s.Handle(%q) }\n", id, fmt.Sprintf("v%d", id))
			realSites[fmt.Sprintf("%s:%d", rel, line)] = true
			line++
		}
		for i := 0; i < nDecoys/nFiles; i++ {
			id := f*100 + i
			typ := "Q"
			if i%2 == 1 {
				typ = "R"
			}
			body += fmt.Sprintf("func CallDecoy%d() string { var d core.%s; return d.Handle(%q, 1) }\n", id, typ, fmt.Sprintf("v%d", id))
			decoySites[fmt.Sprintf("%s:%d", rel, line)] = true
			line++
		}
		write(rel, body)
	}
	return files, realSites, decoySites
}

// grepBaseline simulates the realistic non-graph completeness check: after
// the diff, search the whole tree for every call to the changed method NAME
// (".Handle(") that the diff did not touch. This is what an agent using
// grep — not a graph — actually runs: it has no receiver-type information,
// so it cannot exclude decoy types.
func grepBaseline(t *testing.T, dir string, changedRanges map[string][]lineRange) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`\.Handle\(`)
	flagged := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range splitLines(string(content)) {
			if !re.MatchString(line) {
				continue
			}
			ln := i + 1
			if lineTouched(changedRanges, rel, ln) {
				continue // the diff already touched this line
			}
			flagged[fmt.Sprintf("%s:%d", rel, ln)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return flagged
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

type arm struct {
	name                       string
	tp, fp, fn                 int // true positive / false positive / false negative, summed over trials
}

func (a *arm) score(flagged, real map[string]bool) {
	for k := range real {
		if flagged[k] {
			a.tp++
		} else {
			a.fn++
		}
	}
	for k := range flagged {
		if !real[k] {
			a.fp++
		}
	}
}

func (a *arm) recall() float64 {
	if a.tp+a.fn == 0 {
		return 1
	}
	return float64(a.tp) / float64(a.tp+a.fn)
}
func (a *arm) precision() float64 {
	if a.tp+a.fp == 0 {
		return 1
	}
	return float64(a.tp) / float64(a.tp+a.fp)
}
func (a *arm) f1() float64 {
	p, r := a.precision(), a.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// TestVerify_VsGrepBaseline runs T seeded trials. Each trial: build the
// fixture (nCallers real S sites, nDecoys unrelated-type sites), commit as
// base, change S.Handle's signature, update a RANDOM subset of the real
// sites (simulating an agent's incomplete fix), leave 100% of decoys
// untouched (they never needed updating — that is the point). Score both
// arms against the same ground truth: real sites NOT updated = true missed
// sites; decoy sites are never real, so flagging them is a false positive.
func TestVerify_VsGrepBaseline(t *testing.T) {
	const trials = 12
	const nCallers, nDecoys = 12, 12
	rng := rand.New(rand.NewSource(2026))

	prismArm := &arm{name: "prism verify (type-resolved)"}
	grepArm := &arm{name: "grep baseline (name-only)"}
	var prismFP, grepFP []string

	for trial := 0; trial < trials; trial++ {
		dir := t.TempDir()
		files, realSites, decoySites := handleFixture(t, dir, nCallers, nDecoys)

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

		// Change S.Handle's signature.
		typesPath := filepath.Join(dir, "core/types.go")
		orig, _ := os.ReadFile(typesPath)
		newTypes := regexp.MustCompile(`func \(S\) Handle\(x string\) string`).
			ReplaceAllString(string(orig), "func (S) Handle(x string, opts int) string")
		if newTypes == string(orig) {
			t.Fatal("signature substitution did not match")
		}
		os.WriteFile(typesPath, []byte(newTypes), 0o644)

		// Update a random ~50% subset of REAL sites to the new call shape;
		// decoys are never touched (their Handle never changed).
		siteKeys := make([]string, 0, len(realSites))
		for k := range realSites {
			siteKeys = append(siteKeys, k)
		}
		sort.Strings(siteKeys)
		updated := map[string]bool{}
		for _, k := range siteKeys {
			if rng.Intn(2) == 1 {
				updated[k] = true
			}
		}
		for _, rel := range files {
			p := filepath.Join(dir, rel)
			b, _ := os.ReadFile(p)
			content := string(b)
			for i, line := range splitLines(content) {
				key := fmt.Sprintf("%s:%d", rel, i+1)
				if updated[key] && realSites[key] {
					// Rewrite this one real call site: s.Handle("v..") -> s.Handle("v..", 1)
					newLine := regexp.MustCompile(`\.Handle\((\"[^\"]*\")\)`).
						ReplaceAllString(line, ".Handle($1, 1)")
					content = replaceLine(content, i, newLine)
				}
			}
			os.WriteFile(p, []byte(content), 0o644)
		}

		gc := grove.NewClient("", "").WithTokenFromDir(dir)
		if err := gc.EnsureRunning(t.Context()); err != nil {
			t.Fatalf("grove ensure: %v", err)
		}
		h := NewHandler(config.Default(), dir, gc)
		if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
			t.Fatalf("index: %v", err)
		}

		out, err := h.Invoke("prism_verify", map[string]any{})
		if err != nil {
			t.Fatalf("trial %d: prism_verify: %v", trial, err)
		}
		prismFlagged := map[string]bool{}
		for _, ms := range mustJSON(t, out.(map[string]any)["missedSites"]) {
			prismFlagged[fmt.Sprintf("%v:%v", ms["file"], ms["line"])] = true
		}

		changedRanges, err := gitChangedRanges(dir, "HEAD")
		if err != nil {
			t.Fatalf("trial %d: git diff: %v", trial, err)
		}
		grepFlagged := grepBaseline(t, dir, changedRanges)

		trueMissed := map[string]bool{}
		for k := range realSites {
			if !updated[k] {
				trueMissed[k] = true
			}
		}
		prismArm.score(prismFlagged, trueMissed)
		grepArm.score(grepFlagged, trueMissed)
		for k := range decoySites {
			if prismFlagged[k] {
				prismFP = append(prismFP, fmt.Sprintf("trial%d:%s", trial, k))
			}
			if grepFlagged[k] {
				grepFP = append(grepFP, fmt.Sprintf("trial%d:%s", trial, k))
			}
		}

		gc.Shutdown()
	}

	t.Logf("\n"+
		"=== prism_verify vs grep baseline — %d trials, %d real sites + %d decoy (unrelated-type) sites per trial ===\n"+
		"%-32s recall=%.3f  precision=%.3f  F1=%.3f   (tp=%d fp=%d fn=%d)\n"+
		"%-32s recall=%.3f  precision=%.3f  F1=%.3f   (tp=%d fp=%d fn=%d)\n"+
		"decoy false positives: prism=%d/%d  grep=%d/%d\n",
		trials, nCallers, nDecoys,
		prismArm.name, prismArm.recall(), prismArm.precision(), prismArm.f1(), prismArm.tp, prismArm.fp, prismArm.fn,
		grepArm.name, grepArm.recall(), grepArm.precision(), grepArm.f1(), grepArm.tp, grepArm.fp, grepArm.fn,
		len(prismFP), trials*nDecoys, len(grepFP), trials*nDecoys,
	)

	if prismArm.precision() < 0.99 {
		t.Errorf("prism_verify precision = %.3f, want ~1.0 (type resolution should exclude all decoys); false positives: %v",
			prismArm.precision(), prismFP)
	}
	if prismArm.recall() < 0.99 {
		t.Errorf("prism_verify recall = %.3f, want ~1.0", prismArm.recall())
	}
}

func replaceLine(content string, idx int, newLine string) string {
	lines := splitLines(content)
	if idx >= len(lines) {
		return content
	}
	lines[idx] = newLine
	out := ""
	for i, l := range lines {
		out += l
		if i < len(lines)-1 || content[len(content)-1] == '\n' {
			out += "\n"
		}
	}
	return out
}
