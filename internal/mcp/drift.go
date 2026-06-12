// prism_drift — the delivery half of the stale-context loop.
//
// Fuse records what each merge changed (.git/fuse/drift.json, evidence
// half); Prism's session tracker knows what this agent has actually been
// shown. prism_drift intersects the two worlds: it re-checks every tracked
// file against the working tree and reports, symbol by symbol, where the
// ground shifted under the agent — with merge provenance when a Fuse merge
// caused it. Context-delivering tools also append a one-line warning as
// soon as any tracked file goes stale, so the agent learns mid-task, not
// at merge time.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
)

// maxDriftFiles bounds a full drift check to the hot working set.
const maxDriftFiles = 500

// maxWarningFiles bounds the cheap per-call staleness probe.
const maxWarningFiles = 50

// DriftSymbol is one symbol-level change under the agent's feet.
type DriftSymbol struct {
	Name         string `json:"name"`
	Change       string `json:"change"` // added | removed | changed | renamed
	NewSignature string `json:"newSignature,omitempty"`
	// RenamedTo carries the new name when Change is "renamed" (Grove pairs a
	// removed symbol with an added one whose body matches modulo its name).
	RenamedTo string `json:"renamedTo,omitempty"`
	// Breaking marks exported symbols that were removed, renamed, or
	// re-signatured — the changes that break callers of the old contract.
	Breaking bool `json:"breaking,omitempty"`
}

// FileDrift reports one tracked file that no longer matches what was
// delivered.
type FileDrift struct {
	File   string `json:"file"`
	Status string `json:"status"` // changed | deleted
	// Origin is "merge" when a recorded Fuse merge touched this file,
	// otherwise "edit" (any other writer: another agent, the user, a tool).
	Origin  string        `json:"origin"`
	Symbols []DriftSymbol `json:"symbols,omitempty"`
	// MergeDetails carries Fuse's symbol-precise drift record entries for
	// this file (old → new signatures, renames), when available.
	MergeDetails []FuseDriftSymbol `json:"mergeDetails,omitempty"`
}

// DriftReport is the prism_drift response.
type DriftReport struct {
	CheckedFiles int         `json:"checkedFiles"`
	ChangedFiles int         `json:"changedFiles"`
	Warning      string      `json:"warning,omitempty"`
	Files        []FileDrift `json:"files,omitempty"`
}

// toolDrift implements prism_drift: refresh the index (delta — cheap),
// then compare every tracked file against what was delivered.
func (h *Handler) toolDrift(ctx context.Context, _ map[string]any) (any, error) {
	if h.Grove != nil {
		if _, err := h.Grove.Index(ctx, h.Root); err != nil {
			// Degrade to file-level detection: hash comparison needs no index.
			fmt.Fprintf(os.Stderr, "prism_drift: index refresh failed (%v); symbol detail degraded\n", err)
		}
	}
	entries := h.Session.RecentEntries(maxDriftFiles)
	fuseByFile := loadFuseDrift(h.Root)

	report := DriftReport{CheckedFiles: len(entries)}
	for _, entry := range entries {
		drift, stale := h.fileDrift(ctx, entry, fuseByFile)
		if !stale {
			continue
		}
		report.ChangedFiles++
		report.Files = append(report.Files, drift)
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].File < report.Files[j].File })
	if report.ChangedFiles > 0 {
		report.Warning = staleWarningLine(report.Files)
	}
	return report, nil
}

