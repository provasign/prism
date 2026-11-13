// mroracle: list every reference from the packages under <appdir> (tests included)
// to objects declared in module <libmod>, as resolved by go/types under the
// enclosing go.work. One JSON object per (key, file, line).
package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

type ref struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func key(obj types.Object) (string, string) {
	pkg := obj.Pkg().Path()
	switch o := obj.(type) {
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			t := sig.Recv().Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if n, ok := t.(*types.Named); ok {
				return pkg + "#" + n.Obj().Name() + "." + o.Name(), "method"
			}
			return pkg + "#?." + o.Name(), "ifacemethod"
		}
		return pkg + "#" + o.Name(), "func"
	case *types.Var:
		if o.IsField() {
			return pkg + "#field." + o.Name(), "field"
		}
		return pkg + "#" + o.Name(), "var"
	case *types.TypeName:
		return pkg + "#" + o.Name(), "type"
	case *types.Const:
		return pkg + "#" + o.Name(), "const"
	}
	return pkg + "#" + obj.Name(), "other"
}

func main() {
	appdir, libmod := os.Args[1], os.Args[2]
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo, Dir: appdir, Tests: true}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	seen := map[string]bool{}
	enc := json.NewEncoder(os.Stdout)
	for _, p := range pkgs {
		for _, e := range p.Errors {
			fmt.Fprintln(os.Stderr, "pkgerr:", e)
		}
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.GoFiles {
			rel, _ := filepath.Rel(appdir, f)
			if !seen["file|"+rel] {
				seen["file|"+rel] = true
				fmt.Fprintf(os.Stderr, "loaded: %s\n", rel)
			}
		}
		for id, obj := range p.TypesInfo.Uses {
			if obj == nil || obj.Pkg() == nil {
				continue
			}
			pp := obj.Pkg().Path()
			if pp != libmod && !strings.HasPrefix(pp, libmod+"/") {
				continue
			}
			pos := p.Fset.Position(id.Pos())
			rel, _ := filepath.Rel(appdir, pos.Filename)
			if strings.HasPrefix(rel, "..") {
				continue
			}
			k, kind := key(obj)
			s := fmt.Sprintf("%s|%s|%d|%d", k, rel, pos.Line, pos.Column)
			if seen[s] {
				continue
			}
			seen[s] = true
			enc.Encode(ref{k, kind, rel, pos.Line, pos.Column})
		}
	}
}
