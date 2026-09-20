package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"vitess.io/vitess/go/vt/sqlparser"
)

func TestMySQLTableStatementWhereShapes(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	tests := []struct {
		name     string
		sql      string
		hasWhere bool
	}{
		{name: "ordinary select", sql: "SELECT id FROM users"},
		{name: "union left where", sql: "SELECT id FROM active_users WHERE enabled = 1 UNION ALL SELECT id FROM archived_users", hasWhere: true},
		{name: "union right where", sql: "SELECT id FROM active_users UNION ALL SELECT id FROM archived_users WHERE enabled = 1", hasWhere: true},
		{name: "nested union", sql: "(SELECT id FROM a UNION SELECT id FROM b) UNION SELECT id FROM c WHERE id = 1", hasWhere: true},
		{name: "cte inner where does not protect root", sql: "WITH q AS (SELECT id FROM users WHERE enabled = 1) SELECT id FROM q"},
		{name: "parenthesized select", sql: "(SELECT id FROM users WHERE id = 1)", hasWhere: true},
		{name: "parenthesized union", sql: "(SELECT id FROM a WHERE id = 1) UNION (SELECT id FROM b)", hasWhere: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, parseErr := approved.Parse(test.sql)
			require.NoError(t, parseErr)
			require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
			require.Equal(t, test.hasWhere, ast.HasWhere)
		})
	}
}

func TestMySQLValuesTableStatementsFailClosed(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "union values right", sql: "SELECT 1 UNION VALUES ROW(2)"},
		{name: "union values right after where", sql: "SELECT 1 WHERE 1 = 1 UNION VALUES ROW(2)"},
		{name: "union values left", sql: "VALUES ROW(1) UNION SELECT 2"},
		{name: "nested values left", sql: "(VALUES ROW(1) UNION SELECT 2) UNION SELECT 3"},
		{name: "nested values right", sql: "SELECT 1 UNION (SELECT 2 UNION VALUES ROW(3))"},
		{name: "root values", sql: "VALUES ROW(1), ROW(2)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ast, parseErr := approved.Parse(test.sql)
			require.Error(t, parseErr)
			require.True(t, errors.Is(parseErr, ErrUnparseable))
			require.ErrorContains(t, parseErr, "VALUES table statement is unsupported")
			require.NotNil(t, ast)
			require.Equal(t, test.sql, ast.RawSQL)
			require.False(t, ast.IsMulti)
			require.Empty(t, ast.StmtType)
		})
	}
}

func TestMySQLTableStatementWhereRejectsNilAndValues(t *testing.T) {
	var nilTable sqlparser.TableStatement
	_, _, err := mysqlTableStatementWhere(nilTable)
	require.ErrorContains(t, err, "table statement is nil")

	var nilSelect *sqlparser.Select
	_, _, err = mysqlTableStatementWhere(nilSelect)
	require.ErrorContains(t, err, "SELECT table statement is nil")

	var nilUnion *sqlparser.Union
	_, _, err = mysqlTableStatementWhere(nilUnion)
	require.ErrorContains(t, err, "UNION table statement is nil")

	_, _, err = mysqlTableStatementWhere(&sqlparser.ValuesStatement{})
	require.ErrorContains(t, err, "VALUES table statement is unsupported")

	leftWithWhere := &sqlparser.Select{Where: &sqlparser.Where{Expr: sqlparser.BoolVal(true)}}
	unsafeUnion := &sqlparser.Union{Left: leftWithWhere, Right: &sqlparser.ValuesStatement{}}
	_, _, err = mysqlTableStatementWhere(unsafeUnion)
	require.ErrorContains(t, err, "VALUES table statement is unsupported",
		"both UNION branches must be validated even when the left branch has a WHERE")
}

func TestMySQLNilSelectExprsHelpersDoNotPanic(t *testing.T) {
	selectNode := &sqlparser.Select{SelectExprs: nil}
	require.NotPanics(t, func() {
		require.False(t, mysqlExistsConstantSelect(&sqlparser.Subquery{Select: selectNode}))
		hasGroupBy, pureAggregate := mysqlAggregateShape(selectNode)
		require.False(t, hasGroupBy)
		require.False(t, pureAggregate)
		require.Empty(t, mysqlProjectedColumns(selectNode))
		require.Nil(t, mysqlDirectProjections(selectNode))
	})
}

