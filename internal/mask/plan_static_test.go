package mask

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlanInjectionSurface(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	maskDir := filepath.Dir(current)
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, maskDir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := packages["mask"]
	returnedPlans := []string{}
	gateReferenced := false
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			if containsTypeName(fn.Type.Params, "ActiveHasher") {
				t.Errorf("exported function %s accepts ActiveHasher", fn.Name.Name)
			}
			if containsTypeName(fn.Type.Results, "RedactionPlan") {
				returnedPlans = append(returnedPlans, fn.Name.Name)
			}
			if fn.Name.Name == "BuildRedactionPlan" {
				if !buildPlanSignature(fn.Type) {
					t.Error("BuildRedactionPlan signature changed")
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					if ident, ok := node.(*ast.Ident); ok && ident.Name == "versionedActiveEnabled" {
						gateReferenced = true
					}
					return true
				})
			}
		}
	}
	if len(returnedPlans) != 1 || returnedPlans[0] != "BuildRedactionPlan" {
		t.Fatalf("exported RedactionPlan constructors = %v", returnedPlans)
	}
	if !gateReferenced {
		t.Error("BuildRedactionPlan does not reference versionedActiveEnabled")
	}
}

func TestPlanProductionReferencesAreWhitelisted(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	wanted := map[string]string{"BuildRedactionPlan": filepath.Join("internal", "config"), "WithRedactionPlan": filepath.Join("internal", "config")}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() && strings.HasPrefix(info.Name(), "go-build") {
			return filepath.SkipDir
		}
		if info.IsDir() && path != root && strings.HasPrefix(info.Name(), ".") {
			return filepath.SkipDir
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "vendor") {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "github.com/cuipengdba/agentsql/internal/mask" {
				name := "mask"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				aliases[name] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			base, ok := sel.X.(*ast.Ident)
			if !ok || !aliases[base.Name] {
				return true
			}
			expected, tracked := wanted[sel.Sel.Name]
			if !tracked {
				return true
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			if filepath.Clean(rel) != expected {
				t.Errorf("mask.%s referenced from %s", sel.Sel.Name, path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func containsTypeName(fields *ast.FieldList, name string) bool {
	found := false
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		ast.Inspect(field.Type, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && ident.Name == name {
				found = true
			}
			return true
		})
	}
	return found
}

func buildPlanSignature(signature *ast.FuncType) bool {
	if signature.Params == nil || len(signature.Params.List) != 2 {
		return false
	}
	first, ok := signature.Params.List[0].Type.(*ast.Ident)
	if !ok || first.Name != "int" {
		return false
	}
	mapped, ok := signature.Params.List[1].Type.(*ast.MapType)
	if !ok {
		return false
	}
	key, ok := mapped.Key.(*ast.Ident)
	if !ok || key.Name != "int" {
		return false
	}
	slice, ok := mapped.Value.(*ast.ArrayType)
	if !ok || slice.Len != nil {
		return false
	}
	element, ok := slice.Elt.(*ast.Ident)
	return ok && element.Name == "byte"
}
