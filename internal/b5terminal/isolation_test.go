package b5terminal

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFeatureOffPackageHasNoProductionEntryPointReferences(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	packageDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	dmlContractDir := filepath.Join(repoRoot, "internal", "b5dml")
	typedAdapter := filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "b5_pg_terminal_adapter.go")
	coordinatorAdapter := filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "b5_coordinator.go")
	coordinatorDir := filepath.Join(repoRoot, "internal", "b5coordinator")
	importPath := "github.com/cuipengdba/agentsql/internal/b5terminal"
	var references []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == packageDir || path == dmlContractDir || entry.Name() == ".git" || strings.HasPrefix(entry.Name(), "go-build") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || path == typedAdapter || path == coordinatorAdapter || strings.HasPrefix(path, coordinatorDir+string(filepath.Separator)) {
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
		t.Fatalf("feature-off package imported by production files: %v", references)
	}
}
