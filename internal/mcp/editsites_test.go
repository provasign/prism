package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

// editSites lists every occurrence the change touches in grep's shape:
// the declaration's and overrides' name lines and each call position, with
// the line text, so a text edit needs no second search.
func TestChangeImpactEditSites(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"pkg/core.py": "class Group:\n    def get_command(self, ctx, name):\n        \"\"\"Look up get_command.\"\"\"\n        return None\n\n\nclass Sub(Group):\n    def get_command(self, ctx, name):\n        return super().get_command(ctx, name)\n\n\ndef resolve(g: Group):\n    return g.get_command(None, 'x')\n",
	})
	raw, err := h.Invoke("prism_change_impact", map[string]any{"query": "Group.get_command"})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	rows := toStrings(anySlice(out["editSites"]))
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"pkg/core.py:2:9: def get_command(self, ctx, name):",
		"pkg/core.py:8:9: def get_command(self, ctx, name):",
		"pkg/core.py:13:",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in editSites:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "pkg/core.py:3:") {
		t.Errorf("a docstring is no edit site:\n%s", joined)
	}
	other := strings.Join(toStrings(anySlice(out["otherMentions"])), "\n")
	if !strings.Contains(other, "Every line naming get_command in this repository (whole word, 5): 4 in editSites above, 0 code line(s) naming something else, 0 string literal(s), 1 comment/doc line(s). In test files: 0, 0, 0 and 0 of those.") {
		t.Errorf("partition wrong:\n%s", other)
	}
	rendered, ok := renderChangeImpactAsText(out)
	if !ok || !strings.Contains(rendered, "editSites (") {
		t.Fatalf("editSites not rendered:\n%s", rendered)
	}
}

func toStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strings.TrimSpace(v.(string)))
	}
	return out
}

// evidenceOnly drops the line lists (editSites and the every-line
// partition, which name other symbols' lines on purpose) so a test can
// check what the evidence sections say.
func evidenceOnly(rendered string) string {
	var b strings.Builder
	skip := false
	for _, l := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(l, "editSites (") {
			skip = true
		}
		if skip && (strings.HasPrefix(l, "familyCompleteness:") || strings.HasPrefix(l, "callerCoverage:") || strings.HasPrefix(l, "//")) {
			skip = false
		}
		if !skip {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// A field rename gets the same edit list: the declaration and every
// confirmed access line (wide bed, cli Command.Subcommands had none).
func TestChangeImpactEditSitesField(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n",
		"command.go": "package app\n\ntype Command struct {\n\tSubcommands []*Command\n\tName        string\n}\n\n" +
			"// Visible returns the Subcommands.\nfunc (c *Command) Visible() []*Command {\n\treturn c.Subcommands\n}\n\n" +
			"func build() *Command {\n\treturn &Command{Subcommands: nil}\n}\n",
	})
	raw, err := h.Invoke("prism_change_impact", map[string]any{"query": "Command.Subcommands"})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	joined := strings.Join(toStrings(anySlice(out["editSites"])), "\n")
	for _, want := range []string{
		"command.go:4:2: Subcommands []*Command",
		"command.go:10:11: return c.Subcommands",
		"command.go:14:18: return &Command{Subcommands: nil}",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in editSites:\n%s", want, joined)
		}
	}
	other := strings.Join(toStrings(anySlice(out["otherMentions"])), "\n")
	if !strings.Contains(other, "(whole word, 4): 3 in editSites above, 0 code line(s) naming something else, 0 string literal(s), 1 comment/doc line(s).") {
		t.Errorf("partition wrong:\n%s", other)
	}
	rendered, ok := renderChangeImpactAsText(out)
	if !ok || !strings.Contains(rendered, "editSites (3;") {
		t.Fatalf("editSites not rendered:\n%s", rendered)
	}
}

// Past editSitesMax the list is per file as line:col positions, never
// dropped (gson TypeAdapter.write: 137 declarations).
func TestEditCollectorCompactForm(t *testing.T) {
	dir := t.TempDir()
	var src strings.Builder
	for i := 0; i < editSitesMax+10; i++ {
		src.WriteString("x = write(1)\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.py"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.py"), []byte("y = write(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newEditCollector(dir)
	for i := 1; i <= editSitesMax+10; i++ {
		c.add("a.py", i, 0, "write")
	}
	c.add("b.py", 1, 0, "write")
	rows, keys := c.finish()
	if len(rows) != 2 || len(keys) != editSitesMax+11 {
		t.Fatalf("rows=%d keys=%d:\n%s", len(rows), len(keys), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[0], "a.py (160): 1:5 2:5 3:5") || rows[1] != "b.py (1): 1:5" {
		t.Errorf("compact rows wrong:\n%s", strings.Join(rows, "\n"))
	}
	out := map[string]any{"editSites": rows, "editSiteCount": c.count()}
	if got := formatEditSitesText(out); !strings.HasPrefix(got, "editSites (161 in 2 files;") {
		t.Errorf("header wrong:\n%s", got)
	}
}

// When the answer is partial, code the graph did not tie to the change is
// not called "another" symbol: some of it may be uses (zod _zod.parse).
func TestOtherMentionsPartialLabel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ts"), []byte("interface I {\n  parse(): void\n}\nx._zod.parse()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Root: dir}
	keys := map[string]bool{"a.ts:2": true}
	got := strings.Join(h.impactOtherMentions(context.Background(), &grove.ChangeImpactResult{}, "parse", keys, nil, true), "\n")
	if !strings.Contains(got, "1 code line(s) not tied to this change") || !strings.Contains(got, "some of these may be uses") || strings.Contains(got, "naming another") {
		t.Errorf("partial label wrong:\n%s", got)
	}
	got = strings.Join(h.impactOtherMentions(context.Background(), &grove.ChangeImpactResult{}, "parse", keys, nil, false), "\n")
	if !strings.Contains(got, "code naming another parse (not in this change) (1):") {
		t.Errorf("closed label wrong:\n%s", got)
	}
}

// A caller's call line that editSites prints with its text is not printed
// again as caller evidence (bench29: every change_impact answer had both).
func TestCallerEvidenceNotRepeatedFromEditSites(t *testing.T) {
	h := evidenceHandler(t, map[string]string{
		"pkg/core.py": "class Group:\n    def get_command(self, ctx, name):\n        return None\n\n\ndef resolve(g: Group):\n    return g.get_command(None, 'x')\n",
	})
	raw, err := h.Invoke("prism_change_impact", map[string]any{"query": "Group.get_command"})
	if err != nil {
		t.Fatal(err)
	}
	rendered, ok := renderChangeImpactAsText(raw.(map[string]any))
	if !ok {
		t.Fatal("not rendered as text")
	}
	if n := strings.Count(rendered, "return g.get_command(None, 'x')"); n != 1 {
		t.Errorf("call line printed %d times, want once (in editSites):\n%s", n, rendered)
	}
	if !strings.Contains(rendered, "caller call line(s) are in editSites above") {
		t.Errorf("missing the not-repeated note:\n%s", rendered)
	}
}

func TestBlockCommentOpenSkipsStrings(t *testing.T) {
	for line, want := range map[string]int{
		`subApp.use("/admin/**", fn)`: -1,
		`.get('/**:slug', h)`:         -1,
		"x = `/*`":                    -1,
		`// see /* here`:              -1,
		`/* real`:                     0,
		`a(); /* real`:                5,
		`s = "a\"/*"; /* c`:           13,
	} {
		if got := blockCommentOpen(line); got != want {
			t.Errorf("blockCommentOpen(%q) = %d, want %d", line, got, want)
		}
	}
}
