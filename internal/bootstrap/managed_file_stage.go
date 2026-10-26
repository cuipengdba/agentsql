package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/managedfile"
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
	GrantRead(context.Context, string, int) error
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
	if err != nil || current.DatasourceID != physical.ReadDatasourceID() || current.TenantID != principal.TenantID || current.SchemaName != "agentsql_files" {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	datasource, err := runtime.Store.Datasources().Get(tenantCtx, current.DatasourceID)
	if err != nil || !physical.MatchesReadDatasource(datasource) {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	journal := runtime.Store.ManagedFileImports()
	selectionBytes, err := json.Marshal(sheets)
	if err != nil {
		return store.ManagedFileImport{}, nil, ErrManagedFileStage
	}
	selectionDigest := sha256.Sum256(selectionBytes)
	operation, err := journal.Begin(tenantCtx, current, fileSHA256, fileBytes, len(sheets), principal.UserID, hex.EncodeToString(selectionDigest[:]))
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
	registered := make([]store.ManagedFileTableRecord, len(tables))
	for index, table := range tables {
		registered[index] = store.ManagedFileTableRecord{Name: table.Name, Rows: table.Rows}
	}
	staged, err := journal.MarkStaged(tenantCtx, operation.ID, operation.Revision, registered, principal.UserID)
	if err != nil {
		return cleanup(err)
	}
	return staged, tables, nil
}

// RecoverUnpublishedSQLBotFiles removes abandoned imports, resumes deletions,
// and cleans up published tables whose binding (or parent) was revoked.
// It never selects an otherwise healthy published operation.
func (runtime *Runtime) RecoverUnpublishedSQLBotFiles(ctx context.Context, tenantID string, physical ManagedFilePhysical) (int, error) {
	if runtime == nil || runtime.Store == nil || ctx == nil || tenantID == "" || physical == nil || physical.ReadDatasourceID() == "" {
		return 0, ErrManagedFileStage
	}
	tenantCtx, err := store.WithTenant(ctx, tenantID)
	if err != nil {
		return 0, ErrManagedFileStage
	}
	datasource, err := runtime.Store.Datasources().Get(tenantCtx, physical.ReadDatasourceID())
	if err != nil || !physical.MatchesReadDatasource(datasource) {
		return 0, ErrManagedFileStage
	}
	journal := runtime.Store.ManagedFileImports()
	cleaned := 0
	for {
		items, err := journal.ListRecoverableByDatasource(tenantCtx, physical.ReadDatasourceID(), 1000)
		if err != nil {
			return cleaned, err
		}
		if len(items) == 0 {
			return cleaned, nil
		}
		for _, item := range items {
			if err := runtime.cleanupOneUnpublishedSQLBotFile(tenantCtx, item, physical); err != nil {
				return cleaned, err
			}
			cleaned++
		}
	}
}

func (runtime *Runtime) cleanupOneUnpublishedSQLBotFile(ctx context.Context, item store.ManagedFileImport, physical ManagedFilePhysical) error {
	journal := runtime.Store.ManagedFileImports()
	if item.Status == "published" {
		var err error
		item, err = journal.Advance(ctx, item.ID, item.Revision, "published", "deleting", item.OwnerUserID)
		if err != nil {
			return err
		}
	}
	if item.Status != "cleanup_needed" {
		var err error
		item, err = journal.Advance(ctx, item.ID, item.Revision, item.Status, "cleanup_needed", item.OwnerUserID)
		if err != nil {
			return err
		}
	}
	if err := physical.Drop(ctx, item.ID, item.SheetCount); err != nil {
		return err
	}
	operation, err := managedfile.ParseOperationID(item.ID)
	if err != nil {
		return err
	}
	for sheet := 0; sheet < item.SheetCount; sheet++ {
		policyID, err := managedfile.PolicyID(operation, sheet)
		if err != nil {
			return err
		}
		tableName, err := managedfile.TableName(operation, sheet)
		if err != nil {
			return err
		}
		policy, err := runtime.Store.Policies().Get(ctx, policyID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil || policy.AgentID != item.AgentID || policy.DatasourceID != item.DatasourceID ||
			policy.ObjectName != "agentsql_files."+tableName {
			return ErrManagedFileStage
		}
		if err := runtime.Store.Policies().DeleteIfRevision(ctx, policyID, policy.Revision); err != nil {
			return err
		}
	}
	_, err = journal.Advance(ctx, item.ID, item.Revision, "cleanup_needed", "deleted", item.OwnerUserID)
	return err
}
