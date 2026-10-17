package mcp

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

const grepShapeSample = `// root: /work/repo
// match lists restricted to path=["src/main/java"]; other files were not searched
// First source anchors from search matches (ranked, not an exhaustive inventory):
//   src/A.java:845  [name-exact; 2 term(s)]
//   src/B.java:1661  [text; 2 term(s)]
── findThing ──
// COMPLETE — 4 exact matches across 2 files
symbols (2):
  method A.findThing  src/A.java:845-856  public Thing findThing(JavaType containerType, AnnotatedMemb...  [name-exact]
  method B.findThing  src/B.java:208-237  public Thing findThing(Ctx ctxt, Annotate...  [name-exact]
// locator result — use the prism tool with op=lookup for known symbol bodies, op=read for a known file/range, or op=query for related implementations, callers, and tests
src/A.java:
  843-      * @since 3.0
  844-      */
  845:     public Thing findThing(JavaType containerType,  [A.findThing]
  846-             AnnotatedMember accessor)
  849:             return _config.findThing(this,  [A.findThing]
  850-                     accessor, containerType);
src/B.java:
  207- 
  208:     public Thing findThing(Ctx ctxt,  [B.findThing]
  209-             AnnotatedMember accessor)
`

func matchedLines(s string) []string {
	re := regexp.MustCompile(`^\s+\d+:`)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if re.MatchString(l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func TestGrepShapeKeepsEveryMatchedLineAndDropsTheDecoration(t *testing.T) {
	got := grepShapeSearchText(grepShapeSample, map[string]any{"terms": []any{"findThing"}})
	if strings.Join(matchedLines(got), "\n") != strings.Join(matchedLines(grepShapeSample), "\n") {
		t.Fatalf("a matched line changed:\n%s", got)
	}
	for _, gone := range []string{"// root:", "First source anchors", "match lists restricted", "symbols (2):", "method A.findThing", "  843-", "  207-"} {
		if strings.Contains(got, gone) {
			t.Errorf("grep shape kept %q:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"// COMPLETE — 4 exact matches across 2 files", "src/A.java:", "src/B.java:", "[A.findThing]", "── findThing ──"} {
		if !strings.Contains(got, kept) {
			t.Errorf("grep shape lost %q:\n%s", kept, got)
		}
	}
	if len(got) >= len(grepShapeSample)*3/4 {
		t.Errorf("expected a clearly smaller answer, got %d of %d bytes", len(got), len(grepShapeSample))
	}
}

func TestGrepShapeOptInsBringTheContentBack(t *testing.T) {
	asked := grepShapeSearchText(grepShapeSample, map[string]any{"context": 3})
	for _, want := range []string{"  843-", "  846-", "  207-"} {
		if !strings.Contains(asked, want) {
			t.Errorf("context=3 must keep its windows, lost %q:\n%s", want, asked)
		}
	}
	body := "\n// Exact source for bounded enclosing hits selected by evidence rank; other matches remain locators, and inventory completeness is reported above:\n**`src/A.java`**\n```java\n845→body\n```\n"
	broad := strings.Replace(grepShapeSample, "4 exact matches", "25 exact matches", 1) + body
	if got := grepShapeSearchText(broad, nil); strings.Contains(got, "Exact source") || strings.Contains(got, "845→") {
		t.Errorf("a 25-match answer must not carry a body by default:\n%s", got)
	}
	if got := grepShapeSearchText(broad, map[string]any{"include_bodies": true}); !strings.Contains(got, "845→body") {
		t.Errorf("include_bodies=true must keep the body:\n%s", got)
	}
	narrow := strings.Replace(broad, "25 exact matches", "6 exact matches", 1)
	if got := grepShapeSearchText(narrow, nil); !strings.Contains(got, "845→body") {
		t.Errorf("a 6-match answer keeps its body:\n%s", got)
	}
}

func TestGrepShapeKeepsWarningsAndTheRootOfAnEmptyAnswer(t *testing.T) {
	empty := "// root: /work/repo\n── nothing ──\n// no matches — search completed (not truncated, not timed out)\n"
	if got := grepShapeSearchText(empty, nil); !strings.Contains(got, "// root: /work/repo") {
		t.Errorf("an answer with no hits must still name its root:\n%s", got)
	}
	warn := "// root: /work/repo\n// match lists restricted to path=[\"x\"]; paths outside the root were NOT searched: [../y]\n// showing 5 of AT LEAST 40 matches — a SAMPLE\nsrc/A.java:\n  10:   foo()  [A.f]\n"
	got := grepShapeSearchText(warn, nil)
	for _, want := range []string{"NOT searched", "a SAMPLE", "10:   foo()"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning or hit lost %q:\n%s", want, got)
		}
	}
}

// End to end through the compact server: the same query in both modes returns
// the same matched lines, and the grep shape is not larger.
func TestGrepShapeEndToEndSameMatchesFewerBytes(t *testing.T) {
	files := map[string]string{
		"pkg/core.py": "class Group:\n    def get_thing(self, name):\n        return None\n\n\nclass Sub(Group):\n    def get_thing(self, name):\n        return super().get_thing(name)\n\n\ndef use(g: Group):\n    return g.get_thing('x')\n",
		"pkg/more.py": "from pkg.core import Group\n\n\ndef other(g: Group):\n    a = 1\n    return g.get_thing('y')\n",
	}
	run := func(mode string) string {
		old := searchShapeMode
		searchShapeMode = mode
		defer func() { searchShapeMode = old }()
		srv := compactFixture(t, files)
		return callCompact(t, srv, "search", map[string]any{"terms": []string{"get_thing"}, "scope": "text"})
	}
	base, grep := run("legacy"), run("")
	if strings.Join(matchedLines(base), "\n") != strings.Join(matchedLines(grep), "\n") {
		t.Fatalf("matched lines differ between modes:\nbase:\n%s\ngrep:\n%s", base, grep)
	}
	if len(matchedLines(grep)) == 0 {
		t.Fatalf("fixture produced no matched lines:\n%s", grep)
	}
	if len(grep) >= len(base) {
		t.Errorf("grep shape is not smaller: %d vs %d bytes\nbase:\n%s\ngrep:\n%s", len(grep), len(base), base, grep)
	}
}

func TestGrepShapeKeepsWhatAnExplicitScopeAndUniqueBodiesAsked(t *testing.T) {
	if got := grepShapeSearchText(grepShapeSample, map[string]any{"scope": "both"}); !strings.Contains(got, "symbols (2):") || !strings.Contains(got, "method A.findThing") {
		t.Errorf("scope=both asked for the symbols block, it must stay:\n%s", got)
	}
	unique := grepShapeSample + "\n// Exact source for the unique exact-name match in the first batched term; other hits remain locators (already read; do not fetch it again):\n**`src/A.java`**\n```java\n845→body\n```\n"
	if got := grepShapeSearchText(unique, nil); !strings.Contains(got, "845→body") {
		t.Errorf("a unique-symbol body is the answer and must stay:\n%s", got)
	}
	plainEcho := "// match lists restricted to path=[\"src\"]; other files were not searched\n10:   x\n"
	more := "// match lists restricted to path=[\"src\"]; text search was not run\n"
	if got := grepShapeSearchText(plainEcho+more, nil); strings.Contains(got, "other files were not searched") || !strings.Contains(got, "text search was not run") {
		t.Errorf("only the plain echo goes, a richer scope note stays:\n%s", got)
	}
}
