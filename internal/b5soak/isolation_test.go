//go:build agentsql_b5_soak

package b5soak

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSoakDriverIsNotWiredIntoProductServers(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate package")
	}
	packageDir := filepath.Dir(current)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	allowedCommand := filepath.Join(repoRoot, "cmd", "agentsql-soak")
	importPath := "github.com/cuipengdba/agentsql/internal/b5soak"
	var references []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == packageDir || path == allowedCommand || entry.Name() == ".git" || entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), "go-build") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), importPath) {
			references = append(references, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 0 {
		t.Fatalf("soak driver imported by product path: %v", references)
	}
}
