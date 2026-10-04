package businessdb

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLServer2025ExecutorE2E(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTSQL_SQLSERVER_2025_E2E") != "1" {
		t.Skip("set AGENTSQL_SQLSERVER_2025_E2E=1 and SQL Server connection variables to run")
	}
	port := 1433
	if configured := os.Getenv("AGENTSQL_SQLSERVER_PORT"); configured != "" {
		parsed, err := strconv.Atoi(configured)
		require.NoError(t, err)
		port = parsed
	}
	trust, err := strconv.ParseBool(defaultSQLServerTestEnv("AGENTSQL_SQLSERVER_TRUST_SERVER_CERTIFICATE", "false"))
	require.NoError(t, err)
	datasource := model.Datasource{
		ID: "sqlserver-2025-e2e", Name: "SQL Server 2025 E2E", DBType: "sqlserver",
		Host: requiredSQLServerTestEnv(t, "AGENTSQL_SQLSERVER_HOST"), Port: port,
		Database:               requiredSQLServerTestEnv(t, "AGENTSQL_SQLSERVER_DATABASE"),
		Username:               requiredSQLServerTestEnv(t, "AGENTSQL_SQLSERVER_USERNAME"),
		TLSMode:                defaultSQLServerTestEnv("AGENTSQL_SQLSERVER_TLS_MODE", "strict"),
		TLSServerName:          os.Getenv("AGENTSQL_SQLSERVER_TLS_SERVER_NAME"),
		TLSCAFile:              os.Getenv("AGENTSQL_SQLSERVER_TLS_CA_FILE"),
		TrustServerCertificate: trust, ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := NewSQLServerExecutor(ctx, datasource, requiredSQLServerTestEnv(t, "AGENTSQL_SQLSERVER_PASSWORD"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.NoError(t, executor.Ping(ctx))

	table := "agentsql_sqlserver_2025_e2e"
	_, err = executor.database.ExecContext(ctx, fmt.Sprintf("CREATE TABLE [dbo].[%s] ([id] int NOT NULL, [name] nvarchar(40) NULL)", table))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = executor.database.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE [dbo].[%s]", table))
	})
	_, err = executor.database.ExecContext(ctx, fmt.Sprintf("INSERT INTO [dbo].[%s] ([id],[name]) VALUES (1,N'alpha')", table))
	require.NoError(t, err)

	columns, err := ListSchema(ctx, executor, datasource.Database, []SchemaTable{{Schema: "dbo", Table: table}})
	require.NoError(t, err)
	require.Len(t, columns, 2)
	result, err := Sample(ctx, executor, SchemaTable{Schema: "dbo", Table: table}, []SampleColumn{
		{Schema: "dbo", Table: table, Column: "id"},
		{Schema: "dbo", Table: table, Column: "name"},
	}, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"id", "name"}, result.Columns)
	require.Equal(t, [][]string{{"1", "alpha"}}, result.Rows)
}

func requiredSQLServerTestEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func defaultSQLServerTestEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