func TestMySQLProjectionAndAggregateSignalsRemainStable(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	t.Run("null", func(t *testing.T) {
		ast, parseErr := approved.Parse("SELECT NULL AS missing_value")
		require.NoError(t, parseErr)
		require.Empty(t, ast.Columns)
		require.Nil(t, ast.DirectProjections)
		require.False(t, ast.IsPureAggregate)
	})

	t.Run("stars", func(t *testing.T) {
		ast, parseErr := approved.Parse("SELECT *, t.* FROM t")
		require.NoError(t, parseErr)
		require.Equal(t, []string{"*"}, ast.Columns)
		require.Nil(t, ast.DirectProjections)
	})

	t.Run("pure aggregate", func(t *testing.T) {
		ast, parseErr := approved.Parse("SELECT COUNT(*), SUM(amount) FROM orders")
		require.NoError(t, parseErr)
		require.True(t, ast.IsPureAggregate)
		require.False(t, ast.HasGroupBy)
	})

	t.Run("mixed aggregate", func(t *testing.T) {
		ast, parseErr := approved.Parse("SELECT status, COUNT(*) FROM orders")
		require.NoError(t, parseErr)
		require.False(t, ast.IsPureAggregate)
	})

	t.Run("aliases preserve direct projection order", func(t *testing.T) {
		ast, parseErr := approved.Parse("SELECT t.phone AS mobile, t.id AS identifier FROM customers AS t")
		require.NoError(t, parseErr)
		require.Equal(t, []model.DirectProjectionRef{
			{Column: "phone", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
			{Column: "id", Offset: 1, Source: model.ObjectRef{Table: "customers"}},
		}, ast.DirectProjections)
	})
}

func TestMySQLCTETableStatementProjectionIsConservative(t *testing.T) {
	vitessParser, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.0.30"})
	require.NoError(t, err)

	for _, sql := range []string{
		"WITH q AS (VALUES ROW(1)) SELECT * FROM q",
		"WITH q AS (SELECT phone FROM customers UNION SELECT phone FROM archive) SELECT * FROM q",
	} {
		statement, parseErr := vitessParser.ParseStrictDDL(sql)
		require.NoError(t, parseErr, sql)
		require.Equal(t, []string{"*"}, mysqlProjectedColumns(statement), sql)
		require.Nil(t, mysqlDirectProjections(statement), sql)
	}
}

func TestMySQLNormalizationGoldenRemainsStable(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	tests := []struct {
		name       string
		sql        string
		normalized string
	}{
		{name: "not", sql: "SELECT NOT (id = 7) FROM users", normalized: "select id != :id /* INT64 */ from users"},
		{name: "in", sql: "SELECT id FROM users WHERE id IN (1, 2, 3)", normalized: "select id from users where id in ::redacted1"},
		{name: "exists", sql: "SELECT EXISTS (SELECT 1 FROM orders WHERE orders.user_id = 42)", normalized: "select exists (select 1 from orders where orders.user_id = :orders_user_id /* INT64 */) from dual"},
		{name: "subquery", sql: "SELECT (SELECT name FROM users WHERE id = 9) AS nested_value", normalized: "select (select `name` from users where id = :id /* INT64 */) as nested_value from dual"},
		{name: "string hex date literals", sql: "SELECT 'secret', X'4142', DATE '2024-01-02'", normalized: "select :redacted1 /* VARCHAR */, :redacted2 /* HEXVAL */, CAST(:redacted3 AS DATE) from dual"},
		{name: "ordinary comment", sql: "SELECT /* ordinary */ id FROM users", normalized: "select /* ordinary */ id from users"},
		{name: "optimizer hint", sql: "SELECT /*+ INDEX(users idx_users_id) */ id FROM users", normalized: "select /*+ INDEX(users idx_users_id) */ id from users"},
		{name: "into outfile", sql: "SELECT id FROM users INTO OUTFILE '/tmp/a.csv'", normalized: "select id from users into outfile ?"},
		{name: "into dumpfile", sql: "SELECT id FROM users INTO DUMPFILE '/tmp/a.bin'", normalized: "select id from users into dumpfile ?"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, parseErr := approved.Parse(test.sql)
			require.NoError(t, parseErr)
			require.Equal(t, test.normalized, ast.Normalized)
		})
	}
}

func TestMySQLVersionPinAndQuotedNewKeywords(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	at8030, err := approved.Parse("SELECT 0 /*!80030 , 1 AS at_8030 */")
	require.NoError(t, err)
	require.Equal(t, "select :redacted1 /* INT64 */, :redacted2 /* INT64 */ as at_8030 from dual", at8030.Normalized)

	at8040, err := approved.Parse("SELECT 0 /*!80040 , 1 AS at_8040 */")
	require.NoError(t, err)
	require.Equal(t, "select :redacted1 /* INT64 */ from dual", at8040.Normalized)

	quoted, err := approved.Parse("SELECT `qualify`, `tablesample`, `signal`, `declare`, `handler`, `inout`, `out` FROM words")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"qualify", "tablesample", "signal", "declare", "handler", "inout", "out"}, quoted.Columns)
}

func TestMySQLStoredProceduresAndStackedStatementsRemainRejected(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	for _, sql := range []string{
		"CREATE PROCEDURE p() SELECT 1",
		"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END",
		"DROP PROCEDURE p",
	} {
		ast, parseErr := approved.Parse(sql)
		require.Error(t, parseErr, sql)
		require.True(t, errors.Is(parseErr, ErrUnparseable), sql)
		require.False(t, ast.IsMulti, sql)
	}

	ast, parseErr := approved.Parse("SELECT 1; SELECT 2")
	require.Error(t, parseErr)
	require.True(t, errors.Is(parseErr, ErrUnparseable))
	require.True(t, ast.IsMulti)
}
