package mcp

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

func isSyntheticAnonymousJavaSymbol(sym grove.SymbolRecord) bool {
	return filepath.Ext(sym.FilePath) == ".java" &&
		strings.Contains(sym.QualifiedName, ".<anonymous@")
}

// goAddedTrailingVariadic recognizes one backwards-compatible Go signature
// edit. Go has no overloads: existing calls remain valid when an unchanged
// declaration gains only a final variadic parameter. Return false on any
// parse failure or other edit so verify retains its conservative behavior.
func goAddedTrailingVariadic(before, after string) bool {
	old := parseGoSignature(before)
	now := parseGoSignature(after)
	if old == nil || now == nil || old.Name.Name != now.Name.Name ||
		!sameGoFields(old.Recv, now.Recv) ||
		!sameGoFields(old.Type.TypeParams, now.Type.TypeParams) ||
		!sameGoFields(old.Type.Results, now.Type.Results) {
		return false
	}
	oldParams, newParams := old.Type.Params.List, now.Type.Params.List
	if len(newParams) != len(oldParams)+1 {
		return false
	}
	for i := range oldParams {
		if !sameGoField(oldParams[i], newParams[i]) {
			return false
		}
	}
	_, ok := newParams[len(newParams)-1].Type.(*ast.Ellipsis)
	return ok
}

func parseGoSignature(signature string) *ast.FuncDecl {
	src := "package verifycompat\n" + strings.TrimSpace(signature) + " {}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "signature.go", src, 0)
	if err != nil || len(f.Decls) != 1 {
		return nil
	}
	decl, _ := f.Decls[0].(*ast.FuncDecl)
	return decl
}

func sameGoFields(a, b *ast.FieldList) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if len(a.List) != len(b.List) {
		return false
	}
	for i := range a.List {
		if !sameGoField(a.List[i], b.List[i]) {
			return false
		}
	}
	return true
}

func sameGoField(a, b *ast.Field) bool {
	if len(a.Names) != len(b.Names) || types.ExprString(a.Type) != types.ExprString(b.Type) {
		return false
	}
	for i := range a.Names {
		if a.Names[i].Name != b.Names[i].Name {
			return false
		}
	}
	return true
}

// goUntypedBinding matches the left side of `x := ` or `var x = `, where the
// bound variable takes whatever type the right side has.
var goUntypedBinding = regexp.MustCompile(`(^|[{;])\s*(var\s+)?\w+(\s*,\s*\w+)*\s*:?=\s*$`)

// goFuncValueRefs finds references that use a Go function as a value
// (`var hook = pkg.Do`, `register(pkg.Do)`) rather than calling it. Adding a
// trailing variadic parameter changes the function's type, so those uses
// stop compiling even though direct calls stay valid. The reference layer
// is name-based, so a reference counts only as `pkg.Name` from another
// package or a bare `Name` inside the declaring package.
func (h *Handler) goFuncValueRefs(ctx context.Context, sym grove.SymbolRecord) []missedSite {
	if h.Grove == nil || sym.Name == "" {
		return nil
	}
	if sym.Kind == "method" {
		return h.goMethodValueRefs(ctx, sym)
	}
	if sym.Kind != "function" {
		return nil
	}
	res, err := h.Grove.References(ctx, sym.Name)
	if err != nil {
		return nil
	}
	pkgDir := filepath.ToSlash(filepath.Dir(sym.FilePath))
	pkgName := path.Base(pkgDir)
	qualified := regexp.MustCompile(`\b` + regexp.QuoteMeta(pkgName) + `\.` + regexp.QuoteMeta(sym.Name) + `\b`)
	bare := regexp.MustCompile(`(^|[^.\w])` + regexp.QuoteMeta(sym.Name) + `\b`)
	var out []missedSite
	for _, r := range res.Refs {
		if !strings.HasSuffix(r.File, ".go") {
			continue
		}
		line := stripCommentsAndStringsLine(sourceLineAt(h.Root, r.File, r.Line))
		re := qualified
		if filepath.ToSlash(filepath.Dir(r.File)) == pkgDir {
			re = bare
		}
		for _, loc := range re.FindAllStringIndex(line, -1) {
			rest := strings.TrimLeft(line[loc[1]:], " \t")
			if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "[") {
				continue // a call, or an explicit generic instantiation call
			}
			if strings.HasPrefix(strings.TrimSpace(line), "func ") && strings.Contains(line, "func "+sym.Name+"(") {
				continue // the declaration itself
			}
			if goUntypedBinding.MatchString(line[:loc[0]]) && (rest == "" || strings.HasPrefix(rest, ";") || strings.HasPrefix(rest, "}")) {
				continue // `x := Do` / `var x = Do` takes the new type and still compiles
			}
			out = append(out, missedSite{
				Symbol: sym.Name, QualifiedName: r.Enclosing, File: r.File, Line: r.Line,
				Kind: "value-reference", BecauseOf: displayQN(sym),
				Detail: "uses " + sym.Name + " as a value; its type changed with the new variadic parameter",
			})
			break
		}
		if len(out) == 25 {
			break
		}
	}
	return out
}

// goMethodValueRefs is goFuncValueRefs for a method: a method value
// (`var m func(int) int = sq.Area`) or method expression (`Square.Area`)
// has the method's type, which a new trailing variadic parameter changes,
// while calls stay valid. The scan is by name (`.Area` not followed by a
// call); a same-named field anywhere in the repository is called out so the
// receiver gets checked.
func (h *Handler) goMethodValueRefs(ctx context.Context, sym grove.SymbolRecord) []missedSite {
	res, err := h.Grove.References(ctx, sym.Name)
	if err != nil {
		return nil
	}
	detail := "uses " + sym.Name + " as a method value; its type changed with the new variadic parameter"
	if fields, ferr := h.Grove.SearchSymbols(ctx, sym.Name, 50); ferr == nil {
		for _, f := range fields {
			if f.Name == sym.Name && f.Kind == "field" && f.Language == "go" {
				detail += " (a field named " + sym.Name + " also exists: check the receiver)"
				break
			}
		}
	}
	sel := regexp.MustCompile(`\.` + regexp.QuoteMeta(sym.Name) + `\b`)
	var out []missedSite
	for _, r := range res.Refs {
		if !strings.HasSuffix(r.File, ".go") {
			continue
		}
		line := stripCommentsAndStringsLine(sourceLineAt(h.Root, r.File, r.Line))
		if strings.HasPrefix(strings.TrimSpace(line), "func ") && strings.Contains(line, ") "+sym.Name+"(") {
			continue // the declaration
		}
		for _, loc := range sel.FindAllStringIndex(line, -1) {
			rest := strings.TrimLeft(line[loc[1]:], " \t")
			if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "[") {
				continue // a call
			}
			if goUntypedBinding.MatchString(line[:strings.LastIndexAny(line[:loc[0]+1], " \t=(,{")+1]) && (rest == "" || strings.HasPrefix(rest, ";") || strings.HasPrefix(rest, "}")) {
				continue // `m := sq.Area` takes the new type
			}
			out = append(out, missedSite{
				Symbol: sym.Name, QualifiedName: r.Enclosing, File: r.File, Line: r.Line,
				Kind: "value-reference", BecauseOf: displayQN(sym), Detail: detail,
			})
			break
		}
		if len(out) == 25 {
			break
		}
	}
	return out
}
