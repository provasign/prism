package mcp

// prism — the unified task tool (task compiler). One tool, two moments:
//
//   prism(task="...")                        prepare: anchors -> edit-ready
//                                            source -> change obligations ->
//                                            tests/coverage gaps; obligations
//                                            persisted for the verify moment.
//   prism(task="...", changed_files=[...])   verify: the shipped prism_verify
//                                            pipeline + the stored obligations
//                                            checked against the diff.
//
// Design: docs/DESIGN_TASK_COMPILER.md. The agent describes its task; Prism
// selects the internal operators. Existing tools remain the advanced surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// obligationSite is one site in a recorded obligation's required set.
type obligationSite struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
}

// taskObligation is one anticipated change obligation, recorded at prepare
// time. Completeness is an evidence tier ("closed", "project-local",
// "callers-only"), never an editorial class — see design doc correction 3.
type taskObligation struct {
	Symbol        string           `json:"symbol"`
	QualifiedName string           `json:"qualifiedName"`
	File          string           `json:"file"`
	Line          int              `json:"line"`
	Kind          string           `json:"kind"`
	Completeness  string           `json:"completeness"`
	SiteCount     int              `json:"siteCount"`
	Sites         []obligationSite `json:"sites"`
}

// taskPackage is what prepare persists and verify loads. Stored in the
// .grove state dir so a later process (CLI verify, a hook, CI) can compare.
type taskPackage struct {
	Task        string           `json:"task"`
	Base        string           `json:"base"` // HEAD at prepare time
	Obligations []taskObligation `json:"obligations"`
}

// maxObligationAnchors bounds how many anchor symbols get a full impact
// computation at prepare time; maxObligationSites bounds recorded sites per
// obligation (the count is always exact even when sites are truncated).
const (
	maxObligationAnchors = 5
	// maxObligationSites must comfortably exceed real blast radii: the
	// benchmark corpus tops out at 310 required sites, and a truncated
	// obligation list silently caps an agent's achievable recall (measured:
	// a 60-site cap held Haiku to 0.65 recall on a 108-site change that the
	// direct change_impact arm completed at 0.98).
	maxObligationSites   = 500
)

func (h *Handler) taskPackagePath() string {
	return filepath.Join(h.Root, ".grove", "task-package.json")
}

func (h *Handler) toolTask(ctx context.Context, args map[string]any) (any, error) {
	task := stringArg(args, "task", "")
	if task == "" {
		return nil, errors.New("task is required — describe what you are trying to do")
	}
	changed := stringListArg(args, "changed_files")
	mode := stringArg(args, "mode", "")
	if mode == "" {
		if len(changed) > 0 {
			mode = "verify"
		} else {
			mode = "prepare"
		}
	}
	switch mode {
	case "prepare":
		return h.taskPrepare(ctx, task, args)
	case "verify":
		return h.taskVerify(ctx, task, changed, args)
	default:
		return nil, fmt.Errorf("mode must be \"prepare\" or \"verify\", got %q", mode)
	}
}

func (h *Handler) taskPrepare(ctx context.Context, task string, args map[string]any) (any, error) {
	var terms []string
	if raw, ok := args["terms"]; ok {
		terms = anyToStrings(raw)
	}
	sel, err := h.selectContext(ctx, selectParams{
		task:        task,
		terms:       terms,
		includeSet:  map[string]bool{"graph": true, "tests": true},
		limit:       intArg(args, "limit", 50),
		contextUsed: int64(intArg(args, "context_used", 0)),
		model:       stringArg(args, "model", ""),
		budgetArg:   intArg(args, "budget", 0),
	})
	if err != nil {
		return nil, err
	}

	// Edit-ready delivery: verbatim line-numbered source windows + per-anchor
	// callers and covering tests — the existing prism_query source path.
	read := h.deliverSource(ctx, task, sel, intArg(args, "max_files", 0), sel.budget)

	// Change obligations: for each anchor with a call-shaped contract,
	// compute the required set now, so the agent edits with the blast radius
	// in hand and verify can hold the diff against it later.
	var obligations []taskObligation
	var obligationNotes []string
	seen := map[string]bool{}
	for _, sym := range sel.seedSyms {
		if len(obligations) >= maxObligationAnchors {
			obligationNotes = append(obligationNotes,
				fmt.Sprintf("obligations computed for the top %d anchors only; call prism_change_impact directly for others", maxObligationAnchors))
			break
		}
		switch sym.Kind {
		case "function", "method", "constructor":
		default:
			continue
		}
		key := sym.FilePath + ":" + sym.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		impact, err := h.changeImpactFor(ctx, sym)
		if err != nil {
			obligationNotes = append(obligationNotes,
				displayQN(sym)+": impact not computable ("+err.Error()+")")
			continue
		}
		ob := taskObligation{
			Symbol:        sym.Name,
			QualifiedName: displayQN(sym),
			File:          sym.FilePath,
			Line:          sym.Span.Start,
			Kind:          sym.Kind,
			Completeness:  impact.Completeness,
		}
		if ob.Completeness == "" {
			ob.Completeness = "closed"
		}
		required := make([]grove.SymbolRecord, 0,
			len(impact.Family)+len(impact.Callers)+len(impact.DeclaringTypes))
		required = append(required, impact.Family...)
		required = append(required, impact.Callers...)
		required = append(required, impact.DeclaringTypes...)
		for _, site := range required {
			if site.FilePath == sym.FilePath && site.Span.Start == sym.Span.Start {
				continue // the anchor itself
			}
			ob.SiteCount++
			if len(ob.Sites) < maxObligationSites {
				ob.Sites = append(ob.Sites, obligationSite{
					Symbol: displayQN(site), File: site.FilePath,
					Line: site.Span.Start, Kind: site.Kind,
				})
			}
		}
		obligations = append(obligations, ob)
	}

	gaps := buildCoverageGaps(ctx, h.Grove, sel.seedSyms, sel.graphExtra)

	// Persist the package for the verify moment (best-effort: a read-only
	// checkout must not fail the prepare call).
	pkg := taskPackage{Task: task, Base: gitHead(h.Root), Obligations: obligations}
	if data, err := json.MarshalIndent(pkg, "", " "); err == nil {
		if err := os.MkdirAll(filepath.Dir(h.taskPackagePath()), 0o755); err == nil {
			_ = os.WriteFile(h.taskPackagePath(), data, 0o644)
		}
	}

	h.Ledger.RecordCall("prism")
	out := map[string]any{
		"mode":        "prepare",
		"task":        task,
		"read":        read,
		"obligations": obligations,
		"next": "make the edits, then call prism again with the same task and " +
			"changed_files=[...] to verify the change is complete",
	}
	if len(gaps) > 0 {
		out["coverageGaps"] = gaps
	}
	if len(obligationNotes) > 0 {
		out["notes"] = obligationNotes
	}
	if len(obligations) == 0 && len(sel.seedSyms) > 0 {
		out["obligationsNote"] = "no call-shaped anchors in the top selection; " +
			"if you end up changing a signature, verify will compute the impact from the diff"
	}
	return out, nil
}

