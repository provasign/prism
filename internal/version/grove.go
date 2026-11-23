package version

import (
	"os"
	"runtime/debug"
	"strings"
)

// GroveModule is the Grove module path.
const GroveModule = "github.com/provasign/grove"

// Grove describes the Grove engine linked into this binary.
type Grove struct {
	Version  string // the module version linked (or the pinned one, when replaced)
	Replaced bool   // a replace directive or go.work points it elsewhere
	Path     string // where it was replaced to, when Replaced
}

// LinkedGrove reads the Grove module from the binary's build info. ok is
// false when there is no build info (a test binary may lack it).
func LinkedGrove() (Grove, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Grove{}, false
	}
	for _, m := range info.Deps {
		if m.Path != GroveModule {
			continue
		}
		g := Grove{Version: m.Version}
		if m.Version == "(devel)" {
			// A go.work workspace links the module from a directory and
			// records no replace entry.
			g.Replaced, g.Path = true, "a go.work workspace"
		}
		if m.Replace != nil {
			g.Replaced = true
			g.Path = m.Replace.Path
			if m.Replace.Version != "" {
				g.Path += "@" + m.Replace.Version
			}
		}
		return g, true
	}
	return Grove{}, false
}

// String is "v0.63.27" or "v0.63.27 (replaced by ../grove)".
func (g Grove) String() string {
	if g.Replaced {
		return g.Version + " (replaced by " + g.Path + ")"
	}
	return g.Version
}

// LocalGroveAllowed reports whether PRISM_ALLOW_LOCAL_GROVE=1 permits a
// replaced Grove (development against a sibling checkout).
func LocalGroveAllowed() bool {
	v := strings.TrimSpace(os.Getenv("PRISM_ALLOW_LOCAL_GROVE"))
	return v == "1" || strings.EqualFold(v, "true")
}
