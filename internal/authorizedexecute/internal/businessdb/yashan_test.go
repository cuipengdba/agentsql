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

func TestBuildYashanDSNEscapesDriverDelimiters(t *testing.T) {
	datasource := model.Datasource{
		ID: "yashan-test", DBType: "yashan", Host: "127.0.0.1", Port: 1688,
		Database: "APP", Username: `user/tenant\name`,
	}
	dsn, err := buildYashanDSN(datasource, `p@ss/word\tail`)
	require.NoError(t, err)
	require.Equal(
		t,
		`user\/tenant\\name/p\@ss\/word\\tail@127.0.0.1:1688?compat_vector=yashan`,
		dsn,
	)

	_, err = escapeYashanDSNComponent("bad\ncredential")
	require.Error(t, err)
}

func TestYashanSchemaQueryUsesBindsAndOneBasedOrdinals(t *testing.T) {
	tables := []SchemaTable{
		{Table: "CUSTOMERS"},
		{Schema: "REPORTING", Table: "DAILY_TOTALS"},
	}
	query, args := yashanSchemaQuery("APP", tables)
	require.NotContains(t, query, "CUSTOMERS")
	require.NotContains(t, query, "DAILY_TOTALS")
	require.Equal(t, []any{"APP", "CUSTOMERS", "REPORTING", "DAILY_TOTALS"}, args)
	require.Equal(t, 4, strings.Count(query, "?"))
	require.Contains(t, query, "TO_CHAR(COLUMN_ID + 1)")
}

func TestYashanGeneralSQLFailsClosed(t *testing.T) {
	executor := &YashanExecutor{limitedSQLExecutor: &limitedSQLExecutor{dialect: "yashan"}}
	require.Equal(t, "yashan", executor.Dialect())

	result, err := executor.Query(context.Background(), "SELECT 1 FROM DUAL", 1)
	require.Empty(t, result)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorKindExecution, databaseError.Kind)
	require.Equal(t, DBStageQuery, databaseError.Stage)

	result, err = (&yashanSession{}).Query(context.Background(), "SELECT 1 FROM DUAL", 1)
	require.Empty(t, result)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorKindExecution, databaseError.Kind)
	require.Equal(t, DBStageQuery, databaseError.Stage)

	_, err = executor.Explain(context.Background(), "SELECT 1 FROM DUAL")
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageExplain, databaseError.Stage)
}

func TestYashanDiscoveryE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_YASHAN_E2E") != "1" {
		t.Skip("set AGENTSQL_YASHAN_E2E=1 and build with -tags yashan to run against YashanDB")
	}
	datasource := model.Datasource{
		ID: "yashan-e2e", DBType: "yashan", Host: envOrDefault("YASHAN_HOST", "127.0.0.1"),
		Port: 1688, Database: envOrDefault("YASHAN_SCHEMA", "SYS"),
		Username: envOrDefault("YASHAN_USER", "SYS"), ConnLimit: 1, StmtTimeoutMS: 15_000,
	}
	tableName := envOrDefault("YASHAN_TABLE", "ALL_TAB_COLUMNS")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, err := openExecutor(datasource, os.Getenv("YASHAN_PASSWORD"), true)
	require.NoError(t, err)
	executor, ok := opened.(*YashanExecutor)
	require.True(t, ok)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.Equal(t, "yashan", executor.Dialect())
	require.NoError(t, executor.Ping(ctx))

	session, err := executor.OpenSession(ctx, "yashan-e2e-session")
	require.NoError(t, err)
	result, err := session.Query(ctx, "SELECT 1 FROM DUAL", 1)
	require.Empty(t, result)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorKindExecution, databaseError.Kind)
	require.Equal(t, DBStageQuery, databaseError.Stage)
	require.NoError(t, session.Close())

	columns, err := ListSchema(ctx, executor, datasource.Database, []SchemaTable{{Schema: datasource.Database, Table: tableName}})
	require.NoError(t, err)
	require.NotEmpty(t, columns)
	require.Equal(t, datasource.Database, columns[0].Schema)
	require.Equal(t, tableName, columns[0].Table)
	require.GreaterOrEqual(t, columns[0].Ordinal, 1)

	result, err = executor.Query(ctx, "SELECT 1 FROM DUAL", 1)
	require.Empty(t, result)
	require.Error(t, err)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorKindExecution, databaseError.Kind)
	require.Equal(t, DBStageQuery, databaseError.Stage)

	_, err = executor.Explain(ctx, "SELECT 1 FROM DUAL")
	require.Error(t, err)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageExplain, databaseError.Stage)
}
