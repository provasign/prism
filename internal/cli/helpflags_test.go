package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var flagLiteral = regexp.MustCompile(`^--[a-z][a-z0-9-]*=?$`)

// cliSource parses the package's non-test files and returns its top-level
// functions by name.
func cliSource(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}
	return funcs
}

// dispatch maps each command in Run's switch to the handler it returns.
func dispatch(t *testing.T, funcs map[string]*ast.FuncDecl) map[string]string {
	out := map[string]string{}
	ast.Inspect(funcs["Run"], func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		handler := ""
		for _, st := range cc.Body {
			if r, ok := st.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
				if call, ok := r.Results[0].(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok {
						handler = id.Name
					}
				}
			}
		}
		for _, e := range cc.List {
			if lit, ok := e.(*ast.BasicLit); ok && handler != "" {
				name, _ := strconv.Unquote(lit.Value)
				out[name] = handler
			}
		}
		return true
	})
	return out
}

// flagsCompared are literals in handler bodies that a parser compares an
// argument against: a case label, an == / HasPrefix operand, or an
// argument to a flag-table helper. Error-message text is not a flag.
func acceptedFlags(fd *ast.FuncDecl) []string {
	seen := map[string]bool{}
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, _ := strconv.Unquote(lit.Value)
			if flagLiteral.MatchString(s) {
				seen[strings.TrimSuffix(s, "=")] = true
			}
		}
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CaseClause:
			for _, e := range x.List {
				add(e)
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL {
				add(x.X)
				add(x.Y)
			}
		case *ast.CallExpr:
			name := ""
			switch f := x.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			if name == "HasPrefix" || name == "dirOnlyArgs" || strings.HasPrefix(name, "flagValue") || strings.HasPrefix(name, "parse") || strings.HasPrefix(name, "take") {
				for _, a := range x.Args {
					add(a)
				}
			}
		case *ast.CompositeLit:
			for _, e := range x.Elts {
				add(e)
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					add(kv.Key)
				}
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Every flag a command's parser accepts is in that command's --help. This
// would have caught BUG-05 (--file) and BUG-07 (--force).
func TestCommandHelpListsEveryAcceptedFlag(t *testing.T) {
	funcs := cliSource(t)
	for cmd, handler := range dispatch(t, funcs) {
		fd := funcs[handler]
		if fd == nil || strings.HasPrefix(cmd, "-") || cmd == "help" || cmd == "version" {
			continue
		}
		help := commandHelp(cmd)
		if help == helpText {
			t.Errorf("%s: no help block", cmd)
			continue
		}
		for _, flag := range acceptedFlags(fd) {
			// --depth/--graph-depth on query are accepted only to say they were removed.
			if flag == "--help" || (cmd == "query" && (flag == "--depth" || flag == "--graph-depth")) {
				continue
			}
			if !regexp.MustCompile(regexp.QuoteMeta(flag) + `\b`).MatchString(help) {
				t.Errorf("%s --help does not mention %s", cmd, flag)
			}
		}
	}
}

func TestCommandHelpKeepsEveryContinuationLine(t *testing.T) {
	for _, cmd := range []string{"change-impact", "search", "index"} {
		if lines := strings.Count(commandHelp(cmd), "\n"); lines < 3 {
			t.Errorf("%s --help is %d line(s)", cmd, lines)
		}
	}
}

func TestUnknownCommandIsOneLineWithSuggestion(t *testing.T) {
	var rc int
	out := captureStdout(func() {
		err := captureStderr(func() { rc = Run([]string{"chnage-impact"}) })
		if !strings.Contains(err, `did you mean "change-impact"`) || strings.Count(err, "\n") != 1 {
			t.Errorf("stderr = %q", err)
		}
	})
	if rc != 2 || out != "" {
		t.Fatalf("rc=%d stdout=%q", rc, out)
	}
}
