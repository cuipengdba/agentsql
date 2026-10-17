package businessdb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnIdentityInspectorRealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	datasource := model.Datasource{ID: "identity-e2e", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "reader", ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	inspector, err := NewMySQLColumnIdentityInspector(ctx, datasource, "identity", "")
	require.NoError(t, err)
	defer inspector.Close()
	fact, err := inspector.InspectTable(ctx, "app", "customers")
	require.NoError(t, err)
	require.Positive(t, fact.TableID)
	require.NotEmpty(t, fact.ServerUUID)
	require.True(t, strings.HasPrefix(fact.ServerVersion, "8.0.") || strings.HasPrefix(fact.ServerVersion, "8.4."))
	_, err = inspector.InspectTable(ctx, "app", "missing")
	require.Error(t, err)
	_, err = inspector.InspectTable(ctx, "app/other", "customers")
	require.Error(t, err)
}

func TestPreparedMySQLColumnSelectRealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	datasource := model.Datasource{ID: "bound-e2e", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "reader", ConnLimit: 1, StmtTimeoutMS: 5000, RowLimit: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	inspector, err := NewMySQLColumnIdentityInspector(ctx, datasource, "identity", "")
	require.NoError(t, err)
	defer inspector.Close()
	executor, err := NewMySQLExecutor(ctx, datasource, "", true)
	require.NoError(t, err)
	defer executor.Close()
	query := "SELECT id, phone FROM app.customers WHERE id = 1 ORDER BY customers.id LIMIT 2"
	shape, err := parser.ParseMySQLColumnSelectShape(query)
	require.NoError(t, err)
	prepared, err := executor.PrepareMySQLColumnSelect(ctx, query, shape, inspector)
	require.NoError(t, err)
	catalog := prepared.Catalog()
	require.Equal(t, "app", catalog.Schema)
	require.Equal(t, "customers", catalog.Table)
	require.Positive(t, catalog.TableID)
	require.NotEmpty(t, catalog.Fingerprint)
	require.GreaterOrEqual(t, len(catalog.Columns), 2)
	result, err := prepared.Execute(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"id", "phone"}, result.Columns)
	require.Len(t, result.Rows, 1)
	post, err := prepared.VerifyPost(ctx)
	require.NoError(t, err)
	require.Equal(t, catalog.Fingerprint, post)
	require.NoError(t, prepared.Close(ctx, true))
}

func TestPreparedMySQLColumnMDLAndRebuildRealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	rootPassword, present := os.LookupEnv("MYSQL_ROOT_PASSWORD")
	if !present {
		t.Skip("set MYSQL_ROOT_PASSWORD for the isolated DDL race fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rootDatasource := model.Datasource{ID: "identity-root", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "root", ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 100}
	root, err := NewMySQLExecutor(ctx, rootDatasource, rootPassword, false)
	require.NoError(t, err)
	defer root.Close()
	readerDatasource := rootDatasource
	readerDatasource.ID, readerDatasource.Username = "identity-reader", "reader"
	reader, err := NewMySQLExecutor(ctx, readerDatasource, "", true)
	require.NoError(t, err)
	defer reader.Close()
	inspector, err := NewMySQLColumnIdentityInspector(ctx, readerDatasource, "identity", "")
	require.NoError(t, err)
	defer inspector.Close()
	_, err = root.Execute(ctx, "DROP TABLE IF EXISTS app.agentsql_identity_rebuild")
	require.NoError(t, err)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = root.Execute(cleanup, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'identity'@'%'")
		_, _ = root.Execute(cleanup, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'reader'@'localhost'")
		_, _ = root.Execute(cleanup, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'reader'@'127.0.0.1'")
		_, _ = root.Execute(cleanup, "DROP TABLE IF EXISTS app.agentsql_identity_rebuild")
	}()
	_, err = root.Execute(ctx, "CREATE TABLE app.agentsql_identity_rebuild (id BIGINT PRIMARY KEY) ENGINE=InnoDB")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "GRANT SELECT ON app.agentsql_identity_rebuild TO 'reader'@'localhost'")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "GRANT SELECT ON app.agentsql_identity_rebuild TO 'reader'@'127.0.0.1'")
	require.NoError(t, err)
	query := "SELECT id FROM app.agentsql_identity_rebuild LIMIT 1"
	shape, err := parser.ParseMySQLColumnSelectShape(query)
	require.NoError(t, err)
	prepared, err := reader.PrepareMySQLColumnSelect(ctx, query, shape, inspector)
	require.NoError(t, err)
	defer prepared.Close(context.Background(), false)
	first := prepared.Catalog()
	require.Positive(t, first.TableID)
	ddlDone := make(chan error, 1)
	go func() {
		_, ddlErr := root.Execute(ctx, "ALTER TABLE app.agentsql_identity_rebuild ADD COLUMN mdl_probe INT NULL")
		ddlDone <- ddlErr
	}()
	select {
	case ddlErr := <-ddlDone:
		t.Fatalf("DDL escaped the held metadata lock: %v", ddlErr)
	case <-time.After(300 * time.Millisecond):
	}
	result, err := prepared.Execute(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, result.Rows)
	require.NoError(t, prepared.Close(ctx, true))
	select {
	case ddlErr := <-ddlDone:
		require.NoError(t, ddlErr)
	case <-ctx.Done():
		t.Fatal("DDL did not resume after releasing the metadata lock")
	}
	changed, err := reader.PrepareMySQLColumnSelect(ctx, query, shape, inspector)
	require.NoError(t, err)
	defer changed.Close(context.Background(), false)
	require.NotEqual(t, first.Fingerprint, changed.Catalog().Fingerprint,
		"a column change must invalidate the enrolled catalog")
	require.NoError(t, changed.Close(ctx, false))
	_, err = root.Execute(ctx, "DROP TABLE app.agentsql_identity_rebuild")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "CREATE TABLE app.agentsql_identity_rebuild (id BIGINT PRIMARY KEY) ENGINE=InnoDB")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "GRANT SELECT ON app.agentsql_identity_rebuild TO 'reader'@'localhost'")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "GRANT SELECT ON app.agentsql_identity_rebuild TO 'reader'@'127.0.0.1'")
	require.NoError(t, err)
	second, err := reader.PrepareMySQLColumnSelect(ctx, query, shape, inspector)
	require.NoError(t, err)
	defer second.Close(context.Background(), false)
	require.NotEqual(t, first.TableID, second.Catalog().TableID)
	require.NotEqual(t, first.Fingerprint, second.Catalog().Fingerprint)
	require.NoError(t, second.Close(ctx, false))
	_, err = root.Execute(ctx, "GRANT SELECT ON app.agentsql_identity_rebuild TO 'identity'@'%'")
	require.NoError(t, err)
	_, err = inspector.InspectTable(ctx, "app", "agentsql_identity_rebuild")
	require.Error(t, err, "metadata identity account must have PROCESS and USAGE only")
	_, err = root.Execute(ctx, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'identity'@'%'")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'reader'@'localhost'")
	require.NoError(t, err)
	_, err = root.Execute(ctx, "REVOKE SELECT ON app.agentsql_identity_rebuild FROM 'reader'@'127.0.0.1'")
	require.NoError(t, err)
	revokedReader, err := NewMySQLExecutor(ctx, readerDatasource, "", true)
	require.NoError(t, err)
	defer revokedReader.Close()
	_, err = revokedReader.PrepareMySQLColumnSelect(ctx, query, shape, inspector)
	require.Error(t, err, "revoked business SELECT must fail before execution")
}
