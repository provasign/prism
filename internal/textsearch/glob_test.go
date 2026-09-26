package textsearch

import "testing"

func TestMatchGlobRipgrepSemantics(t *testing.T) {
	cases := []struct {
		glob, rel string
		want      bool
	}{
		{"**/types.py", "src/click/types.py", true},
		{"**/types.py", "types.py", true},
		{"**/types.py", "src/click/core.py", false},
		{"*.py", "src/click/types.py", true},
		{"types.py", "src/click/types.py", true},
		{"src/**/*.java", "src/main/java/A.java", true},
		{"src/**/*.java", "test/A.java", false},
		{"tests/**", "tests/unit/test_a.py", true},
		{"tests/**", "src/tests.py", false},
		{"render/*.go", "render/html.go", true},
		{"render/*.go", "render/sub/html.go", false},
		{"**/deser/**/*.java", "src/main/java/x/deser/bean/B.java", true},
		{"./src/", "src/a.go", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.glob, c.rel); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.glob, c.rel, got, c.want)
		}
	}
}
