package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pruneOldLedgers must remove files older than maxAge and leave recent ones.
func TestPruneOldLedgers(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	write := func(name string, modtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, modtime, modtime); err != nil {
			t.Fatal(err)
		}
	}

	write("old.json", now.Add(-40*24*time.Hour))      // 40 days ago — should be pruned
	write("recent.json", now.Add(-5*24*time.Hour))    // 5 days ago  — must survive
	write("fresh.json", now.Add(-1*time.Hour))        // 1 hour ago  — must survive
	write("unrelated.txt", now.Add(-50*24*time.Hour)) // wrong ext — must survive

	pruneOldLedgers(dir, 30*24*time.Hour)

	if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
		t.Error("old.json should have been pruned")
	}
	for _, keep := range []string{"recent.json", "fresh.json", "unrelated.txt"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should survive pruning: %v", keep, err)
		}
	}
}

// cmdFeedback validation: missing --rating or out-of-range must return exit 2.
func TestCmdFeedbackValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"missing rating", []string{"--tool", "prism_query"}, 2},
		{"rating too high", []string{"--tool", "prism_query", "--rating", "6"}, 2},
		{"rating negative", []string{"--tool", "prism_query", "--rating", "-1"}, 2},
		{"non-numeric rating", []string{"--tool", "prism_query", "--rating", "abc"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// cmdFeedback must return 2 (usage error) without reaching the
			// network — Grove is not running in unit test context.
			got := cmdFeedback(tc.args)
			if got != tc.want {
				t.Errorf("cmdFeedback(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
