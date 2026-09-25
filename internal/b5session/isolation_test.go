package b5session

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFeatureOffPackageHasOnlyReviewedS6Reference(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate package")
	}
	packageDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	importPath := "github.com/cuipengdba/agentsql/internal/b5session"
	coordinatorGate := filepath.Clean(filepath.Join(repoRoot, "internal", "b5coordinator", "session.go"))
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
		if strings.Contains(string(contents), importPath) && filepath.Clean(path) != coordinatorGate {
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
