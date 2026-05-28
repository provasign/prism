package config

import "testing"

func TestModelContextWindowAutoDetect(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		// Empty / auto → safe 200k default
		{"", 200000},
		// Claude family (all versions) → 200k
		{"claude-sonnet-4-6", 200000},
		{"claude-opus-4-7", 200000},
		{"claude-haiku-4-5-20251001", 200000},
		{"claude-3-5-sonnet", 200000},
		{"CLAUDE-3-OPUS", 200000}, // case-insensitive
		// GPT-4 family → 128k
		{"gpt-4o", 128000},
		{"gpt-4o-2024-11-20", 128000},
		{"gpt-4-turbo", 128000},
		{"o1", 128000},
		{"o3-mini", 128000},
		// GPT-3 → 16k
		{"gpt-3.5-turbo", 16000},
		// Gemini → 1M or 128k
		{"gemini-1.5-pro", 1000000},
		{"gemini-2.0-flash", 1000000},
		{"gemini-pro", 128000},
		// Completely unknown → conservative 128k
		{"some-unknown-model-v9", 128000},
	}
	for _, tc := range cases {
		got := ModelContextWindow(tc.model)
		if got != tc.want {
			t.Errorf("ModelContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestWithModelDoesNotMutate(t *testing.T) {
	base := Default()
	base.Model = ""

	derived := base.WithModel("claude-sonnet-4-6")
	if derived.Model != "claude-sonnet-4-6" {
		t.Errorf("derived.Model = %q, want claude-sonnet-4-6", derived.Model)
	}
	if base.Model != "" {
		t.Errorf("WithModel mutated base config: base.Model = %q", base.Model)
	}
	if derived.ContextWindow() != 200000 {
		t.Errorf("derived ContextWindow = %d, want 200000", derived.ContextWindow())
	}
}

func TestWithModelEmptyIsNoOp(t *testing.T) {
	base := Default()
	base.Model = "claude-sonnet-4-6"

	// Passing empty string should return original config unchanged.
	same := base.WithModel("")
	if same != base {
		t.Error("WithModel(\"\") should return the same pointer, not a copy")
	}
}

func TestDefaultModelIsEmpty(t *testing.T) {
	cfg := Default()
	if cfg.Model != "" {
		t.Errorf("Default model should be empty (auto), got %q", cfg.Model)
	}
	// Empty model should still give a useful context window.
	if cfg.ContextWindow() != 200000 {
		t.Errorf("Default ContextWindow with empty model = %d, want 200000", cfg.ContextWindow())
	}
}
