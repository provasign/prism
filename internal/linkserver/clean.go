package linkserver

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLineRunes caps a source line shown next to a site. Real code lines
// are shorter (the longest in prism, grove and mason is 216 characters;
// 0.05% of grafana's lines exceed 300); a minified file or a hostile upload
// is not, and one such line used to put 200 KB into an answer.
const MaxLineRunes = 300

// MaxRefsPerKey is how many references to one symbol a bundle may carry.
// A real repository has far fewer; a bundle with more is refused at
// upload rather than flooding every answer about that symbol.
const MaxRefsPerKey = 20000

// CleanLine makes text from another repository safe to print: control
// characters (terminal escape sequences among them) and bidirectional
// overrides are removed, tabs become spaces, and a line longer than
// MaxLineRunes is cut to a window around near (the referenced name) with
// "…" where it was cut.
func CleanLine(s, near string) string {
	s = stripControls(s)
	if utf8.RuneCountInString(s) <= MaxLineRunes {
		return s
	}
	r := []rune(s)
	at := 0
	if near != "" {
		if i := strings.Index(s, near); i >= 0 {
			at = utf8.RuneCountInString(s[:i])
		}
	}
	start := at - MaxLineRunes/3
	if start < 0 {
		start = 0
	}
	end := start + MaxLineRunes
	if end > len(r) {
		end = len(r)
		start = end - MaxLineRunes
	}
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

func stripControls(s string) string {
	clean := true
	for _, c := range s {
		if dropRune(c) || c == '\t' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c == '\t':
			b.WriteByte(' ')
		case dropRune(c):
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// dropRune: C0 and C1 controls, DEL, and the bidirectional embedding,
// override and isolate characters that can make text display differently
// from what it says ("Trojan Source").
func dropRune(c rune) bool {
	if c == '\t' {
		return false
	}
	if unicode.IsControl(c) || c == utf8.RuneError {
		return true
	}
	switch {
	case c >= 0x202A && c <= 0x202E, c >= 0x2066 && c <= 0x2069, c == 0x200E, c == 0x200F:
		return true
	}
	return false
}

// CleanName cleans a short identifier-like field (a file path, an
// enclosing symbol) and caps it at max runes.
func CleanName(s string, max int) string {
	s = stripControls(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// validate refuses a bundle whose identifying fields are malformed or
// absurd, and cleans the text fields it keeps. Run on every upload.
func (b *Bundle) validate() error {
	for name, v := range map[string]struct {
		s   string
		max int
	}{"repo": {b.Repo, 256}, "sha": {b.SHA, 128}, "ref": {b.Ref, 256}} {
		if len(v.s) > v.max || stripControls(v.s) != v.s {
			return fmt.Errorf("bundle field %s is malformed (control characters or longer than %d)", name, v.max)
		}
	}
	perKey := map[string]int{}
	for i := range b.Refs {
		r := &b.Refs[i]
		if len(r.Key) > 1024 || stripControls(r.Key) != r.Key || len(r.File) > 1024 || stripControls(r.File) != r.File {
			return fmt.Errorf("bundle field refs[%d] has a malformed key or file", i)
		}
		perKey[r.Key]++
		if perKey[r.Key] > MaxRefsPerKey {
			return fmt.Errorf("bundle field refs has more than %d references to %s; no real repository has that many", MaxRefsPerKey, r.Key)
		}
		r.Text = CleanLine(r.Text, keyLeaf(r.Key))
		r.From = CleanName(r.From, 512)
	}
	for i := range b.Defs {
		d := &b.Defs[i]
		if len(d.Key) > 1024 || stripControls(d.Key) != d.Key || len(d.File) > 1024 || stripControls(d.File) != d.File {
			return fmt.Errorf("bundle field defs[%d] has a malformed key or file", i)
		}
		d.Name = CleanName(d.Name, 512)
		d.Kind = CleanName(d.Kind, 64)
		d.Signature = CleanLine(d.Signature, "")
	}
	return nil
}

// KeyLeaf is the last name segment of a key: go:mod/pkg#Type.Method -> Method.
func KeyLeaf(key string) string { return keyLeaf(key) }

func keyLeaf(key string) string {
	name := key
	if i := strings.IndexByte(name, '#'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}