// fileDrift checks one tracked file. stale=false means it still matches
// what was delivered.
func (h *Handler) fileDrift(ctx context.Context, entry session.Entry, fuseByFile map[string][]FuseDriftSymbol) (FileDrift, bool) {
	abs := filepath.Join(h.Root, filepath.FromSlash(entry.FilePath))
	content, err := os.ReadFile(abs)
	if err != nil {
		return FileDrift{
			File:         entry.FilePath,
			Status:       "deleted",
			Origin:       driftOrigin(entry.FilePath, fuseByFile),
			MergeDetails: fuseByFile[entry.FilePath],
		}, true
	}
	if compression.Hash(string(content)) == entry.ContentHash {
		return FileDrift{}, false
	}

	drift := FileDrift{
		File:         entry.FilePath,
		Status:       "changed",
		Origin:       driftOrigin(entry.FilePath, fuseByFile),
		MergeDetails: fuseByFile[entry.FilePath],
	}
	if h.Grove == nil {
		return drift, true
	}

	// Preferred path: structural diff via Grove's GraphDiff against the
	// symbols delivered this session — pairs renames and classifies
	// breaking changes instead of reporting remove+add churn.
	if base := h.driftBaseFor(entry.FilePath); len(base) > 0 {
		if d, err := h.Grove.DiffFile(ctx, base, entry.FilePath); err == nil {
			drift.Symbols = driftSymbolsFromGraphDiff(d)
			return drift, true
		}
	}

	// Fallback (warm cache from a previous session, or query-only files):
	// compare the persisted per-symbol SHAs. No rename pairing here — only
	// the structural path has the body text needed to match renames.
	if len(entry.SymbolSHAs) == 0 {
		return drift, true
	}
	current, err := h.Grove.FileSymbols(ctx, entry.FilePath)
	if err != nil {
		return drift, true
	}
	currentByKey := map[string]struct {
		sha string
		sig string
	}{}
	for _, sym := range current {
		body := sym.RawText
		if body == "" {
			body = sym.BlobSha
		}
		currentByKey[compression.SymbolKey(sym)] = struct {
			sha string
			sig string
		}{sha: compression.Hash(body), sig: sym.Signature}
	}
	for key, deliveredSHA := range entry.SymbolSHAs {
		cur, ok := currentByKey[key]
		if !ok {
			drift.Symbols = append(drift.Symbols, DriftSymbol{Name: key, Change: "removed"})
			continue
		}
		if cur.sha != deliveredSHA {
			drift.Symbols = append(drift.Symbols, DriftSymbol{Name: key, Change: "changed", NewSignature: cur.sig})
		}
	}
	for key := range currentByKey {
		if _, delivered := entry.SymbolSHAs[key]; !delivered {
			cur := currentByKey[key]
			drift.Symbols = append(drift.Symbols, DriftSymbol{Name: key, Change: "added", NewSignature: cur.sig})
		}
	}
	sortDriftSymbols(drift.Symbols)
	return drift, true
}

// driftSymbolsFromGraphDiff flattens Grove's GraphDiff into the drift report
// shape. Renamed pairs surface once (old name → new name), and members of
// BreakingChanges carry the breaking flag.
func driftSymbolsFromGraphDiff(d *grove.FileGraphDiff) []DriftSymbol {
	breaking := map[string]bool{}
	for _, c := range d.Breaking {
		if c.Before != nil {
			breaking[c.Before.QualifiedName] = true
		} else if c.After != nil {
			breaking[c.After.QualifiedName] = true
		}
	}
	isBreaking := func(before, after *grove.SymbolRecord) bool {
		if before != nil && breaking[before.QualifiedName] {
			return true
		}
		return after != nil && breaking[after.QualifiedName]
	}

	var out []DriftSymbol
	for _, s := range d.Added {
		out = append(out, DriftSymbol{
			Name: s.QualifiedName, Change: "added", NewSignature: s.Signature,
			Breaking: breaking[s.QualifiedName],
		})
	}
	for _, s := range d.Removed {
		out = append(out, DriftSymbol{
			Name: s.QualifiedName, Change: "removed",
			Breaking: breaking[s.QualifiedName],
		})
	}
	for _, c := range d.Changed {
		ds := DriftSymbol{Change: "changed", Breaking: isBreaking(c.Before, c.After)}
		if c.Before != nil {
			ds.Name = c.Before.QualifiedName
		}
		if c.After != nil {
			if ds.Name == "" {
				ds.Name = c.After.QualifiedName
			}
			ds.NewSignature = c.After.Signature
		}
		out = append(out, ds)
	}
	for _, c := range d.Renamed {
		ds := DriftSymbol{Change: "renamed", Breaking: isBreaking(c.Before, c.After)}
		if c.Before != nil {
			ds.Name = c.Before.QualifiedName
		}
		if c.After != nil {
			ds.RenamedTo = c.After.QualifiedName
			ds.NewSignature = c.After.Signature
		}
		out = append(out, ds)
	}
	sortDriftSymbols(out)
	return out
}