func (h *Handler) taskVerify(ctx context.Context, task string, changed []string, args map[string]any) (any, error) {
	base := stringArg(args, "base", "HEAD")
	out, err := h.toolVerify(ctx, map[string]any{"base": base})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return out, nil
	}
	m["mode"] = "verify"
	m["task"] = task

	// Cross-check the claimed changed_files against the authoritative diff.
	changedRanges, rerr := gitChangedRanges(h.Root, base)
	if rerr == nil && len(changed) > 0 {
		var notInDiff []string
		for _, f := range changed {
			if _, ok := changedRanges[filepath.ToSlash(f)]; !ok {
				notInDiff = append(notInDiff, f)
			}
		}
		if len(notInDiff) > 0 {
			m["changedFilesNote"] = fmt.Sprintf(
				"claimed as changed but not in the diff vs %s: %s (the git diff is authoritative)",
				base, strings.Join(notInDiff, ", "))
		}
	}

	// Stored obligations from the prepare moment: anticipated sites the diff
	// did not touch are reported with a caveat, never as accusations — the
	// implementation may legitimately have left that contract unchanged
	// (design doc correction 4). The verdict stays diff-driven.
	pkg := h.loadTaskPackage()
	if pkg != nil && rerr == nil {
		if pkg.Task != task {
			m["obligationsNote"] = "stored obligations were recorded for a different task (" +
				truncate(pkg.Task, 80) + "); skipping obligation comparison"
		} else {
			satisfied := 0
			var unaddressed []obligationSite
			for _, ob := range pkg.Obligations {
				for _, s := range ob.Sites {
					// File-level granularity: the recorded line is the
					// symbol's span start, which an edit inside the body
					// will not hit exactly. Anticipated obligations get the
					// benefit of the doubt; line-precise accusation is the
					// diff-driven pipeline's job.
					if spanFileTouched(changedRanges, s.File) {
						satisfied++
					} else {
						unaddressed = append(unaddressed, s)
					}
				}
			}
			m["obligationsSatisfied"] = satisfied
			if len(unaddressed) > 0 {
				m["unaddressedObligations"] = unaddressed
				m["unaddressedCaveat"] = "anticipated at prepare time but untouched by the diff — " +
					"fine if the chosen implementation did not change that contract; " +
					"review any that correspond to a contract you DID change"
			}
		}
	}
	h.Ledger.RecordCall("prism")
	return m, nil
}

// spanFileTouched reports whether any hunk touched the file at all — the
// obligation site line recorded at prepare time is the symbol's span start,
// which an edit inside the symbol body will not hit exactly.
func spanFileTouched(changed map[string][]lineRange, file string) bool {
	return len(changed[file]) > 0
}

func (h *Handler) loadTaskPackage() *taskPackage {
	data, err := os.ReadFile(h.taskPackagePath())
	if err != nil {
		return nil
	}
	var pkg taskPackage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	return &pkg
}

func gitHead(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func stringListArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	return anyToStrings(raw)
}

func anyToStrings(raw any) []string {
	switch v := raw.(type) {
	case []any:
		var out []string
		for _, t := range v {
			if s, ok := t.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
