package grove

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"testing"

	"github.com/provasign/prism/internal/version"
)

// REL-02: tests must run against the Grove go.mod pins. A go.work in a
// parent directory silently swapped in a sibling checkout, so a local test
// run and a release build linked different engines. Test binaries carry no
// dependency build info, so this asks the go command which Grove the
// current environment resolves.
func TestLinkedGroveMatchesPin(t *testing.T) {
	if version.LocalGroveAllowed() {
		t.Skip("PRISM_ALLOW_LOCAL_GROVE=1: a local Grove is allowed")
	}
	out, err := exec.Command("go", "list", "-m", "-json", version.GroveModule).Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	var m struct {
		Version string
		Main    bool
		Dir     string
		Replace *struct{ Path, Version string }
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Main || m.Replace != nil {
		t.Fatalf("Grove resolves to %s (workspace or replace), not the go.mod pin; run with GOWORK=off, or set PRISM_ALLOW_LOCAL_GROVE=1 to develop against it", m.Dir)
	}
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(version.GroveModule) + `\s+(\S+)`).FindSubmatch(gomod)
	if pin == nil {
		t.Fatal("go.mod does not require Grove")
	}
	if m.Version != string(pin[1]) {
		t.Fatalf("Grove resolves to %s, go.mod pins %s", m.Version, pin[1])
	}
}
