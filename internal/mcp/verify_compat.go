package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
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
