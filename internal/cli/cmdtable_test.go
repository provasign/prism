package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// helpFlagToken matches a flag as help writes it: -x or --name.
var helpFlagToken = regexp.MustCompile(`(?:^|[^\w-])(-{1,2}[a-zA-Z][a-zA-Z0-9-]*)`)

// Every flag a command accepts is in its --help (CON-09, BUG-08).
func TestCommandHelpListsEveryFlag(t *testing.T) {
	for _, c := range commandTable {
		help := commandHelp(c.name)
		inHelp := map[string]bool{}
		for _, m := range helpFlagToken.FindAllStringSubmatch(help, -1) {
			inHelp[m[1]] = true
		}
		for _, f := range strings.Fields(c.flags) {
			if strings.HasSuffix(f, "!") {
				continue // removed; recognized only to say so
			}
			f = strings.TrimSuffix(f, "=")
			if !inHelp[f] {
				t.Errorf("prism %s accepts %s but its help does not mention it:\n%s", c.name, f, help)
			}
		}
	}
}

// extraParsers are the functions besides a command's run function that
// parse its arguments.
var extraParsers = map[string][]string{
	"init":          {"runInit"},
	"install":       {"runInit"},
	"change-impact": {"cmdChangeImpactDiff"},
	"workspace":     {"workspaceAdd", "workspaceRemove", "workspaceDiscover", "workspaceFetch", "workspaceExport"},
}

var parserFlag = regexp.MustCompile(`^-{1,2}[a-zA-Z][a-zA-Z0-9-]*$`)

// Every flag a command's parser matches is declared in its table entry, so
// the table (and with it the help) cannot fall behind the parser.
func TestCommandFlagsMatchTheirParsers(t *testing.T) {
	funcs := parseCLIFuncs(t)
	for _, c := range commandTable {
		name := runtime.FuncForPC(reflect.ValueOf(c.run).Pointer()).Name()
		name = name[strings.LastIndex(name, ".")+1:]
		declared := map[string]bool{}
		for _, f := range strings.Fields(c.flags) {
			declared[strings.TrimRight(f, "=!")] = true
		}
		var undeclared []string
		for _, fn := range append([]string{name}, extraParsers[c.name]...) {
			fd := funcs[fn]
			if fd == nil {
				t.Fatalf("prism %s: parser %s not found", c.name, fn)
			}
			for f := range flagLiterals(fd) {
				if !declared[f] {
					undeclared = append(undeclared, f)
				}
			}
		}
		sort.Strings(undeclared)
		if len(undeclared) > 0 {
			t.Errorf("prism %s parses %v, which its table entry does not declare", c.name, undeclared)
		}
	}
}

func parseCLIFuncs(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
					funcs[fd.Name.Name] = fd
				}
			}
		}
	}
	return funcs
}

// flagLiterals are the flag-shaped string literals a function matches on:
// case labels, == comparisons, and value flags handed to a parsing helper.
func flagLiterals(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	lit := func(e ast.Expr, longOnly bool) {
		bl, ok := e.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return
		}
		v, _ := strconv.Unquote(bl.Value)
		if parserFlag.MatchString(v) && (!longOnly || strings.HasPrefix(v, "--")) {
			out[v] = true
		}
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CaseClause:
			for _, e := range x.List {
				lit(e, false)
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				lit(x.X, false)
				lit(x.Y, false)
			}
		case *ast.CallExpr:
			for _, a := range x.Args {
				lit(a, true)
			}
		}
		return true
	})
	return out
}

func TestNormalizeArgs(t *testing.T) {
	c := &command{name: "x", flags: "--file= --strict --old! -C="}
	cases := []struct {
		in   []string
		want []string
		err  string
	}{
		{[]string{"A", "--file=a.go"}, []string{"A", "--file", "a.go"}, ""},
		{[]string{"--file", "--strict"}, []string{"--file", "--strict"}, ""}, // a value is never a flag
		{[]string{"--", "--file=x", "-z"}, []string{"--", "--file=x", "-z"}, ""},
		{[]string{"-C", "2", "-1"}, []string{"-C", "2", "-1"}, ""},
		{[]string{"--old"}, []string{"--old"}, ""},
		{[]string{"--strict=yes"}, nil, "--strict takes no value"},
		{[]string{"--fiel"}, nil, `unknown flag "--fiel"; did you mean "--file"?`},
		{[]string{"--nothing-like-it"}, nil, `unknown flag "--nothing-like-it" (see prism help)`},
	}
	for _, tc := range cases {
		got, err := c.normalizeArgs(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: err %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}
