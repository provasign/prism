package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// lookupResultSymbol extracts the delivered primary symbol, if any.
func lookupResultSymbol(out map[string]any) (grove.SymbolRecord, bool) {
	switch sym := out["symbol"].(type) {
	case grove.SymbolRecord:
		return sym, sym.ID != ""
	case *grove.SymbolRecord:
		if sym != nil {
			return *sym, sym.ID != ""
		}
	}
	return grove.SymbolRecord{}, false
}

// bareParamToken reduces a parameter type to the comparable token grove
// uses: generics stripped, last dotted segment, arrays kept.
func bareParamToken(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i > 0 {
		if j := strings.LastIndexByte(t, '>'); j > i {
			t = t[:i] + t[j+1:]
		}
	}
	t = strings.ReplaceAll(t, "...", "[]")
	t = strings.TrimLeft(t, "*&")
	arr := strings.HasSuffix(t, "[]")
	t = strings.TrimSuffix(t, "[]")
	if j := strings.LastIndexAny(t, ".:"); j >= 0 {
		t = t[j+1:]
	}
	if arr {
		t += "[]"
	}
	return strings.ToLower(t)
}

func sameParamTypes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if bareParamToken(a[i]) != bareParamToken(b[i]) {
			return false
		}
	}
	return true
}

// selectLookupOverload narrows a lookup that delivered several same-named
// overloads to the one the caller's signature names; true when it did.
func (h *Handler) selectLookupOverload(ctx context.Context, out map[string]any, signature string) bool {
	sym, ok := lookupResultSymbol(out)
	if !ok || signature == "" {
		return false
	}
	cands := append([]grove.SymbolRecord{sym}, h.sameNameOverloads(ctx, sym)...)
	if len(cands) < 2 {
		return false
	}
	want, ok := signatureParamTypes(signature, sym.Language)
	if !ok {
		out["note"] = appendNote(stringArg(out, "note", ""), fmt.Sprintf("signature %q has no parseable parameter list; all overloads are shown", signature))
		return false
	}
	var match []grove.SymbolRecord
	for _, c := range cands {
		src := c.Signature
		if !strings.Contains(src, "(") {
			src = c.RawText
		}
		if got, ok := signatureParamTypes(src, c.Language); ok && sameParamTypes(got, want) {
			match = append(match, c)
		}
	}
	if len(match) != 1 {
		out["note"] = appendNote(stringArg(out, "note", ""), fmt.Sprintf("signature %q matched %d of %d overloads; all are shown", signature, len(match), len(cands)))
		return false
	}
	m := match[0]
	out["symbol"] = m
	if _, has := out["content"]; has {
		out["content"] = m.RawText
	}
	delete(out, "overloads")
	out["note"] = appendNote(stringArg(out, "note", ""), fmt.Sprintf("signature selected 1 of %d overloads (line %d)", len(cands), m.Span.Start))
	return true
}

// addLookupCallerSignal flags test-only APIs on a lookup result: for a
// method/constructor (and its same-file overloads), or — for a type — its
// overloaded constructors/methods. Only symbols whose callers are ALL tests
// are named, so ordinary lookups are unchanged. See testOnlySignal.
// onlyPrimary restricts the check to the delivered symbol (a signature
// already selected one overload).
func (h *Handler) addLookupCallerSignal(ctx context.Context, result any, onlyPrimary bool) any {
	out, ok := result.(map[string]any)
	if !ok || h.Grove == nil {
		return result
	}
	if m, present := out["matched"].(bool); present && !m {
		return result
	}
	sym, ok := lookupResultSymbol(out)
	if !ok {
		return result
	}
	var check []grove.SymbolRecord
	switch {
	case testOnlyCheckable(sym.Kind) && onlyPrimary:
		check = []grove.SymbolRecord{sym}
	case testOnlyCheckable(sym.Kind):
		check = append([]grove.SymbolRecord{sym}, h.sameNameOverloads(ctx, sym)...)
	case isTypeKind(sym.Kind):
		check = h.overloadedMembers(ctx, sym)
	}
	if len(check) == 0 {
		return result
	}
	if lines := h.testOnlySignal(ctx, check); len(lines) > 0 {
		if isTypeKind(sym.Kind) {
			lines = h.keepOverloadGaps(lines)
		}
		if len(lines) > 0 {
			out["testOnly"] = lines
		}
	}
	return out
}

// overloadedMembers returns t's constructors/methods that share a name with
// another member — the shape where one variant can be added and never wired
// in while its siblings carry the production traffic.
func (h *Handler) overloadedMembers(ctx context.Context, t grove.SymbolRecord) []grove.SymbolRecord {
	syms, err := h.Grove.FileSymbols(ctx, t.FilePath)
	if err != nil {
		return nil
	}
	prefix := lookupSymbolName(t) + "."
	byQN := map[string][]grove.SymbolRecord{}
	var order []string
	for _, s := range dedupeSymbolsByID(syms) {
		qn := displayQN(s)
		if !testOnlyCheckable(s.Kind) || !strings.HasPrefix(qn, prefix) || strings.Contains(qn[len(prefix):], ".") {
			continue
		}
		if _, seen := byQN[qn]; !seen {
			order = append(order, qn)
		}
		byQN[qn] = append(byQN[qn], s)
	}
	var out []grove.SymbolRecord
	for _, qn := range order {
		if group := byQN[qn]; len(group) >= 2 {
			out = append(out, group...)
		}
	}
	return out
}

// keepOverloadGaps keeps only type-outline findings that point at a
// production sibling ("Production code calls the other overload(s)"): an
// overloaded member nobody calls in production at all is not the
// added-but-unwired shape and would be noise in a class outline.
func (h *Handler) keepOverloadGaps(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "Production code calls the other overload") {
			out = append(out, l)
		}
	}
	return out
}
