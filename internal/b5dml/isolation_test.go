package b5dml

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFeatureOffPackageHasOnlyS5bBinderReference(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	packageDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	importPath := "github.com/cuipengdba/agentsql/internal/b5dml"
	allowed := filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "postgres_dml_binder.go"))
	var references []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == packageDir || entry.Name() == ".git" || strings.HasPrefix(entry.Name(), "go-build") {
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
		if strings.Contains(string(contents), importPath) && filepath.Clean(path) != allowed {
			references = append(references, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 0 {
		t.Fatalf("feature-off package imported outside the isolated S5b binder: %v", references)
	}
}
