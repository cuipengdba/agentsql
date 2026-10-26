package bootstrap

import (
	"context"
	"errors"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

var ErrManagedFileStage = errors.New("managed file staging is unavailable")

// ManagedFilePhysical is the narrowly configured managed PostgreSQL capability.
// It is separate from the ordinary Agent datasource executor.
type ManagedFilePhysical interface {
	ReadDatasourceID() string
	MatchesReadDatasource(model.Datasource) bool
	ExpectedTableName(string, int) (string, error)
	Stage(context.Context, string, []executor.ManagedFileSheet) ([]executor.ManagedFileTable, error)
	Drop(context.Context, string, int) error
}

// StageSQLBotFile couples a durable control-store journal to hidden physical
// tables. It intentionally stops before policy enrollment or SELECT grants.
// Future publication must start from the returned staged revision and recheck
// the user, binding, table identity, policy and audit under a separate gate.
func (runtime *Runtime) StageSQLBotFile(ctx context.Context, principal rbac.Principal, binding store.SQLBotBinding,
	fileSHA256 string, fileBytes int64, sheets []executor.ManagedFileSheet, physical ManagedFilePhysical,
) (store.ManagedFileImport, []executor.ManagedFileTable, error) {
	if runtime == nil || runtime.Store == nil || ctx == nil || physical == nil ||
		principal.UserID == "" || principal.TenantID == "" || principal.UserID != binding.AgentSQLUserID ||
		principal.TenantID != binding.TenantID || !principal.Has(rbac.PermissionDatasourceManage) ||
		!principal.Has(rbac.PermissionStrategyManage) || !principal.Has(rbac.PermissionQueryExecute) ||
		len(sheets) == 0 || len(sheets) > 8 {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	tenantCtx, err := store.WithTenant(ctx, principal.TenantID)
	if err != nil {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	current, err := runtime.Store.SQLBotBindings().ValidateRuntime(tenantCtx, binding.ID, binding.Revision, binding.AgentID, binding.AgentSQLUserID)
	if err != nil || current.DatasourceID != physical.ReadDatasourceID() || current.TenantID != principal.TenantID {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	datasource, err := runtime.Store.Datasources().Get(tenantCtx, current.DatasourceID)
	if err != nil || !physical.MatchesReadDatasource(datasource) {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	journal := runtime.Store.ManagedFileImports()
	operation, err := journal.Begin(tenantCtx, current, fileSHA256, fileBytes, len(sheets), principal.UserID)
	if err != nil {
		return store.ManagedFileImport{}, nil, err
	}
	cleanup := func(cause error) (store.ManagedFileImport, []executor.ManagedFileTable, error) {
		// Client cancellation must not abandon a committed hidden table. A
		// journal still in preparing/cleanup_needed is retryable after restart.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(tenantCtx), 30*time.Second)
		defer cancel()
		marked, markErr := journal.Advance(recovery, operation.ID, operation.Revision, "preparing", "cleanup_needed", principal.UserID)
		dropErr := physical.Drop(recovery, operation.ID, operation.SheetCount)
		if markErr == nil && dropErr == nil {
			_, markErr = journal.Advance(recovery, operation.ID, marked.Revision, "cleanup_needed", "deleted", principal.UserID)
		}
		return store.ManagedFileImport{}, nil, errors.Join(cause, markErr, dropErr)
	}
	tables, err := physical.Stage(tenantCtx, operation.ID, sheets)
	if err != nil || len(tables) != len(sheets) {
		if err == nil {
			err = ErrManagedFileStage
		}
		return cleanup(err)
	}
	for index, table := range tables {
		expected, nameErr := physical.ExpectedTableName(operation.ID, index)
		if nameErr != nil || table.Schema != "agentsql_files" || table.Name != expected || table.Rows != len(sheets[index].Rows) {
			return cleanup(ErrManagedFileStage)
		}
	}
	staged, err := journal.Advance(tenantCtx, operation.ID, operation.Revision, "preparing", "staged", principal.UserID)
	if err != nil {
		return cleanup(err)
	}
	return staged, tables, nil
}
