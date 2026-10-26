package bootstrap

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagedFileProvisionerConfigurationNeverRegistersWriterAsDatasource(t *testing.T) {
	runtime, _, _, _ := managedFileStageFixture(t)
	cfg := config.ManagedFileConfig{Enabled: true, TenantID: store.DefaultTenantID, ReadDatasourceID: "managed-read",
		ProvisionerUsername: "file_writer", ProvisionerPasswordEnv: "AGENTSQL_TEST_FILE_WRITER_PASSWORD"}
	_, err := runtime.ConfigureManagedFiles(context.Background(), cfg)
	require.Error(t, err)
	t.Setenv(cfg.ProvisionerPasswordEnv, "test-only-secret")
	physical, err := runtime.ConfigureManagedFiles(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, "managed-read", physical.ReadDatasourceID())
	_, err = runtime.Store.Datasources().Get(context.Background(), "__managed_file_provisioner__managed-read")
	require.ErrorIs(t, err, store.ErrNotFound)
}
