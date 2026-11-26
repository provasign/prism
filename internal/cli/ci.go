package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/core"
	"github.com/provasign/prism/internal/trust"
)

// ciSchemaVersion versions `prism ci --format json`.
const ciSchemaVersion = 1

// ciFinding is one fact the gate reports, with its location.
type ciFinding struct {
	Kind    string `json:"kind"` // missed_site | arch
	File    string `json:"file"`
	Line    int    `json:"line"`
	Symbol  string `json:"symbol,omitempty"`
	Message string `json:"message"`
}

// ciReport is the gate's answer.
type ciReport struct {
	SchemaVersion int              `json:"schemaVersion"`
	Base          string           `json:"base"`
	Verdict       string           `json:"verdict"` // pass | fail | degraded
	FailOn        []string         `json:"failOn"`
	Findings      []ciFinding      `json:"findings"`
	Degraded      []string         `json:"degraded,omitempty"`
	Trust         string           `json:"trust"`
	Impact        []map[string]any `json:"impact"`
	Tests         map[string]any   `json:"tests,omitempty"`
	ChangedFiles  []string         `json:"changedFiles"`
}

// cmdCI is the PR/CI gate (MKT-01): one command that indexes, runs verify,
// change-impact --diff and test-plan against a base, and reports facts with
// site lists. No LLM, no style review, no risk score.
//
// Exit: 1 when a finding kind in ci_fail_on is present, 3 when the answer
// stands but compiler-backed analysis did not run, else 0.
// Settings come from the BASE revision's prism.yaml: a pull request must not
// be able to switch its own gate off.
func cmdCI(args []string) int {
	dir, dirSet := ".", false
	base := "HEAD"
	format := "text"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--base":
			v, err := flagValue("ci", args, i)
			if err != nil {
				return usageError(err)
			}
			base = v
			i++
		case "--format":
			v, err := choiceFlag("ci", args, i, "text", "github", "sarif", "json")
			if err != nil {
				return usageError(err)
			}
			format = v
			i++
		case "--json":
			format = "json"
		case "--untrusted":
			trust.SetOverride(trust.Untrusted)
		case "--trusted":
			trust.SetOverride(trust.Trusted)
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("ci", a)
			}
			if err := positionalDir("ci", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	root := mustAbs(dir)
	if err := requireDir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	failOn := ciFailOn(root, base)
	cfg, client, err := newClient(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci:", err)
		return exitInternal
	}
	defer client.Shutdown()
	rep := ciReport{SchemaVersion: ciSchemaVersion, Base: base, FailOn: failOn, Findings: []ciFinding{},
		Trust: trust.For(root).String(), Impact: []map[string]any{}}
	idx, err := client.Index(context.Background(), root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci: index:", err)
		return exitInternal
	}
	for _, r := range idx.Readiness {
		rep.Degraded = append(rep.Degraded, r.Language+": "+r.Problem+" (fix: "+r.Fix+")")
	}
	h := core.NewHandler(cfg, root, client)
	vout, err := h.Invoke("prism_verify", map[string]any{"base": base})
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("ci", err))
		if strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "unknown revision") {
			return exitUsage
		}
		return exitInternal
	}
	v := toMap(vout)
	for _, f := range anySliceCLI(v["changedFiles"]) {
		rep.ChangedFiles = append(rep.ChangedFiles, fmt.Sprint(f))
	}
	for _, ms := range anySliceCLI(v["missedSites"]) {
		m, _ := ms.(map[string]any)
		rep.Findings = append(rep.Findings, ciFinding{
			Kind: "missed_site", File: fmt.Sprint(m["file"]), Line: jsonInt(m["line"]), Symbol: fmt.Sprint(m["symbol"]),
			Message: fmt.Sprintf("%v: %v", m["becauseOf"], m["detail"]),
		})
	}
	for _, av := range anySliceCLI(v["archIntroduced"]) {
		m, _ := av.(map[string]any)
		edge, _ := m["edge"].(map[string]any)
		sites := anySliceCLI(edge["sites"])
		file, line := "", 0
		if len(sites) > 0 {
			s, _ := sites[0].(map[string]any)
			file, line = fmt.Sprint(s["fromFile"]), jsonInt(s["fromLine"])
		}
		rep.Findings = append(rep.Findings, ciFinding{
			Kind: "arch", File: file, Line: line,
			Message: fmt.Sprintf("architecture rule %v: %v -> %v (%d site(s), weakest evidence %v)", m["rule"], edge["from"], edge["to"], len(sites), m["minTier"]),
		})
	}
	if dout, derr := h.Invoke("prism_change_impact_diff", map[string]any{"base": base}); derr == nil {
		d := toMap(dout)
		for _, c := range anySliceCLI(d["changes"]) {
			if cm, ok := c.(map[string]any); ok {
				rep.Impact = append(rep.Impact, cm)
			}
		}
		if t, ok := d["tests"].(map[string]any); ok {
			rep.Tests = t
		}
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	code := exitOK
	rep.Verdict = "pass"
	for _, f := range rep.Findings {
		if ciHas(failOn, f.Kind) {
			rep.Verdict, code = "fail", exitFinding
			break
		}
	}
	if code == exitOK && len(rep.Degraded) > 0 {
		rep.Verdict, code = "degraded", exitDegraded
	}
	switch format {
	case "json":
		printJSON(rep)
	case "sarif":
		printJSON(ciSARIF(rep))
	case "github":
		ciGitHub(rep)
	default:
		fmt.Print(ciText(rep))
	}
	return code
}

func ciHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ciFailOn reads ci_fail_on from the base revision's prism.yaml, falling
// back to both kinds. Never from the working tree: the change under review
// must not set its own gate.
func ciFailOn(root, base string) []string {
	def := []string{"missed_site", "arch"}
	if !validGitBaseCLI(base) {
		return def
	}
	out, err := exec.Command("git", "-C", root, "show", "--end-of-options", base+":prism.yaml").Output()
	if err != nil {
		return def
	}
	c := config.Parse(out)
	if c.CIFailOn == nil {
		return def
	}
	norm := make([]string, 0, len(c.CIFailOn))
	for _, k := range c.CIFailOn {
		switch k {
		case "missed_sites", "missed_site":
			norm = append(norm, "missed_site")
		case "arch":
			norm = append(norm, "arch")
		}
	}
	return norm
}

func validGitBaseCLI(base string) bool {
	if base == "" || strings.HasPrefix(base, "-") {
		return false
	}
	for _, r := range base {
		if r <= ' ' || r == ':' {
			return false
		}
	}
	return true
}

func ciText(r ciReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "prism ci vs %s: %s (%d finding(s); fails on %s; trust %s)\n", r.Base, r.Verdict, len(r.Findings), strings.Join(r.FailOn, ", "), r.Trust)
	for _, d := range r.Degraded {
		fmt.Fprintf(&b, "degraded: %s\n", d)
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s:%d: %s: %s\n", f.File, f.Line, f.Kind, f.Message)
	}
	if len(r.Impact) > 0 {
		fmt.Fprintf(&b, "impact: %d changed symbol(s); prism change-impact --diff --base %s lists their sites\n", len(r.Impact), r.Base)
	}
	if t := r.Tests; t != nil {
		fmt.Fprintf(&b, "tests: run first %d file(s), %v transitive, %v for safety; prism test-plan --base %s lists them\n",
			len(anySliceCLI(t["runFirst"])), t["transitive"], t["safetyFiles"], r.Base)
	}
	return b.String()
}

// ciGitHub writes workflow annotations to stdout and a Markdown summary to
// $GITHUB_STEP_SUMMARY (stdout when unset).
func ciGitHub(r ciReport) {
	for _, f := range r.Findings {
		level := "warning"
		if ciHas(r.FailOn, f.Kind) {
			level = "error"
		}
		fmt.Printf("::%s file=%s,line=%d,title=prism %s::%s\n", level, ghEscape(f.File), f.Line, f.Kind, ghEscape(f.Message))
	}
	var md strings.Builder
	fmt.Fprintf(&md, "## prism ci: %s\n\n", r.Verdict)
	fmt.Fprintf(&md, "Base `%s`, %d changed file(s), trust %s. Fails on: %s.\n\n", r.Base, len(r.ChangedFiles), r.Trust, strings.Join(r.FailOn, ", "))
	if len(r.Degraded) > 0 {
		md.WriteString("**Compiler-backed analysis did not run** for:\n\n")
		for _, d := range r.Degraded {
			fmt.Fprintf(&md, "- %s\n", d)
		}
		md.WriteString("\n")
	}
	if len(r.Findings) == 0 {
		md.WriteString("No missed sites and no new architecture violations.\n")
	} else {
		md.WriteString("| Kind | Site | Finding |\n|---|---|---|\n")
		for _, f := range r.Findings {
			fmt.Fprintf(&md, "| %s | `%s:%d` | %s |\n", f.Kind, f.File, f.Line, strings.ReplaceAll(f.Message, "|", "\\|"))
		}
	}
	if len(r.Impact) > 0 {
		fmt.Fprintf(&md, "\n<details><summary>Impact of %d changed symbol(s)</summary>\n\n", len(r.Impact))
		for _, c := range r.Impact {
			fmt.Fprintf(&md, "- `%v` (%v): %v\n", c["symbol"], c["file"], c["reason"])
		}
		md.WriteString("\n</details>\n")
	}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(md.String())
			_ = f.Close()
			return
		}
	}
	fmt.Print(md.String())
}

func ghEscape(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// ciSARIF renders the findings as SARIF 2.1.0 for code scanning: one rule
// per finding kind.
func ciSARIF(r ciReport) map[string]any {
	rules := []map[string]any{
		{"id": "missed_site", "shortDescription": map[string]any{"text": "A site the change requires was not edited"}},
		{"id": "arch", "shortDescription": map[string]any{"text": "The change adds a dependency an architecture rule denies"}},
	}
	results := make([]map[string]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		level := "warning"
		if ciHas(r.FailOn, f.Kind) {
			level = "error"
		}
		line := f.Line
		if line < 1 {
			line = 1
		}
		results = append(results, map[string]any{
			"ruleId": f.Kind, "level": level, "message": map[string]any{"text": f.Message},
			"locations": []map[string]any{{"physicalLocation": map[string]any{
				"artifactLocation": map[string]any{"uri": f.File},
				"region":           map[string]any{"startLine": line},
			}}},
		})
	}
	return map[string]any{
		"version": "2.1.0",
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"runs": []map[string]any{{
			"tool":    map[string]any{"driver": map[string]any{"name": "prism", "informationUri": "https://github.com/provasign/prism", "rules": rules}},
			"results": results,
		}},
	}
}
