package authorizedexecute

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBusinessDriverImportsStayInsideCapabilityDomain(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	forbidden := map[string]bool{
		"database/sql":                    true,
		"database/sql/driver":             true,
		"github.com/go-sql-driver/mysql":  true,
		"github.com/jackc/pgx/v5":         true,
		"github.com/jackc/pgx/v5/pgconn":  true,
		"github.com/jackc/pgx/v5/pgxpool": true,
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if vanishedGoBuildPath(root, path, walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".gocache" || entry.Name() == ".design" || strings.HasPrefix(entry.Name(), "go-build")) {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		relative, _ := filepath.Rel(root, path)
		for _, imported := range parsed.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			if !forbidden[name] {
				continue
			}
			allowedBusiness := strings.HasPrefix(filepath.ToSlash(relative), "internal/authorizedexecute/internal/businessdb/")
			allowedControl := strings.HasPrefix(filepath.ToSlash(relative), "internal/store/") || strings.HasPrefix(filepath.ToSlash(relative), "cmd/agentsqlctl/")
			normalized := filepath.ToSlash(relative)
			allowedB5S3 := normalized == "internal/b5session/sql_ledger.go" || normalized == "internal/b5session/tombstone.go" || normalized == "internal/b5session/pg_inventory.go"
			require.Truef(t, allowedBusiness || allowedControl || allowedB5S3, "%s imports driver capability %s", relative, name)
		}
		return nil
	})
	require.NoError(t, err)
}

func TestNoProductionImportOfDeletedExecutorPackage(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if vanishedGoBuildPath(root, path, walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".gocache" || entry.Name() == ".design" || strings.HasPrefix(entry.Name(), "go-build")) {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range parsed.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			require.NotEqual(t, "github.com/cuipengdba/agentsql/internal/executor", name, path)
		}
		return nil
	})
	require.NoError(t, err)
}

func TestPublicStatementCapabilityCannotAcceptCallerSQL(t *testing.T) {
	statement := reflect.TypeOf((*Statement)(nil)).Elem()
	for index := 0; index < statement.NumMethod(); index++ {
		method := statement.Method(index)
		require.NotContains(t, []string{"Exec", "Prepare", "Raw", "Conn"}, method.Name)
		if method.Name == "Query" || method.Name == "Execute" || method.Name == "Explain" || method.Name == "ExecuteTransactional" {
			for parameter := 0; parameter < method.Type.NumIn(); parameter++ {
				require.NotEqualf(t, reflect.TypeOf(""), method.Type.In(parameter), "%s accepts caller SQL", method.Name)
			}
		}
	}
}

func TestCompilerRejectsRawBusinessDatabaseCapabilities(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, testCase := range []struct {
		name, source, marker string
	}{
		{
			name: "nested internal import",
			source: `package capabilitycompile
import _ "github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
`,
			marker: "use of internal package",
		},
		{
			name: "removed raw manager API",
			source: `package capabilitycompile
import authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
var _ = authorizedexecute.NewManager
`,
			marker: "undefined: authorizedexecute.NewManager",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory, err := os.MkdirTemp(root, "capabilitycompile")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
			require.NoError(t, os.WriteFile(filepath.Join(directory, "illegal.go"), []byte(testCase.source), 0o600))
			relative, err := filepath.Rel(root, directory)
			require.NoError(t, err)
			command := exec.Command("go", "test", "./"+filepath.ToSlash(relative))
			command.Dir = root
			output, err := command.CombinedOutput()
			require.Error(t, err, string(output))
			require.Contains(t, string(output), testCase.marker)
		})
	}
}

func TestSQLSinkCallsitesMatchManifest(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	manifestBytes, err := os.ReadFile(filepath.Join(filepath.Dir(current), "callsite_manifest.txt"))
	require.NoError(t, err)
	allowed := make([]string, 0)
	for _, line := range strings.Split(string(manifestBytes), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allowed = append(allowed, line)
		}
	}
	sinks := map[string]bool{
		"Query": true, "QueryContext": true, "Exec": true, "ExecContext": true,
		"Prepare": true, "PrepareContext": true, "Raw": true, "Conn": true,
		"OpenDB": true, "NewConnector": true,
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if vanishedGoBuildPath(root, path, walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".gocache" || entry.Name() == ".design" || strings.HasPrefix(entry.Name(), "go-build")) {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		set := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(set, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		permitted := false
		for _, prefix := range allowed {
			if relative == prefix || strings.HasPrefix(relative, prefix) {
				permitted = true
				break
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !sinks[selector.Sel.Name] {
				return true
			}
			// net/url Query() and other zero-argument accessors are not SQL sinks.
			if selector.Sel.Name == "Query" && len(call.Args) == 0 {
				return true
			}
			if selector.Sel.Name == "Query" || selector.Sel.Name == "QueryContext" ||
				selector.Sel.Name == "Exec" || selector.Sel.Name == "ExecContext" ||
				selector.Sel.Name == "Prepare" || selector.Sel.Name == "PrepareContext" {
				hasSQL := false
				for _, argument := range call.Args {
					if expressionLooksLikeSQL(argument) {
						hasSQL = true
						break
					}
				}
				if !hasSQL {
					return true
				}
			}
			require.Truef(t, permitted, "unmanifested SQL-capable callsite %s:%d %s", relative, set.Position(call.Pos()).Line, selector.Sel.Name)
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

func vanishedGoBuildPath(root, path string, err error) bool {
	if !os.IsNotExist(err) {
		return false
	}
	relative, relErr := filepath.Rel(root, path)
	return relErr == nil && strings.HasPrefix(filepath.ToSlash(relative), "go-build")
}

func expressionLooksLikeSQL(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.BasicLit:
		return value.Kind == token.STRING
	case *ast.Ident:
		name := strings.ToLower(value.Name)
		return strings.Contains(name, "sql") || strings.Contains(name, "query") || strings.Contains(name, "statement")
	case *ast.SelectorExpr:
		name := strings.ToLower(value.Sel.Name)
		return name == "sql" || name == "rawsql" || name == "query" || name == "statement"
	case *ast.BinaryExpr:
		return expressionLooksLikeSQL(value.X) || expressionLooksLikeSQL(value.Y)
	case *ast.CallExpr:
		for _, argument := range value.Args {
			if expressionLooksLikeSQL(argument) {
				return true
			}
		}
	}
	return false
}
