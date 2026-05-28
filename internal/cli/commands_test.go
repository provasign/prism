package cli

import (
	"testing"
)

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
