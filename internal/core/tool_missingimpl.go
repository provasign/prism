package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

func (h *Handler) toolMissingImplementations(ctx context.Context, args map[string]any) (any, error) {
	query := stringArg(args, "query", "")
	if query == "" {
		return nil, errors.New("query is required")
	}
	args, query, err := h.scopePathLineQuery(ctx, args, query)
	if err != nil {
		return nil, err
	}
	r, err := h.Grove.MissingImplementationsScoped(ctx, query, stringArg(args, "file", ""))
	if err != nil {
		if ferr := h.fileNotSymbolError(query); ferr != nil {
			return nil, ferr
		}
		if strings.Contains(err.Error(), "declares no member") {
			if planned, ok := h.plannedMemberImplementations(ctx, query, stringArg(args, "file", ""), stringArg(args, "signature", "")); ok {
				h.Ledger.RecordCall("prism_missing_implementations")
				return planned, nil
			}
		}
	}
	if err != nil {
		if note := h.unindexedNote(ctx, leafOf(query)); note != "" {
			return nil, fmt.Errorf("%w\n%s", err, note)
		}
		return nil, err // grove already prefixes; re-wrapping tripled the message
	}
	h.Ledger.RecordCall("prism_missing_implementations")
	compact := func(syms []grove.SymbolRecord) []map[string]any {
		out := make([]map[string]any, 0, len(syms))
		for _, s := range syms {
			qn := s.QualifiedName
			if qn == "" {
				qn = s.Name
			}
			out = append(out, map[string]any{
				"name":          s.Name,
				"qualifiedName": qn,
				"filePath":      s.FilePath,
				"line":          s.Span.Start,
				"kind":          s.Kind,
				"signature":     s.Signature,
			})
		}
		return out
	}
	out := map[string]any{
		"query":            r.Query,
		"contract":         compact(r.Contract),
		"missing":          compact(r.Missing),
		"implementedCount": r.ImplementedCount,
	}
	addIndexedCompletenessSafety(out)
	if len(r.AbstractMissing) > 0 {
		out["abstractMissing"] = compact(r.AbstractMissing)
	}
	if len(r.Unverifiable) > 0 {
		out["unverifiable"] = compact(r.Unverifiable)
		out["unverifiableNote"] = "these types have no visible implementation but their " +
			"superclass chain leaves the index — an external base class may provide the " +
			"member; verify before treating them as broken"
	}
	if r.DefaultProvided {
		out["defaultProvided"] = true
		out["note"] = "the contract supplies a body every subtype inherits, so nothing is " +
			"compile-broken today — 'missing' lists the types that inherit the default and " +
			"would break if the member became abstract/required"
	}
	if r.Completeness != "" {
		out["completeness"] = r.Completeness
	}
	if len(r.ExternalSupers) > 0 {
		out["externalSupers"] = r.ExternalSupers
	}
	if len(r.OverridesExternal) > 0 {
		out["overridesExternal"] = r.OverridesExternal
	}
	if former := h.formerImplementers(ctx, r); len(former) > 0 {
		out["formerlyImplementing"] = former
		out["formerlyImplementingNote"] = "these types have every other member of the interface, so they implemented it before this member was added; nothing uses them as the interface, so the compiler does not require them to change. Add the member if they are meant to implement it (matched by method names)"
	}
	return out, nil
}
