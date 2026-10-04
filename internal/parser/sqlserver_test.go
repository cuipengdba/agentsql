package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLServerParserAcceptsOnlyNarrowSelectProfile(t *testing.T) {
	approved, err := NewParser(model.DialectSQLServer)
	require.NoError(t, err)
	ast, err := approved.Parse("SELECT TOP 20 u.[id],u.[display name] FROM [dbo].[users] AS u WHERE u.[id] = 1 ORDER BY u.[id]")
	require.NoError(t, err)
	require.Equal(t, model.DialectSQLServer, ast.Dialect)
	require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
	require.True(t, ast.HasLimit)
	require.True(t, ast.HasWhere)
	require.Equal(t, []model.ObjectRef{{Schema: "dbo", Table: "users", Alias: "u"}}, ast.Tables)
}

func TestSQLServerParserRejectsWritesProceduresAndDynamicSources(t *testing.T) {
	approved, err := NewParser(model.DialectSQLServer)
	require.NoError(t, err)
	for _, sqlText := range []string{
		"UPDATE dbo.users SET enabled=0",
		"DELETE FROM dbo.users",
		"EXEC xp_cmdshell 'whoami'",
		"SELECT * FROM OPENROWSET(BULK 'x', SINGLE_BLOB) AS x",
		"SELECT * FROM dbo.users; DELETE FROM dbo.users",
	} {
		ast, parseErr := approved.Parse(sqlText)
		require.Error(t, parseErr, sqlText)
		require.NotNil(t, ast, sqlText)
	}
}
