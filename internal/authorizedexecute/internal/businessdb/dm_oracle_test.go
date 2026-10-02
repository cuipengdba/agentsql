package businessdb

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	dm8 "github.com/godoes/gorm-dameng/dm8"
	gooranetwork "github.com/sijms/go-ora/v2/network"
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

func TestLimitedDialectSelectValidatorFailsClosed(t *testing.T) {
	for _, sqlText := range []string{
		"SELECT CUSTOMER_ID, NAME FROM APP.CUSTOMERS WHERE STATUS = 'ACTIVE'",
		`SELECT "MixedCase" FROM "APP"."Customer"`,
		"SELECT LEVEL AS N FROM DUAL CONNECT BY LEVEL <= 3",
	} {
		require.NoError(t, validateLimitedSelect(sqlText), sqlText)
	}
	for _, sqlText := range []string{
		"", "WITH X AS (SELECT 1 FROM DUAL) SELECT * FROM X", "SELECT COUNT(*) FROM T",
		"SELECT * FROM T; DELETE FROM T", "SELECT * INTO BACKUP FROM T", "SELECT SEQ.NEXTVAL FROM DUAL",
		"SELECT * FROM T -- trailing", "UPDATE T SET V=1", "SELECT * FROM T WHERE ID=:1",
	} {
		require.Error(t, validateLimitedSelect(sqlText), sqlText)
	}
}

func TestLimitedDialectPaginationAndKeywordBoundaries(t *testing.T) {
	accepted := []struct {
		dialect string
		sql     string
	}{
		{dialect: "dm", sql: "SELECT TOP 5 ID FROM APP.CUSTOMERS"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS LIMIT 5"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS FETCH FIRST 5 ROWS ONLY"},
		{dialect: "dm", sql: "SELECT ID FROM APP.CUSTOMERS WHERE ROWNUM <= 5"},
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS FETCH FIRST 5 ROWS ONLY"},
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS WHERE ROWNUM <= 5"},
		{dialect: "oracle", sql: "SELECT DUMMY FROM DUAL"},
		{dialect: "oracle", sql: `SELECT "LIMIT", UPDATE_COUNT, NEXTVALUE FROM "APP"."T"`},
		{dialect: "oracle", sql: "SELECT '-- LIMIT CURRVAL UPDATE' AS TEXT_VALUE FROM DUAL"},
	}
	for _, test := range accepted {
		require.NoError(t, validateLimitedSelectForDialect(test.dialect, test.sql), test.sql)
	}

	rejected := []struct {
		dialect string
		sql     string
	}{
		{dialect: "oracle", sql: "SELECT ID FROM APP.CUSTOMERS LIMIT 5"},
		{dialect: "oracle", sql: "SELECT TOP 5 ID FROM APP.CUSTOMERS"},
		{dialect: "oracle", sql: "SELECT SEQ.NEXTVAL FROM DUAL"},
		{dialect: "oracle", sql: "SELECT SEQ.CURRVAL FROM DUAL"},
		{dialect: "dm", sql: "SELECT SEQ.CURRVAL FROM DUAL"},
		{dialect: "dm", sql: "SELECT ID FROM T /*+ INDEX(T IDX_T) */"},
		{dialect: "oracle", sql: "SELECT ID FROM T -- trailing"},
	}
	for _, test := range rejected {
		require.Error(t, validateLimitedSelectForDialect(test.dialect, test.sql), test.sql)
	}
}

func TestBuildSampleQueryUsesVendorPagination(t *testing.T) {
	tableName := `"APP"."CUSTOMERS"`
	projections := []string{`"ID"`, `"NAME"`}
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" LIMIT 10`,
		buildSampleQuery("dm", tableName, projections, 10),
	)
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" FETCH FIRST 10 ROWS ONLY`,
		buildSampleQuery("oracle", tableName, projections, 10),
	)
	require.Equal(
		t,
		`SELECT "ID","NAME" FROM "APP"."CUSTOMERS" LIMIT 10`,
		buildSampleQuery("postgres", tableName, projections, 10),
	)
}

func TestLimitedDialectWritePathsStillFailClosed(t *testing.T) {
	executor := &limitedSQLExecutor{dialect: "oracle", readOnly: true}
	_, err := executor.Execute(context.Background(), "DELETE FROM T")
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeReadOnly, databaseError.Code)

	executor.readOnly = false
	_, err = executor.Execute(context.Background(), "DELETE FROM T")
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageExecute, databaseError.Stage)
	require.Equal(t, DBErrorCodeExecution, databaseError.Code)
}

func TestDMAndOracleErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		classify   limitedDialectErrorClassifier
		cause      error
		wantKind   DBErrorKind
		wantCode   DBErrorCode
		wantDriver string
	}{
		{name: "dm syntax", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2007}, wantKind: DBErrorKindSyntax, wantCode: DBErrorCodeSyntax, wantDriver: "-2007"},
		{name: "dm permission range", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -5516}, wantKind: DBErrorKindPermission, wantCode: DBErrorCodePermission, wantDriver: "-5516"},
		{name: "dm authentication", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2501}, wantKind: DBErrorKindAuthentication, wantCode: DBErrorCodeAuthentication, wantDriver: "-2501"},
		{name: "dm object", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2106}, wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "-2106"},
		{name: "dm schema", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -2103}, wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "-2103"},
		{name: "dm lock timeout", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -6407}, wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "-6407"},
		{name: "dm constraint", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -6602}, wantKind: DBErrorKindConstraint, wantCode: DBErrorCodeConstraint, wantDriver: "-6602"},
		{name: "dm unknown", classify: classifyDMError, cause: &dm8.DmError{ErrCode: -77777}, wantKind: DBErrorKindExecution, wantCode: DBErrorCodeExecution, wantDriver: "-77777"},
		{name: "dm connection", classify: classifyDMError, cause: errors.New("connection refused"), wantKind: DBErrorKindConnection, wantCode: DBErrorCodeConnection},
		{name: "oracle syntax", classify: classifyOracleError, cause: gooranetwork.NewOracleError(900), wantKind: DBErrorKindSyntax, wantCode: DBErrorCodeSyntax, wantDriver: "900"},
		{name: "oracle permission", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1031), wantKind: DBErrorKindPermission, wantCode: DBErrorCodePermission, wantDriver: "1031"},
		{name: "oracle authentication", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1017), wantKind: DBErrorKindAuthentication, wantCode: DBErrorCodeAuthentication, wantDriver: "1017"},
		{name: "oracle connection", classify: classifyOracleError, cause: gooranetwork.NewOracleError(12514), wantKind: DBErrorKindConnection, wantCode: DBErrorCodeConnection, wantDriver: "12514"},
		{name: "oracle object", classify: classifyOracleError, cause: gooranetwork.NewOracleError(4043), wantKind: DBErrorKindObjectNotFound, wantCode: DBErrorCodeObjectNotFound, wantDriver: "4043"},
		{name: "oracle resource busy", classify: classifyOracleError, cause: gooranetwork.NewOracleError(54), wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "54"},
		{name: "oracle deadlock", classify: classifyOracleError, cause: gooranetwork.NewOracleError(60), wantKind: DBErrorKindRetryable, wantCode: DBErrorCodeRetryable, wantDriver: "60"},
		{name: "oracle temp space", classify: classifyOracleError, cause: gooranetwork.NewOracleError(1652), wantKind: DBErrorKindResource, wantCode: DBErrorCodeResource, wantDriver: "1652"},
		{name: "oracle shared pool", classify: classifyOracleError, cause: gooranetwork.NewOracleError(4031), wantKind: DBErrorKindResource, wantCode: DBErrorCodeResource, wantDriver: "4031"},
		{name: "oracle unknown", classify: classifyOracleError, cause: gooranetwork.NewOracleError(77777), wantKind: DBErrorKindExecution, wantCode: DBErrorCodeExecution, wantDriver: "77777"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified, ok := test.classify(context.Background(), DBStageQuery, test.cause)
			require.True(t, ok)
			var databaseError *DBError
			require.ErrorAs(t, classified, &databaseError)
			require.Equal(t, test.wantKind, databaseError.Kind)
			require.Equal(t, test.wantCode, databaseError.Code)
			require.Equal(t, test.wantDriver, databaseError.DriverCode)
		})
	}
}

func TestParseOraclePlanTable(t *testing.T) {
	result := model.QueryResult{
		Columns: []string{"ID", "PARENT_ID", "OPERATION", "OPTIONS", "CARDINALITY", "COST"},
		Rows: [][]string{
			{"0", "", "SELECT STATEMENT", "", "12", "7"},
			{"1", "0", "TABLE ACCESS", "FULL", "12", "7"},
			{"2", "1", "INDEX", "RANGE SCAN", "3", "1"},
		},
		RowCount: 3,
	}
	info, err := parseOraclePlanTable(result)
	require.NoError(t, err)
	require.Equal(t, int64(12), info.EstScanRows)
	require.Equal(t, float64(7), info.EstCost)
	require.True(t, info.UsesIndex)
	require.True(t, info.SeqScan)
	require.NotContains(t, info.Raw, "OBJECT_NAME")

	result.Rows[0][2] = "UPDATE STATEMENT"
	_, err = parseOraclePlanTable(result)
	require.Error(t, err)
	result.Rows[0][2] = "SELECT STATEMENT"
	result.Rows[2][1] = "999"
	_, err = parseOraclePlanTable(result)
	require.Error(t, err)
}

