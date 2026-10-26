package bootstrap

import (
	"context"
	"time"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

// DeleteSQLBotFile fences an owner's published import in the audited control
// store before removing its physical tables and column policies. A failed or
// interrupted drop remains recoverable at the next managed-file startup.
func (runtime *Runtime) DeleteSQLBotFile(ctx context.Context, principal rbac.Principal, binding store.SQLBotBinding,
	operationID string, physical ManagedFilePhysical,
) error {
	if runtime == nil || runtime.Store == nil || ctx == nil || physical == nil || operationID == "" ||
		principal.UserID == "" || principal.UserID != binding.AgentSQLUserID ||
		principal.TenantID == "" || principal.TenantID != binding.TenantID ||
		!principal.Has(rbac.PermissionDatasourceManage) || !principal.Has(rbac.PermissionStrategyManage) ||
		binding.DatasourceID != physical.ReadDatasourceID() || binding.SchemaName != "agentsql_files" {
		return ErrManagedFileStage
	}
	tenantCtx, err := store.WithTenant(ctx, principal.TenantID)
	if err != nil {
		return ErrManagedFileStage
	}
	current, err := runtime.Store.SQLBotBindings().ValidateRuntime(tenantCtx, binding.ID, binding.Revision, binding.AgentID, principal.UserID)
	if err != nil || current.DatasourceID != physical.ReadDatasourceID() || current.SchemaName != "agentsql_files" {
		return ErrManagedFileStage
	}
	datasource, err := runtime.Store.Datasources().Get(tenantCtx, current.DatasourceID)
	if err != nil || !physical.MatchesReadDatasource(datasource) {
		return ErrManagedFileStage
	}
	journal := runtime.Store.ManagedFileImports()
	item, err := journal.Get(tenantCtx, operationID)
	if err != nil || item.TenantID != principal.TenantID || item.BindingID != current.ID ||
		item.BindingRevision != current.Revision || item.WorkspaceID != current.WorkspaceID ||
		item.OwnerUserID != principal.UserID || item.AgentID != current.AgentID ||
		item.DatasourceID != current.DatasourceID || item.SheetCount < 1 || item.SheetCount > 8 {
		return ErrManagedFileStage
	}
	if item.Status == "deleted" {
		return nil
	}
	if item.Status == "published" {
		item, err = journal.Advance(tenantCtx, item.ID, item.Revision, "published", "deleting", principal.UserID)
		if err != nil {
			return err
		}
	} else if item.Status != "deleting" {
		return ErrManagedFileStage
	}
	// The durable deleting state prevents future gateway reads even if the
	// HTTP client disconnects after the management audit commits.
	recovery, cancel := context.WithTimeout(context.WithoutCancel(tenantCtx), 30*time.Second)
	defer cancel()
	return runtime.cleanupOneUnpublishedSQLBotFile(recovery, item, physical)
}
