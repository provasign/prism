package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// isTestDouble reports whether a file path looks like a test double (mock/fake/
// stub) or a test file, so lookup can prefer the real implementation when
// several symbols share a name.
func isTestDouble(path string) bool {
	lp := strings.ToLower(filepath.ToSlash(path))
	return strings.HasSuffix(lp, "_test.go") ||
		strings.Contains(lp, "mock") || strings.Contains(lp, "fake") ||
		strings.Contains(lp, "stub") || strings.Contains(lp, "/testdata/")
}

// searchTestLabel distinguishes a test double (mock/fake/stub) from other
// test code for search locator lines. isTestDouble also covers every
// _test.go file, which is right for demoting candidates but labelled real
// test functions (TestErrorSlice, TestContextGetErrorSlice) "[test double]".
func searchTestLabel(s grove.SymbolRecord) string {
	lp := strings.ToLower(filepath.ToSlash(s.FilePath))
	ln := strings.ToLower(s.Name)
	for _, marker := range []string{"mock", "fake", "stub"} {
		if strings.Contains(lp, marker) || strings.Contains(ln, marker) {
			return "test double"
		}
	}
	if isTestDouble(s.FilePath) || isTestFilePath(s.FilePath) {
		return "test"
	}
	return ""
}

// projectSymbol returns only the requested columns of a symbol. file, line and
// name are always included as identity. Recognized fields: signature, doc, body,
// kind, parent, modifiers. An empty list means "default" (caller adds the body).
func projectSymbol(root string, s grove.SymbolRecord, fields []string) map[string]any {
	qn := s.QualifiedName
	if qn == "" {
		qn = s.Name
	}
	out := map[string]any{"name": qn, "file": s.FilePath, "line": s.Span.Start}
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "signature", "sig":
			out["signature"] = s.Signature
		case "doc", "docstring":
			out["doc"] = s.Docstring
		case "body", "source":
			out["body"] = symbolSourceText(root, s)
		case "kind":
			out["kind"] = string(s.Kind)
		case "parent":
			out["parent"] = s.ParentSymbol
		case "modifiers":
			out["modifiers"] = s.Modifiers
		case "name", "file", "line":
			// already included as identity
		}
	}
	return out
}

// lookupCandidateLabel names a tied lookup candidate with its line span, so two
// same-named symbols in one file never render as identical lines.
// cFamilyDefinitionBonus breaks a lookup tie between a C/C++ definition and
// its prototypes in the definition's favor; it is smaller than every other
// ranking signal.
const cFamilyDefinitionBonus = 2

func cFamilyDeclaration(s grove.SymbolRecord) bool {
	return (s.Language == "c" || s.Language == "cpp") && slices.Contains(s.Annotations, "declaration")
}

// cFamilyDefinition reports a C/C++ callable with a body (not a prototype).
func cFamilyDefinition(s grove.SymbolRecord) bool {
	if s.Language != "c" && s.Language != "cpp" || cFamilyDeclaration(s) {
		return false
	}
	switch s.Kind {
	case "function", "method", "constructor":
		return true
	}
	return false
}

func lookupCandidateLabel(s grove.SymbolRecord) string {
	return fmt.Sprintf("%s (%s:%d-%d)", lookupSymbolName(s), s.FilePath, s.Span.Start, s.Span.End)
}

