package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMySQLUnsupportedAdministrationFailsInParse(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "reset", sql: "RESET MASTER"},
		{name: "grant", sql: "GRANT SELECT ON app.orders TO 'report'@'%'"},
		{name: "revoke", sql: "REVOKE SELECT ON app.orders FROM 'report'@'%'"},
		{name: "shutdown", sql: "SHUTDOWN"},
		{name: "create user", sql: "CREATE USER 'report'@'%' IDENTIFIED BY 'secret'"},
		{name: "drop user", sql: "DROP USER 'report'@'%'"},
		{name: "set password", sql: "SET PASSWORD FOR 'report'@'%' = 'hash'"},
		{name: "alter instance", sql: "ALTER INSTANCE ROTATE INNODB MASTER KEY"},
		{name: "handler", sql: "HANDLER orders OPEN"},
	}

	approvedParser, err := NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NotNil(t, ast)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrUnparseable))
		})
	}
}

func TestMySQLSetScopeIsExposedInOperations(t *testing.T) {
	tests := map[string]string{
		"SET GLOBAL max_connections = 100":   "SET GLOBAL",
		"SET @@global.max_connections = 100": "SET GLOBAL",
		"SET SESSION sql_mode = ''":          "SET SESSION",
		"SET LOCAL sql_mode = ''":            "SET SESSION",
		"SET @application_flag = 1":          "SET SESSION",
	}

	approvedParser, err := NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	for sql, operation := range tests {
		ast, err := approvedParser.Parse(sql)
		require.NoError(t, err)
		require.Equal(t, model.StmtType("ADMIN"), ast.StmtType)
		require.Equal(t, []string{operation}, legacyCorpusOperations(ast.Operations))
	}
}
