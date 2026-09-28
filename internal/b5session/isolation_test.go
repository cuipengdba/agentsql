package b5session

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFeatureOffPackageHasOnlyReviewedReferences(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate package")
	}
	packageDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	importPath := "github.com/cuipengdba/agentsql/internal/b5session"
	reviewed := map[string]bool{
		filepath.Clean(filepath.Join(repoRoot, "internal", "b5coordinator", "session.go")): true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "mcpserver", "b5_service.go")):  true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "bootstrap", "b5_runtime.go")):  true,
	}
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
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(contents), importPath) && !reviewed[filepath.Clean(path)] {
			references = append(references, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 0 {
		t.Fatalf("feature-off package imported by production files: %v", references)
	}
}
