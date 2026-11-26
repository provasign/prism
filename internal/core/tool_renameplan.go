package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// identRe: rename targets must be bare identifiers — a path or expression
// in the newName slot produces garbage edits framed as authoritative.
var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func (h *Handler) toolRenamePlan(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	newName := stringArg(args, "newName", "")
	if query == "" || newName == "" {
		return nil, errors.New("query and newName are required")
	}
	args, query, err := h.scopePathLineQuery(ctx, args, query)
	if err != nil {
		return nil, err
	}
	if !identRe.MatchString(newName) {
		return nil, fmt.Errorf("newName must be a bare identifier, got %q "+
			"(did a directory path land in the newName position?)", newName)
	}
	r, err := h.Grove.RenamePlanScoped(ctx, query, newName, stringArg(args, "file", ""))
	if err != nil {
		if note := h.unindexedNote(ctx, leafOf(query)); note != "" {
			return nil, fmt.Errorf("%w\n%s", err, note)
		}
		return nil, err // grove already prefixes; re-wrapping tripled the message
	}
	h.Ledger.RecordCall("prism_rename_plan")
	restoreEditIndent(h.Root, r.Edits)
	restoreEditIndent(h.Root, r.Ambiguous)
	impact, ierr := h.Grove.ChangeImpactScoped(ctx, query, stringArg(args, "file", ""))
	if ierr != nil {
		return nil, ierr
	}
	collisions := h.renameCollisions(ctx, impact, newName)
	if len(collisions) > 0 && !boolArg(args, "allow_collision") {
		return map[string]any{
			"query":      r.Query,
			"newName":    r.NewName,
			"status":     "collision",
			"collisions": collisions,
			"note": fmt.Sprintf("%s is already declared where the renamed member would land (%d declaration(s) listed); "+
				"the rename would not compile or would change which method is called. Pick another name, or pass "+
				"allow_collision=true (CLI: --allow-collision) to see the plan anyway", newName, len(collisions)),
		}, nil
	}
	out := map[string]any{
		"query":      r.Query,
		"newName":    r.NewName,
		"totalSites": r.SitesTotal,
		"edits":      r.Edits,
	}
	if len(collisions) > 0 {
		out["status"] = "collision"
		out["collisions"] = collisions
		out["warning"] = newName + " is already declared where the renamed member would land; see collisions"
	}
	addIndexedCompletenessSafety(out)
	if len(r.Unresolved) > 0 {
		out["unresolved"] = r.Unresolved
		out["unresolvedNote"] = "no line edit could be derived for these " +
			"change-set sites — handle them manually; totalSites = edit sites " +
			"+ ambiguous sites + unresolved"
	}
	if len(r.Ambiguous) > 0 {
		out["ambiguous"] = r.Ambiguous
		out["ambiguousNote"] = "these lines sit in methods that also call a same-named " +
			"method on an unrelated type — verify the receiver resolves to the renamed " +
			"member before applying"
		if r.Ambiguous[0].Reason != "" {
			// Data-member plans say per edit why it is unconfirmed; the
			// method-plan explanation above would be false for them.
			out["ambiguousNote"] = "these lines name the member but their receiver could not be " +
				"typed (each edit's reason says why) — verify the receiver is the renamed " +
				"member's owner before applying"
		}
	}
	// Bucket counts: the header must agree with what the buckets hold.
	out["editCount"] = len(r.Edits)
	out["ambiguousCount"] = len(r.Ambiguous)
	out["unresolvedCount"] = len(r.Unresolved)
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	// The same safety verdict change-impact gives this symbol: rename-plan
	// said safeToClaimComplete false where change-impact said true (BUG-13).
	// A plan with unresolved sites does not cover the change set.
	h.addDegradedNote(ctx, out, impact)
	applyCompilerBackedCompleteness(out, impact)
	if len(r.Unresolved) > 0 {
		addIndexedCompletenessSafety(out)
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
		out["warning"] = "the member overrides an external contract — renaming it breaks " +
			"that contract; do not proceed"
	}
	return out, nil
}
