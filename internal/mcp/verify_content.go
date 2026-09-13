package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"

	"github.com/provasign/prism/internal/grove"
)

// goUnexportedStringConstValueOnly identifies the narrow case where verify
// cannot infer a broken reference from the edit: an unexported Go constant
// retains its declaration name and type while only string-literal content
// changes. The value may still affect behavior, so callers receive an
// advisory rather than a claim that the content was verified.
func goUnexportedStringConstValueOnly(before, after *grove.SymbolRecord) bool {
	if before == nil || after == nil || before.Kind != "const" || after.Kind != "const" ||
		before.Name != after.Name || before.Name == "" || ast.IsExported(after.Name) ||
		filepath.Ext(after.FilePath) != ".go" {
		return false
	}
	oldName, oldOK := goUntypedStringConst(before.RawText)
	newName, newOK := goUntypedStringConst(after.RawText)
	return oldOK && newOK && oldName == before.Name && newName == after.Name
}

func goUntypedStringConst(raw string) (string, bool) {
	file, err := parser.ParseFile(token.NewFileSet(), "constant.go", "package p\n"+raw, 0)
	if err != nil || len(file.Decls) != 1 {
		return "", false
	}
	decl, ok := file.Decls[0].(*ast.GenDecl)
	if !ok || decl.Tok != token.CONST || len(decl.Specs) != 1 {
		return "", false
	}
	spec, ok := decl.Specs[0].(*ast.ValueSpec)
	if !ok || spec.Type != nil || len(spec.Names) != 1 || len(spec.Values) != 1 ||
		!goStringLiteralExpression(spec.Values[0]) {
		return "", false
	}
	return spec.Names[0].Name, true
}

func goStringLiteralExpression(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.BasicLit:
		return value.Kind == token.STRING
	case *ast.BinaryExpr:
		return value.Op == token.ADD && goStringLiteralExpression(value.X) && goStringLiteralExpression(value.Y)
	case *ast.ParenExpr:
		return goStringLiteralExpression(value.X)
	default:
		return false
	}
}
