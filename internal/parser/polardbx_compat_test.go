package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPolarDBXUsesBoundedMySQLGrammar(t *testing.T) {
	approved, err := NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)

	for _, test := range []struct {
		name     string
		sql      string
		stmtType model.StmtType
	}{
		{name: "select with where and limit", sql: "SELECT id, phone FROM customers WHERE id = 7 LIMIT 10", stmtType: "SELECT"},
		{name: "insert", sql: "INSERT INTO customers(id, phone) VALUES (7, '13800138000')", stmtType: "INSERT"},
		{name: "update with predicate", sql: "UPDATE customers SET phone = '13900139000' WHERE id = 7", stmtType: "UPDATE"},
		{name: "delete with predicate", sql: "DELETE FROM customers WHERE id = 7", stmtType: "DELETE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ast, parseErr := approved.Parse(test.sql)
			require.NoError(t, parseErr)
			require.Equal(t, mysqlDialect, ast.Dialect)
			require.Equal(t, test.stmtType, ast.StmtType)
			require.False(t, ast.IsMulti)
		})
	}
}

func TestPolarDBXExtensionsDoNotCreateASecondDialect(t *testing.T) {
	_, err := NewParser(model.DBDialect("polardbx"))
	require.ErrorIs(t, err, ErrUnsupportedDialect)

	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	ast, err := approved.Parse("SHOW TOPOLOGY FROM customers")
	if err == nil {
		require.NotContains(t, []model.StmtType{"SELECT", "INSERT", "UPDATE", "DELETE"}, ast.StmtType)
	}
}
