package linkserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanLineRemovesControlsAndBidi(t *testing.T) {
	in := "\x1b]0;PWNED\x07\x1b[2Jreturn lib.Normalize(k)‮ // ⁦evil⁩\tok\x00"
	got := CleanLine(in, "Normalize")
	if strings.ContainsAny(got, "\x1b\x07\x00‮⁦⁩\t") {
		t.Fatalf("control or bidi characters left: %q", got)
	}
	if !strings.Contains(got, "return lib.Normalize(k)") || !strings.Contains(got, " ok") {
		t.Fatalf("code lost: %q", got)
	}
}

// A long line is cut to a window that keeps the referenced name.
func TestCleanLineCapsAroundTheName(t *testing.T) {
	line := strings.Repeat("a", 5000) + "lib.Normalize(k)" + strings.Repeat("b", 5000)
	got := CleanLine(line, "Normalize")
	if n := utf8.RuneCountInString(got); n > MaxLineRunes+2 {
		t.Fatalf("%d runes, cap %d", n, MaxLineRunes)
	}
	if !strings.Contains(got, "lib.Normalize(k)") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("window lost the name or its markers: %q", got)
	}
	short := "return lib.Normalize(k)"
	if CleanLine(short, "Normalize") != short {
		t.Fatal("a normal line was changed")
	}
}

func TestUploadValidation(t *testing.T) {
	s, _ := Open(t.TempDir())
	good := func() Bundle {
		return Bundle{Format: 1, Repo: "example.org/app", SHA: "c1", Ref: "main",
			Refs: []Site{{Key: "go:example.com/lib#F", File: "a.go", Line: 1, From: "main", Text: "\x1b[31m" + strings.Repeat("x", 9000) + "lib.F()"}}}
	}
	if err := s.Put(good()); err != nil {
		t.Fatal(err)
	}
	got := s.Consumers(ConsumersRequest{Keys: []string{"go:example.com/lib#F"}})
	text := got.Repos[0].Sites[0].Text
	if strings.Contains(text, "\x1b") || utf8.RuneCountInString(text) > MaxLineRunes+2 || !strings.Contains(text, "lib.F()") {
		t.Fatalf("stored text not cleaned: %d runes %q", utf8.RuneCountInString(text), text[:40])
	}
	for name, mutate := range map[string]func(*Bundle){
		"control in repo": func(b *Bundle) { b.Repo = "example.org/\x1bapp" },
		"long sha":        func(b *Bundle) { b.SHA = strings.Repeat("a", 200) },
		"control in file": func(b *Bundle) { b.Refs[0].File = "a\x00.go" },
		"too many refs": func(b *Bundle) {
			for i := 0; i <= MaxRefsPerKey; i++ {
				b.Refs = append(b.Refs, Site{Key: "go:example.com/lib#F", File: fmt.Sprintf("f%d.go", i), Line: 1})
			}
		},
	} {
		b := good()
		b.SHA = "c2"
		mutate(&b)
		err := s.PutContext(context.Background(), b)
		if err == nil || !strings.Contains(err.Error(), "bundle field") {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}