func TestParseOraclePlanTableOfflineFixtures(t *testing.T) {
	columns := []string{"ID", "PARENT_ID", "OPERATION", "OPTIONS", "CARDINALITY", "COST"}
	tests := []struct {
		name        string
		rows        [][]string
		wantIndex   bool
		wantSeqScan bool
		wantRaw     string
		wantError   bool
	}{
		{
			name: "fast dual",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "2"},
				{"1", "0", "FAST DUAL", "", "1", "2"},
			},
			wantRaw: "0|SELECT STATEMENT||1|2\n1|FAST DUAL||1|2",
		},
		{
			name: "bitmap index",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "4", "3"},
				{"1", "0", "TABLE ACCESS", "BY INDEX ROWID", "4", "3"},
				{"2", "1", "BITMAP INDEX", "RANGE SCAN", "4", "1"},
			},
			wantIndex: true,
		},
		{
			name: "domain index",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "2", "8"},
				{"1", "0", "DOMAIN INDEX", "", "2", "8"},
			},
			wantIndex: true,
		},
		{
			name: "multiple roots",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "", "FAST DUAL", "", "1", "1"},
			},
			wantError: true,
		},
		{
			name: "cycle",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "2", "VIEW", "", "1", "1"},
				{"2", "1", "FAST DUAL", "", "1", "1"},
			},
			wantError: true,
		},
		{
			name: "invalid child cardinality",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "0", "TABLE ACCESS", "FULL", "NaN", "1"},
			},
			wantError: true,
		},
		{
			name: "control character",
			rows: [][]string{
				{"0", "", "SELECT STATEMENT", "", "1", "1"},
				{"1", "0", "TABLE\nACCESS", "FULL", "1", "1"},
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := model.QueryResult{Columns: columns, Rows: test.rows, RowCount: len(test.rows)}
			info, err := parseOraclePlanTable(result)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantIndex, info.UsesIndex)
			require.Equal(t, test.wantSeqScan, info.SeqScan)
			if test.wantRaw != "" {
				require.Equal(t, test.wantRaw, info.Raw)
			}
		})
	}
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
	result, err := session.Query(ctx, "SELECT USER AS CURRENT_USER", 1)
	require.NoError(t, err)
	require.Equal(t, [][]string{{datasource.Username}}, result.Rows)
	require.NoError(t, session.Close())
	result, err = executor.Query(ctx, "SELECT TABLE_NAME FROM SYS.ALL_TABLES WHERE OWNER='SYS'", 2)
	require.NoError(t, err)
	require.Equal(t, 2, result.RowCount)
	require.True(t, result.Truncated)
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
	_, err := NewOracleExecutor(ctx, datasource, "agentsql-intentionally-wrong", true)
	require.Error(t, err)
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeAuthentication, databaseError.Code)

	executor, err := NewOracleExecutor(ctx, datasource, os.Getenv("ORACLE_PASSWORD"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.NoError(t, executor.Ping(ctx))
	session, err := executor.OpenSession(ctx, "oracle-e2e-session")
	require.NoError(t, err)
	result, err := session.Query(ctx, "SELECT DUMMY FROM SYS.DUAL", 1)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"X"}}, result.Rows)
	require.NoError(t, session.Close())
	result, err = executor.Query(ctx, "SELECT LEVEL AS N FROM DUAL CONNECT BY LEVEL <= 3", 2)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1"}, {"2"}}, result.Rows)
	require.True(t, result.Truncated)
	_, err = executor.Query(ctx, "SELECT FROM DUAL", 1)
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBErrorCodeSyntax, databaseError.Code)
	require.Equal(t, DBStageQuery, databaseError.Stage)
	info, err := executor.Explain(ctx, "SELECT TABLE_NAME FROM ALL_TABLES WHERE OWNER='SYSTEM'")
	require.NoError(t, err)
	require.Positive(t, info.EstScanRows)
	require.Positive(t, info.EstCost)
	require.NotEmpty(t, info.Raw)
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