// lookupOverloads returns the other overloads of a looked-up symbol, in source
// order. Bodies are included while the total delivered body text (the primary
// body plus overloads) stays within searchFullBodyMaxBytes; past that, an
// overload keeps its signature and span so the agent can read it by range.
// fields= projections apply to each overload the same way as the primary.
func lookupOverloads(root string, overloads []grove.SymbolRecord, fields []string, primaryBytes int) []map[string]any {
	sorted := append([]grove.SymbolRecord(nil), overloads...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Span.Start < sorted[j].Span.Start })
	wantBody := len(fields) == 0
	for _, f := range fields {
		if lf := strings.ToLower(f); lf == "body" || lf == "source" {
			wantBody = true
		}
	}
	used := primaryBytes
	out := make([]map[string]any, 0, len(sorted))
	for _, s := range sorted {
		var entry map[string]any
		if len(fields) > 0 {
			entry = projectSymbol(root, s, fields)
			delete(entry, "body")
		} else {
			entry = map[string]any{"name": lookupSymbolName(s), "file": s.FilePath, "line": s.Span.Start,
				"signature": s.Signature}
		}
		entry["end"] = s.Span.End
		if wantBody {
			if used+len(s.RawText) <= searchFullBodyMaxBytes {
				used += len(s.RawText)
				if len(fields) > 0 {
					entry["body"] = symbolSourceText(root, s)
				} else {
					entry["content"] = symbolSourceText(root, s)
				}
			} else {
				entry["bodyOmitted"] = true
				if _, ok := entry["signature"]; !ok {
					entry["signature"] = s.Signature
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

func lookupSymbolName(s grove.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

func (h *Handler) toolLookup(ctx context.Context, args map[string]any) (any, error) {
	if requests, batch, err := lookupBatchRequests(args["name"]); err != nil {
		return nil, err
	} else if batch {
		return h.toolLookupBatch(ctx, args, requests)
	}
	out, err := h.lookupSymbol(ctx, args, "")
	if err != nil {
		return out, err
	}
	selected := false
	if sig := stringArg(args, "signature", ""); sig != "" {
		if m, ok := out.(map[string]any); ok {
			selected = h.selectLookupOverload(ctx, m, sig)
		}
	}
	// Delivered-body size cap (bodycap.go), kept outside lookupSymbol.
	return h.addLookupCallerSignal(ctx, h.capLookupResult(ctx, out), selected), nil
}

func (h *Handler) lookupSymbol(ctx context.Context, args map[string]any, fileScope string) (any, error) {
	name := stringArg(args, "name", stringArg(args, "qualifiedName", ""))
	if name == "" {
		return nil, errors.New("name is required")
	}
	// CON-10: path:line names the symbol declared there.
	if pathLineForm.MatchString(name) {
		resolved := map[string]any{"name": name}
		if _, err := h.resolvePathLine(ctx, resolved, "name"); err != nil {
			return nil, err
		}
		args = copyArgs(args)
		args["name"], args["file"] = resolved["name"], resolved["file"]
		name = resolved["name"].(string)
	}
	if fileScope != "" {
		var err error
		fileScope, err = h.lookupFileScope(fileScope)
		if err != nil {
			return nil, err
		}
	}
	// Optional column projection: return only the requested fields (signature,
	// doc, body, kind, parent, modifiers) instead of the full source body.
	var fields []string
	if raw, ok := args["fields"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				if sv, ok := v.(string); ok {
					fields = append(fields, sv)
				}
			}
		}
	}
	// Optional file disambiguator: when several symbols share a qualified name
	// (e.g. two packages with a Service.DecryptedValues), pass the file path (or
	// any substring of it, as shown in prism_search results) to pick the right one.
	fileHint := strings.ToLower(stringArg(args, "file", ""))

	// repo: answer from one linked repository (or "all" of them), for a
	// name this repository also defines.
	if repo := stringArg(args, "repo", ""); repo != "" {
		return h.lookupInRepo(ctx, name, repo, fields)
	}
	out, err := h.resolveLookup(ctx, name, fileScope, fileHint, fields)
	if err == nil {
		// CON-10: a file path that matched no symbol is a usage error naming
		// the forms that work. A Java/Kotlin file path still resolves to the
		// class it declares, so this only replaces a miss.
		if m, ok := out.(map[string]any); ok && m["matched"] == false {
			if ferr := h.fileNotSymbolError(name); ferr != nil {
				return nil, ferr
			}
		}
	}
	return out, err
}
