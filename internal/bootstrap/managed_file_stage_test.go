package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type managedFilePhysicalProbe struct {
	read      model.Datasource
	operation string
	stages    int
	drops     int
	stageErr  error
	dropErr   error
}

func (probe *managedFilePhysicalProbe) ReadDatasourceID() string { return probe.read.ID }
func (probe *managedFilePhysicalProbe) MatchesReadDatasource(value model.Datasource) bool {
	return value.ID == probe.read.ID && value.Host == probe.read.Host && value.Database == probe.read.Database
}
func (*managedFilePhysicalProbe) ExpectedTableName(operation string, sheet int) (string, error) {
	id, err := managedfile.ParseOperationID(operation)
	if err != nil {
		return "", err
	}
	return managedfile.TableName(id, sheet)
}
func (probe *managedFilePhysicalProbe) Stage(_ context.Context, operation string, sheets []executor.ManagedFileSheet) ([]executor.ManagedFileTable, error) {
	probe.operation = operation
	probe.stages++
	if probe.stageErr != nil {
		return nil, probe.stageErr
	}
	name, err := probe.ExpectedTableName(operation, 0)
	if err != nil {
		return nil, err
	}
	return []executor.ManagedFileTable{{Schema: "agentsql_files", Name: name, Rows: len(sheets[0].Rows)}}, nil
}
func (probe *managedFilePhysicalProbe) Drop(_ context.Context, operation string, _ int) error {
	probe.operation = operation
	probe.drops++
	return probe.dropErr
}

func TestManagedFileRecoveryFencesAndRetriesOnlyUnpublishedTables(t *testing.T) {
	runtime, principal, binding, physical := managedFileStageFixture(t)
	ctx := context.Background()
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sheets := []executor.ManagedFileSheet{{Columns: []executor.ManagedFileColumn{{Name: "amount", Type: "int"}}, Rows: [][]any{{int64(10)}}}}
	staged, _, err := runtime.StageSQLBotFile(ctx, principal, binding, digest, 16, sheets, physical)
	require.NoError(t, err)
	physical.dropErr = errors.New("temporary database failure")
	count, err := runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.ErrorContains(t, err, "temporary database failure")
	require.Zero(t, count)
	current, err := runtime.Store.ManagedFileImports().Get(ctx, staged.ID)
	require.NoError(t, err)
	require.Equal(t, "cleanup_needed", current.Status)
	physical.dropErr = nil
	count, err = runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	current, err = runtime.Store.ManagedFileImports().Get(ctx, staged.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", current.Status)
	count, err = runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.NoError(t, err)
	require.Zero(t, count)

	preparing, err := runtime.Store.ManagedFileImports().Begin(ctx, binding, digest, 16, 1, principal.UserID)
	require.NoError(t, err)
	physical.read.Host = "changed-host"
	count, err = runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.ErrorIs(t, err, ErrManagedFileStage)
	require.Zero(t, count)
	physical.read.Host = "127.0.0.1"
	count, err = runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	current, err = runtime.Store.ManagedFileImports().Get(ctx, preparing.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", current.Status)
}

func managedFileStageFixture(t *testing.T) (*Runtime, rbac.Principal, store.SQLBotBinding, *managedFilePhysicalProbe) {
	t.Helper()
	ctx := context.Background()
	s, err := store.OpenWithSecret(ctx, filepath.Join(t.TempDir(), "control.db"), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = s.Agents().Create(ctx, model.Agent{ID: "managed-agent", Name: "managed-agent", Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	_, err = s.Datasources().Create(ctx, model.Datasource{ID: "managed-read", Name: "managed-read", DBType: "postgres", Host: "127.0.0.1", Port: 5432,
		Database: "managed", Username: "file_reader", TLSMode: "disable", ConnLimit: 2, StmtTimeoutMS: 10000, RowLimit: 100}, "reader-password")
	require.NoError(t, err)
	_, err = s.RBAC().CreateUser(ctx, model.User{ID: "managed-human", TenantID: store.DefaultTenantID, Username: "managed-human", DisplayName: "Managed Human", PasswordHash: "hash", Status: "active"})
	require.NoError(t, err)
	binding, _, err := s.SQLBotBindings().Create(ctx, store.SQLBotBinding{WorkspaceID: "1", DatasourceName: "Managed", SQLBotUserID: "1",
		AgentSQLUserID: "managed-human", AgentID: "managed-agent", DatasourceID: "managed-read", SchemaName: "agentsql_files", Enabled: true}, "operator")
	require.NoError(t, err)
	reader, err := s.Datasources().Get(ctx, "managed-read")
	require.NoError(t, err)
	principal := rbac.Principal{UserID: "managed-human", TenantID: store.DefaultTenantID,
		Permissions: []string{rbac.PermissionDatasourceManage, rbac.PermissionStrategyManage, rbac.PermissionQueryExecute}}
	return &Runtime{Store: s}, principal, binding, &managedFilePhysicalProbe{read: reader}
}

func TestManagedFileStagingJournalBeforePhysicalAndCleansFailure(t *testing.T) {
	runtime, principal, binding, physical := managedFileStageFixture(t)
	ctx := context.Background()
	sheets := []executor.ManagedFileSheet{{Columns: []executor.ManagedFileColumn{{Name: "amount", Type: "int"}}, Rows: [][]any{{int64(10)}}}}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	staged, tables, err := runtime.StageSQLBotFile(ctx, principal, binding, digest, 16, sheets, physical)
	require.NoError(t, err)
	require.Equal(t, "staged", staged.Status)
	require.Len(t, tables, 1)
	require.Equal(t, 1, physical.stages)
	require.Zero(t, physical.drops)
	require.Equal(t, staged.ID, physical.operation)

	runtime, principal, binding, physical = managedFileStageFixture(t)
	physical.stageErr = errors.New("injected physical failure")
	_, _, err = runtime.StageSQLBotFile(ctx, principal, binding, digest, 16, sheets, physical)
	require.ErrorContains(t, err, "injected physical failure")
	require.Equal(t, 1, physical.stages)
	require.Equal(t, 1, physical.drops)
	failed, err := runtime.Store.ManagedFileImports().Get(ctx, physical.operation)
	require.NoError(t, err)
	require.Equal(t, "deleted", failed.Status)

	principal.Permissions = []string{rbac.PermissionQueryExecute}
	_, _, err = runtime.StageSQLBotFile(ctx, principal, binding, digest, 16, sheets, physical)
	require.ErrorIs(t, err, ErrManagedFileStage)
	require.Equal(t, 1, physical.stages)
	physical.stageErr = nil
	physical.read.Host = "other-host"
	_, _, err = runtime.StageSQLBotFile(ctx, rbac.Principal{UserID: binding.AgentSQLUserID, TenantID: store.DefaultTenantID,
		Permissions: []string{rbac.PermissionDatasourceManage, rbac.PermissionStrategyManage, rbac.PermissionQueryExecute}}, binding, digest, 16, sheets, physical)
	require.ErrorIs(t, err, ErrManagedFileStage)
	require.Equal(t, 1, physical.stages)
}
