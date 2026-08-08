package cli

import (
	"strings"
	"testing"
)

// prism init used to WriteFile the template over prism.yaml unconditionally,
// deleting every key it does not manage. arch_deny is the worst loss: those
// rules ARE the `prism arch` CI gate, so a re-init silently turned the gate
// into a no-op while leaving the workflow green. (Measured: it deleted this
// repo's five rules during a routine steering regeneration.)
func TestMergePrismYAMLPreservesUserContent(t *testing.T) {
	existing := `version: 1
# model: auto  # Prism detects the active model.
profile: "default"
agent_mode: "mcp"

# Declared architecture — enforced by ` + "`prism arch`" + ` (exit 1 on violation).
arch_deny: internal/grove -> internal/*    # engine wrapper is a leaf
arch_deny: internal/* -> internal/cli      # nothing imports the CLI
`
	got := mergePrismYAML(existing, "default")

	for _, want := range []string{
		"arch_deny: internal/grove -> internal/*    # engine wrapper is a leaf",
		"arch_deny: internal/* -> internal/cli      # nothing imports the CLI",
		"# model: auto  # Prism detects the active model.",
		"# Declared architecture",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lost user content %q:\n%s", want, got)
		}
	}
	// agent_mode stopped being a managed key in v0.38.0. An existing one is
	// now USER content: preserved untouched, never rewritten, never added.
	if !strings.Contains(got, `agent_mode: "mcp"`) {
		t.Errorf("user's agent_mode line was clobbered:\n%s", got)
	}
	// Exactly one of each managed key — no duplicates appended.
	for _, k := range []string{"version:", "profile:"} {
		if n := strings.Count(got, "\n"+k) + boolToInt(strings.HasPrefix(got, k)); n != 1 {
			t.Errorf("%s appears %d times, want 1:\n%s", k, n, got)
		}
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A file missing a managed key gets it appended, without disturbing the rest.
func TestMergePrismYAMLAppendsMissingKeys(t *testing.T) {
	got := mergePrismYAML("arch_deny: a -> b\n", "fast")
	for _, want := range []string{"version: 1", `profile: "fast"`, "arch_deny: a -> b"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// agent_mode is no longer a managed key and must not be reintroduced.
	if strings.Contains(got, "agent_mode") {
		t.Errorf("agent_mode reappeared:\n%s", got)
	}
}

// Idempotence: merging its own output must not drift.
func TestMergePrismYAMLIsIdempotent(t *testing.T) {
	in := "version: 1\nprofile: \"default\"\nagent_mode: \"both\"\narch_deny: a -> b\n"
	once := mergePrismYAML(in, "default")
	twice := mergePrismYAML(once, "default")
	if once != twice {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

// An indented key of the same name belongs to a nested block and must not be
// rewritten as a top-level setting.
func TestMergePrismYAMLIgnoresNestedKeys(t *testing.T) {
	in := "version: 1\nsomething:\n  profile: \"nested\"\n"
	got := mergePrismYAML(in, "default")
	if !strings.Contains(got, `  profile: "nested"`) {
		t.Errorf("nested key was rewritten:\n%s", got)
	}
}
