package businessdb

import (
	"net/url"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/stretchr/testify/require"
)

func TestBuildSQLServerDSNDefaultsToStrictTLS(t *testing.T) {
	datasource := model.Datasource{
		ID: "sqlserver", DBType: "sqlserver", Host: "db.example.test", Port: 1433,
		Database: "app", Username: "agent", TLSServerName: "db.internal.example.test",
	}
	dsn, err := buildSQLServerDSN(datasource, "p@ss:/?#")
	require.NoError(t, err)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "sqlserver", parsed.Scheme)
	require.Equal(t, "db.example.test:1433", parsed.Host)
	require.Equal(t, "agent", parsed.User.Username())
	password, present := parsed.User.Password()
	require.True(t, present)
	require.Equal(t, "p@ss:/?#", password)
	require.Equal(t, "strict", parsed.Query().Get("encrypt"))
	require.Equal(t, "1.2", parsed.Query().Get("tlsmin"))
	require.Equal(t, "db.internal.example.test", parsed.Query().Get("hostnameincertificate"))
}

func TestBuildSQLServerDSNRejectsUnsafeTLSConfiguration(t *testing.T) {
	base := model.Datasource{ID: "sqlserver", Host: "127.0.0.1", Port: 1433, Database: "app", Username: "agent"}
	base.TrustServerCertificate = true
	_, err := buildSQLServerDSN(base, "secret")
	require.ErrorContains(t, err, "strict TLS")

	base.TrustServerCertificate = false
	base.TLSMode = "disable"
	_, err = buildSQLServerDSN(base, "secret")
	require.ErrorContains(t, err, "unsupported TLS mode")
}

func TestSQLServerLimitedSelectFailsClosed(t *testing.T) {
	allowed := []string{
		"SELECT TOP 10 [id],[name] FROM [dbo].[users] WHERE [id] = 1",
		"SELECT [id] FROM [dbo].[users] ORDER BY [id] OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY",
	}
	for _, sqlText := range allowed {
		require.NoError(t, validateLimitedSelectForDialect("sqlserver", sqlText), sqlText)
	}
	blocked := []string{
		"SELECT * FROM dbo.users; EXEC xp_cmdshell 'whoami'",
		"SELECT * FROM OPENROWSET(BULK 'x', SINGLE_BLOB) AS x",
		"SELECT * FROM dbo.users WAITFOR DELAY '00:00:05'",
		"UPDATE dbo.users SET enabled = 0",
		"DELETE FROM dbo.users",
		"EXEC xp_cmdshell 'whoami'",
		"SELECT * FROM dbo.users LIMIT 10",
	}
	for _, sqlText := range blocked {
		require.Error(t, validateLimitedSelectForDialect("sqlserver", sqlText), sqlText)
	}
}

func TestParseSQLServerShowplan(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements><StmtSimple StatementSubTreeCost="0.42" StatementEstRows="11"><QueryPlan><RelOp EstimateRows="10.2" EstimatedTotalSubtreeCost="0.42" PhysicalOp="Index Seek"/><RelOp EstimateRows="3" EstimatedTotalSubtreeCost="0.1" PhysicalOp="Table Scan"/></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`)
	info, err := parseSQLServerShowplan(raw)
	require.NoError(t, err)
	require.EqualValues(t, 11, info.EstScanRows)
	require.InDelta(t, 0.42, info.EstCost, 0.0001)
	require.True(t, info.UsesIndex)
	require.True(t, info.SeqScan)
	require.NotContains(t, info.Raw, "dbo")

	_, err = parseSQLServerShowplan([]byte(`<!DOCTYPE x><ShowPlanXML/>`))
	require.Error(t, err)
}

func TestSQLServerSchemaQueryIsParameterized(t *testing.T) {
	query, args := sqlServerSchemaQuery("dbo", []SchemaTable{{Schema: "sales", Table: "orders"}})
	require.Contains(t, query, "FROM sys.tables")
	require.Contains(t, query, "s.name=@p1")
	require.Contains(t, query, "t.name=@p2")
	require.NotContains(t, query, "sales")
	require.Equal(t, []any{"sales", "orders"}, args)
}

func TestClassifySQLServerErrorUsesStablePublicCodes(t *testing.T) {
	for _, test := range []struct {
		number int32
		kind   DBErrorKind
		code   DBErrorCode
	}{
		{number: 18456, kind: DBErrorKindAuthentication, code: DBErrorCodeAuthentication},
		{number: 229, kind: DBErrorKindPermission, code: DBErrorCodePermission},
		{number: 208, kind: DBErrorKindObjectNotFound, code: DBErrorCodeObjectNotFound},
		{number: 1205, kind: DBErrorKindRetryable, code: DBErrorCodeRetryable},
		{number: 10054, kind: DBErrorKindConnection, code: DBErrorCodeConnection},
	} {
		classified, ok := classifySQLServerError(t.Context(), DBStageQuery, mssql.Error{Number: test.number})
		require.True(t, ok)
		var databaseError *DBError
		require.ErrorAs(t, classified, &databaseError)
		require.Equal(t, test.kind, databaseError.Kind)
		require.Equal(t, test.code, databaseError.Code)
		require.Equal(t, DBStageQuery, databaseError.Stage)
	}
}
