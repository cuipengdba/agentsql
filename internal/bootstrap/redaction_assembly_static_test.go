package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBootstrapUsesRedactionAssembly(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(current), "bootstrap.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "BuildRedactionAssembly" {
			return true
		}
		base, ok := selector.X.(*ast.Ident)
		found = ok && base.Name == "config"
		return true
	})
	if !found {
		t.Fatal("bootstrap must call config.BuildRedactionAssembly")
	}
}
