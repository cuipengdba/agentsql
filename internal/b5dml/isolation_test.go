package b5dml

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFeatureOffPackageHasOnlyReviewedBinderReferences(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	packageDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	importPath := "github.com/cuipengdba/agentsql/internal/b5dml"
	allowedFiles := map[string]bool{
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "postgres_dml_binder.go")):         true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "b5_coordinator.go")):              true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "easy_deploy_binder_contract.go")): true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "easy_deploy_catalog_kernel.go")):  true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "easy_deploy_native_adapter.go")):  true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "internal", "businessdb", "easy_deploy_closed_dml.go")):      true,
		filepath.Clean(filepath.Join(repoRoot, "internal", "authorizedexecute", "b5_transaction.go")):                                        true,
	}
	coordinatorDir := filepath.Clean(filepath.Join(repoRoot, "internal", "b5coordinator"))
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
		clean := filepath.Clean(path)
		inCoordinator := strings.HasPrefix(clean, coordinatorDir+string(filepath.Separator))
		if strings.Contains(string(contents), importPath) && !allowedFiles[clean] && !inCoordinator {
			references = append(references, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 0 {
		t.Fatalf("feature-off package imported outside the reviewed binder boundary: %v", references)
	}
}
