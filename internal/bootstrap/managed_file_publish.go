package bootstrap

import (
	"context"
	"errors"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

// PostgreSQL identifier quoting here only formats names that the physical
// capability already validated and created. It does not open a DB connection.
func quoteManagedIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// PublishSQLBotFile runs the physical grant, formal column enrollment and
// audited visibility transition for one already staged operation. No client
// supplied SQL or relation name reaches the binder. Failure leaves a durable
// cleanup state; interrupted attempts are removed by startup recovery.
func (runtime *Runtime) PublishSQLBotFile(ctx context.Context, principal rbac.Principal, binding store.SQLBotBinding,
	staged store.ManagedFileImport, tables []executor.ManagedFileTable, sheets []executor.ManagedFileSheet,
	physical ManagedFilePhysical,
) (store.ManagedFileImport, error) {
	if runtime == nil || runtime.Store == nil || runtime.business == nil || ctx == nil || physical == nil ||
		staged.ID == "" || staged.Status != "staged" || staged.TenantID != principal.TenantID ||
		staged.OwnerUserID != principal.UserID || staged.BindingID != binding.ID || staged.BindingRevision != binding.Revision ||
		staged.AgentID != binding.AgentID || staged.DatasourceID != binding.DatasourceID ||
		principal.UserID != binding.AgentSQLUserID || principal.TenantID != binding.TenantID ||
		!principal.Has(rbac.PermissionDatasourceManage) || !principal.Has(rbac.PermissionStrategyManage) ||
		!principal.Has(rbac.PermissionQueryExecute) || len(tables) != staged.SheetCount || len(sheets) != staged.SheetCount ||
		staged.SheetCount < 1 || staged.SheetCount > 8 || physical.ReadDatasourceID() != staged.DatasourceID {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	tenantCtx, err := store.WithTenant(ctx, principal.TenantID)
	if err != nil {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	current, err := runtime.Store.SQLBotBindings().ValidateRuntime(tenantCtx, binding.ID, binding.Revision, binding.AgentID, principal.UserID)
	if err != nil || current.SchemaName != "agentsql_files" || current.DatasourceID != staged.DatasourceID ||
		current.WorkspaceID != staged.WorkspaceID {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	datasource, err := runtime.Store.Datasources().Get(tenantCtx, current.DatasourceID)
	if err != nil || !physical.MatchesReadDatasource(datasource) || datasource.DBType != "postgres" {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	operation, err := managedfile.ParseOperationID(staged.ID)
	if err != nil {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	for index, table := range tables {
		name, nameErr := managedfile.TableName(operation, index)
		if nameErr != nil || table.Schema != "agentsql_files" || table.Name != name || table.Rows != len(sheets[index].Rows) ||
			len(sheets[index].Columns) == 0 || len(sheets[index].Columns) > 32 {
			return store.ManagedFileImport{}, ErrManagedFileStage
		}
	}
	latest, err := runtime.Store.ManagedFileImports().Get(tenantCtx, staged.ID)
	if err != nil || latest.Status != "staged" || latest.Revision != staged.Revision ||
		latest.TenantID != staged.TenantID || latest.BindingID != staged.BindingID || latest.BindingRevision != staged.BindingRevision ||
		latest.OwnerUserID != staged.OwnerUserID || latest.AgentID != staged.AgentID ||
		latest.DatasourceID != staged.DatasourceID || latest.SheetCount != staged.SheetCount {
		return store.ManagedFileImport{}, ErrManagedFileStage
	}
	cleanup := func(cause error) (store.ManagedFileImport, error) {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(tenantCtx), 30*time.Second)
		defer cancel()
		latest, getErr := runtime.Store.ManagedFileImports().Get(recovery, staged.ID)
		if getErr == nil && latest.Status != "published" && latest.Status != "deleted" {
			getErr = runtime.cleanupOneUnpublishedSQLBotFile(recovery, latest, physical)
		}
		return store.ManagedFileImport{}, errors.Join(cause, getErr)
	}
	if err := physical.GrantRead(tenantCtx, staged.ID, staged.SheetCount); err != nil {
		return cleanup(err)
	}
	for index, sheet := range sheets {
		columnNames := make([]string, len(sheet.Columns))
		quoted := make([]string, len(sheet.Columns))
		for columnIndex, column := range sheet.Columns {
			if column.Name == "" || strings.ContainsAny(column.Name, ",\x00") || strings.TrimSpace(column.Name) != column.Name {
				return cleanup(ErrManagedFileStage)
			}
			columnNames[columnIndex] = column.Name
			quoted[columnIndex] = quoteManagedIdentifier(column.Name)
		}
		policyID, idErr := managedfile.PolicyID(operation, index)
		if idErr != nil {
			return cleanup(idErr)
		}
		columns := strings.Join(columnNames, ",")
		policy, err := runtime.Store.Policies().Create(tenantCtx, model.Policy{ID: policyID, AgentID: staged.AgentID,
			DatasourceID: staged.DatasourceID, ObjectType: "column", ObjectName: "agentsql_files." + tables[index].Name,
			Columns: &columns, Action: "allow"})
		if err != nil {
			return cleanup(err)
		}
		query := "SELECT " + strings.Join(quoted, ",") + " FROM " + quoteManagedIdentifier("agentsql_files") + "." + quoteManagedIdentifier(tables[index].Name)
		if _, err := runtime.BindPostgresColumnPolicy(tenantCtx, policyID, policy.Revision, query); err != nil {
			return cleanup(err)
		}
	}
	ready, err := runtime.Store.ManagedFileImports().MarkPolicyReady(tenantCtx, staged.ID, staged.Revision, principal.UserID)
	if err != nil {
		return cleanup(err)
	}
	current, err = runtime.Store.SQLBotBindings().ValidateRuntime(tenantCtx, binding.ID, binding.Revision, binding.AgentID, principal.UserID)
	if err != nil || current.DatasourceID != staged.DatasourceID || current.SchemaName != "agentsql_files" {
		return cleanup(ErrManagedFileStage)
	}
	datasource, err = runtime.Store.Datasources().Get(tenantCtx, current.DatasourceID)
	if err != nil || !physical.MatchesReadDatasource(datasource) {
		return cleanup(ErrManagedFileStage)
	}
	if err := physical.GrantRead(tenantCtx, staged.ID, staged.SheetCount); err != nil {
		return cleanup(err)
	}
	// Publish rechecks the binding and formal policy evidence in its own
	// transaction. If its commit is ambiguous, cleanup first reads the journal
	// and never drops a successfully published operation.
	published, err := runtime.Store.ManagedFileImports().Publish(tenantCtx, ready.ID, ready.Revision, principal.UserID)
	if err != nil {
		return cleanup(err)
	}
	return published, nil
}
