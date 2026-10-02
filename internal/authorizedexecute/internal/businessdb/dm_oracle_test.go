package businessdb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBuildDMDSNHandlesDriverCredentialRules(t *testing.T) {
	datasource := model.Datasource{
		Host: "127.0.0.1", Port: 5236, Database: "APP", Username: "user@tenant",
		StmtTimeoutMS: 5_000,
	}
	dsn, err := buildDMDSN(datasource, "p@ss:/word")
	require.NoError(t, err)
	require.Equal(t, "dm://user@tenant:p@ss:/word@127.0.0.1:5236?schema=APP&connectTimeout=5000", dsn)

	datasource.Username = "user:name"
	_, err = buildDMDSN(datasource, "password")
	require.Error(t, err)
	datasource.Username = "user"
	_, err = buildDMDSN(datasource, "ambiguous?password")
	require.Error(t, err)
}

func TestOracleStyleSchemaQueriesUseBinds(t *testing.T) {
	tables := []SchemaTable{
		{Table: "CUSTOMERS"},
		{Schema: "REPORTING", Table: "DAILY_TOTALS"},
	}
	dmQuery, dmArgs := dmSchemaQuery("APP", tables)
	require.NotContains(t, dmQuery, "CUSTOMERS")
	require.Equal(t, []any{"APP", "CUSTOMERS", "REPORTING", "DAILY_TOTALS"}, dmArgs)
	require.Equal(t, 4, strings.Count(dmQuery, "?"))

	oracleQuery, oracleArgs := oracleSchemaQuery("APP", tables)
	require.NotContains(t, oracleQuery, "DAILY_TOTALS")
	require.Contains(t, oracleQuery, "OWNER=:1")
	require.Contains(t, oracleQuery, "TABLE_NAME=:4")
	require.Equal(t, dmArgs, oracleArgs)
}

func TestLimitedDialectSQLFailsClosed(t *testing.T) {
	executor := &limitedSQLExecutor{dialect: "oracle", readOnly: false}
	_, err := executor.Query(context.Background(), "SELECT 1 FROM DUAL", 1)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageQuery, databaseError.Stage)
	require.Equal(t, DBErrorCodeExecution, databaseError.Code)

	executor.readOnly = true
	_, err = executor.Execute(context.Background(), "DELETE FROM T")
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeReadOnly, databaseError.Code)
}

func TestDMDiscoveryE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_DM_E2E") != "1" {
		t.Skip("set AGENTSQL_DM_E2E=1 to run against DM8")
	}
	datasource := model.Datasource{
		ID: "dm-e2e", DBType: "dm", Host: envOrDefault("DM_HOST", "127.0.0.1"),
		Port: 5236, Database: envOrDefault("DM_SCHEMA", "SYSDBA"),
		Username: envOrDefault("DM_USER", "SYSDBA"), ConnLimit: 1, StmtTimeoutMS: 15_000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := NewDMExecutor(ctx, datasource, os.Getenv("DM_PASSWORD"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.NoError(t, executor.Ping(ctx))
	session, err := executor.OpenSession(ctx, "dm-e2e-session")
	require.NoError(t, err)
	require.NoError(t, session.Close())
	columns, err := ListSchema(ctx, executor, datasource.Database, []SchemaTable{{Schema: "SYS", Table: "ALL_TAB_COLUMNS"}})
	require.NoError(t, err)
	require.NotEmpty(t, columns)
}

func TestOracleDiscoveryE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_ORACLE_E2E") != "1" {
		t.Skip("set AGENTSQL_ORACLE_E2E=1 to run against Oracle")
	}
	datasource := model.Datasource{
		ID: "oracle-e2e", DBType: "oracle", Host: envOrDefault("ORACLE_HOST", "127.0.0.1"),
		Port: 1521, Database: envOrDefault("ORACLE_SERVICE", "FREEPDB1"),
		Username: envOrDefault("ORACLE_USER", "SYSTEM"), ConnLimit: 1, StmtTimeoutMS: 15_000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := NewOracleExecutor(ctx, datasource, os.Getenv("ORACLE_PASSWORD"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.NoError(t, executor.Ping(ctx))
	session, err := executor.OpenSession(ctx, "oracle-e2e-session")
	require.NoError(t, err)
	require.NoError(t, session.Close())
	columns, err := ListSchema(ctx, executor, datasource.Database, []SchemaTable{{Schema: "SYS", Table: "DUAL"}})
	require.NoError(t, err)
	require.NotEmpty(t, columns)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