func sortDriftSymbols(syms []DriftSymbol) {
	sort.Slice(syms, func(i, j int) bool {
		if syms[i].Change == syms[j].Change {
			return syms[i].Name < syms[j].Name
		}
		return syms[i].Change < syms[j].Change
	})
}

// StaleContextWarning is the cheap per-call probe: hash-compare the most
// recently delivered files and return a one-line warning when any changed.
// Empty string means the working set is fresh. No index refresh, no graph
// access — just N small file reads.
func (h *Handler) StaleContextWarning() string {
	if h.Session == nil {
		return ""
	}
	var changed []string
	for _, entry := range h.Session.RecentEntries(maxWarningFiles) {
		abs := filepath.Join(h.Root, filepath.FromSlash(entry.FilePath))
		content, err := os.ReadFile(abs)
		if err != nil || compression.Hash(string(content)) != entry.ContentHash {
			changed = append(changed, entry.FilePath)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	sort.Strings(changed)
	preview := changed
	if len(preview) > 5 {
		preview = preview[:5]
	}
	return fmt.Sprintf("⚠ stale context: %d file(s) you received have changed since delivery (%s). Call prism_drift for symbol-level details before relying on them.",
		len(changed), strings.Join(preview, ", "))
}

func staleWarningLine(files []FileDrift) string {
	parts := make([]string, 0, len(files))
	for _, f := range files {
		detail := f.Status
		if n := len(f.Symbols); n > 0 {
			detail = fmt.Sprintf("%d symbol(s) %s", n, f.Status)
		}
		if f.Origin == "merge" {
			detail += " via merge"
		}
		parts = append(parts, f.File+" ("+detail+")")
		if len(parts) == 5 {
			break
		}
	}
	return "the ground shifted: " + strings.Join(parts, ", ")
}

// ─── Fuse drift.json provenance ──────────────────────────────────────────────

// FuseDriftSymbol mirrors fuse's drift record symbol entries (subset).
type FuseDriftSymbol struct {
	QualifiedName string `json:"qualifiedName"`
	Change        string `json:"change"`
	OldSignature  string `json:"oldSignature,omitempty"`
	NewSignature  string `json:"newSignature,omitempty"`
	RenamedFrom   string `json:"renamedFrom,omitempty"`
}

type fuseDriftRecord struct {
	File  string `json:"file"`
	Drift struct {
		Added    []FuseDriftSymbol `json:"added"`
		Removed  []FuseDriftSymbol `json:"removed"`
		Changed  []FuseDriftSymbol `json:"changed"`
		Renamed  []FuseDriftSymbol `json:"renamed"`
		Breaking []FuseDriftSymbol `json:"breaking"`
	} `json:"drift"`
}

// loadFuseDrift reads .git/fuse/drift.json (advisory; absent is normal) and
// indexes its symbol entries by file.
func loadFuseDrift(root string) map[string][]FuseDriftSymbol {
	gitDir := resolveGitDir(root)
	if gitDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "fuse", "drift.json"))
	if err != nil {
		return nil
	}
	var records []fuseDriftRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil
	}
	out := map[string][]FuseDriftSymbol{}
	for _, rec := range records {
		file := filepath.ToSlash(rec.File)
		out[file] = append(out[file], rec.Drift.Changed...)
		out[file] = append(out[file], rec.Drift.Renamed...)
		out[file] = append(out[file], rec.Drift.Added...)
		out[file] = append(out[file], rec.Drift.Removed...)
	}
	return out
}

func driftOrigin(file string, fuseByFile map[string][]FuseDriftSymbol) string {
	if len(fuseByFile[file]) > 0 {
		return "merge"
	}
	return "edit"
}

// resolveGitDir returns root/.git, following a worktree "gitdir:" pointer.
func resolveGitDir(root string) string {
	candidate := filepath.Join(root, ".git")
	info, err := os.Stat(candidate)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return candidate
	}
	data, err := os.ReadFile(candidate)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if rest, ok := strings.CutPrefix(line, "gitdir:"); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}
