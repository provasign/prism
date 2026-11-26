// Package funlen holds the soft function-length limit (ENG-02 step 5).
// It is a test, not a linter dependency: the module's non-test Go code may
// not gain a function longer than maxLines, and the functions that already
// are longer may only shrink. Split one below the limit and remove its
// entry; split one partly and lower its number.
package funlen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const maxLines = 150

// overLimit is each function already over maxLines, keyed by package
// directory and name, with the most lines it may have.
var overLimit = map[string]int{
	"internal/cli.cmdSearch":                              163,
	"internal/core.Handler.InvokeContext":                 152,
	"internal/core.Handler.compactSearchBodiesLegacyWith": 171,
	"internal/core.Handler.impactOtherMentions":           165,
	"internal/core.Handler.partitionLocalFamiliesCapped":  189,
	"internal/core.Handler.readRanges":                    174,
	"internal/core.Handler.rootConsumers":                 207,
	"internal/core.Handler.toolQueryScoped":               205,
	"internal/core.Handler.toolTestPlan":                  186,
	"internal/core.fitTextPresentation":                   203,
	"internal/core.planTraverse":                          163,
	"internal/core.renderChangeImpactLayout":              217,
	"internal/core.renderOneSearchText":                   220,
	"internal/core.renderTestPlanAsText":                  151,
	"internal/ranking.Select":                             164,
	"internal/view.build":                                 164,
}

func TestFunctionLength(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	lengths := functionLengths(t, root)
	var problems []string
	for key, n := range lengths {
		limit, listed := overLimit[key]
		switch {
		case !listed && n > maxLines:
			problems = append(problems, fmt.Sprintf("%s is %d lines; split it to %d or fewer", key, n, maxLines))
		case listed && n > limit:
			problems = append(problems, fmt.Sprintf("%s grew to %d lines (its limit is %d); split it instead", key, n, limit))
		case listed && n <= maxLines:
			problems = append(problems, fmt.Sprintf("%s is %d lines now; remove it from overLimit", key, n))
		case listed && n < limit:
			problems = append(problems, fmt.Sprintf("%s shrank to %d lines; lower its overLimit entry from %d", key, n, limit))
		}
	}
	for key := range overLimit {
		if _, ok := lengths[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s no longer exists; remove it from overLimit", key))
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// functionLengths returns the length of every function with a body in the
// module's non-test Go files, from the func line to the closing brace.
func functionLengths(t *testing.T, root string) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "docs") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		pkg := filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := pkg + "." + fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				key = pkg + "." + receiverName(fd.Recv.List[0].Type) + "." + fd.Name.Name
			}
			n := fset.Position(fd.End()).Line - fset.Position(fd.Pos()).Line + 1
			if n > out[key] { // per-platform variants: the longest counts
				out[key] = n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func receiverName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}
