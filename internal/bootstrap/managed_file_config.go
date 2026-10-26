package bootstrap

import (
	"context"
	"errors"
	"os"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
)

// ConfigureManagedFiles constructs the one server-owned provisioning role and
// reconciles never-published tables before the HTTP import route is exposed.
// The writer credential is not registered as an Agent datasource.
func (runtime *Runtime) ConfigureManagedFiles(ctx context.Context, cfg config.ManagedFileConfig) (*executor.ManagedFileProvisioner, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if runtime == nil || runtime.Store == nil || ctx == nil || cfg.TenantID == "" || cfg.ReadDatasourceID == "" ||
		cfg.ProvisionerUsername == "" || cfg.ProvisionerPasswordEnv == "" {
		return nil, ErrManagedFileStage
	}
	tenantCtx, err := store.WithTenant(ctx, cfg.TenantID)
	if err != nil {
		return nil, ErrManagedFileStage
	}
	reader, err := runtime.Store.Datasources().Get(tenantCtx, cfg.ReadDatasourceID)
	if err != nil || reader.DBType != "postgres" || reader.TenantID != cfg.TenantID {
		return nil, ErrManagedFileStage
	}
	password, present := os.LookupEnv(cfg.ProvisionerPasswordEnv)
	if !present || password == "" {
		return nil, errors.New("managed file provisioner credential is unavailable")
	}
	writer := reader
	writer.ID = "__managed_file_provisioner__" + reader.ID
	writer.Name = "managed file provisioner"
	writer.Username = cfg.ProvisionerUsername
	writer.PasswordEnc = ""
	physical, err := executor.NewManagedFileProvisioner(reader, writer, password)
	if err != nil {
		return nil, err
	}
	if _, err := runtime.RecoverUnpublishedSQLBotFiles(tenantCtx, cfg.TenantID, physical); err != nil {
		return nil, err
	}
	return physical, nil
}
