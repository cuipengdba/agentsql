package businessdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestBuildMySQLDSNDisablesMultiStatements(t *testing.T) {
	datasource := model.Datasource{
		ID:       "ds_mysql",
		DBType:   "mysql",
		Host:     "127.0.0.1",
		Port:     3306,
		Database: "app",
		Username: "agentsql",
	}
	dsn, err := buildMySQLDSN(datasource, "password")
	require.NoError(t, err)
	require.Contains(t, dsn, "parseTime=true")
	require.Contains(t, dsn, "multiStatements=false")
	config, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	require.True(t, config.ParseTime)
	require.False(t, config.MultiStatements)
}

func TestInjectMySQLMaxExecutionTime(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		injected bool
		contains string
	}{
		{name: "select", sql: "SELECT id FROM users", injected: true, contains: "SELECT /*+ MAX_EXECUTION_TIME(5000) */"},
		{name: "leading whitespace", sql: "\n  select id FROM users", injected: true, contains: "select /*+ MAX_EXECUTION_TIME(5000) */"},
		{name: "update unchanged", sql: "UPDATE users SET a = 1", injected: false},
		{name: "insert unchanged", sql: "INSERT INTO users(id) VALUES (1)", injected: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, injected, err := injectMySQLMaxExecutionTime(test.sql, 5000)
			require.NoError(t, err)
			require.Equal(t, test.injected, injected)
			if test.contains == "" {
				require.Equal(t, test.sql, actual)
				return
			}
			require.Contains(t, actual, test.contains)
		})
	}

	first, injected, err := injectMySQLMaxExecutionTime("SELECT id FROM users", 100)
	require.NoError(t, err)
	require.True(t, injected)
	second, injected, err := injectMySQLMaxExecutionTime(first, 200)
	require.NoError(t, err)
	require.True(t, injected)
	require.Equal(t, first, second)
	require.Equal(t, 1, strings.Count(second, "MAX_EXECUTION_TIME"))
}

func TestParseMySQLExplainRows(t *testing.T) {
	columns := []string{"id", "select_type", "table", "type", "possible_keys", "key", "rows"}
	t.Run("full scan without key", func(t *testing.T) {
		info, err := parseMysqlExplainRows(columns, [][]any{
			{1, "SIMPLE", "orders", "ALL", nil, nil, []byte("120000")},
		})
		require.NoError(t, err)
		require.Equal(t, int64(120000), info.EstScanRows)
		require.True(t, info.SeqScan)
		require.False(t, info.UsesIndex)
		require.NotEmpty(t, info.Raw)
	})

	t.Run("multiple tables with index", func(t *testing.T) {
		info, err := parseMysqlExplainRows(columns, [][]any{
			{1, "SIMPLE", "orders", "ref", "idx_user", "idx_user", int64(10)},
			{1, "SIMPLE", "users", "eq_ref", "PRIMARY", "PRIMARY", "1"},
		})
		require.NoError(t, err)
		require.Equal(t, int64(11), info.EstScanRows)
		require.False(t, info.SeqScan)
		require.True(t, info.UsesIndex)
	})

	t.Run("insert values null estimate", func(t *testing.T) {
		insertColumns := []string{
			"id", "select_type", "table", "partitions", "type", "possible_keys",
			"key", "key_len", "ref", "rows", "filtered", "Extra",
		}
		info, err := parseMysqlExplainRows(insertColumns, [][]any{{
			int64(1), "INSERT", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		}})
		require.NoError(t, err)
		require.Zero(t, info.EstScanRows)
		require.False(t, info.SeqScan)
		require.False(t, info.UsesIndex)
		require.NotEmpty(t, info.Raw)
	})

	t.Run("all null plan row", func(t *testing.T) {
		info, err := parseMysqlExplainRows(
			[]string{"id", "type", "key", "rows"},
			[][]any{{nil, nil, nil, nil}},
		)
		require.NoError(t, err)
		require.Zero(t, info.EstScanRows)
		require.False(t, info.SeqScan)
		require.False(t, info.UsesIndex)
	})
}

func TestParseMySQLExplainRowsRejectsInvalidInput(t *testing.T) {
	_, err := parseMysqlExplainRows([]string{"type", "key"}, [][]any{{"ALL", nil}})
	require.Error(t, err)
	_, err = parseMysqlExplainRows(
		[]string{"type", "key", "rows"},
		[][]any{{"ALL", nil, "not-a-number"}},
	)
	require.Error(t, err)
	_, err = parseMysqlExplainRows(
		[]string{"type", "key", "rows"},
		[][]any{{"ALL", nil, "-1"}},
	)
	require.Error(t, err)
}

func TestMySQLDatabaseErrorClassification(t *testing.T) {
	tests := []struct {
		name  string
		cause error
	}{
		{name: "context deadline", cause: context.DeadlineExceeded},
		{name: "server max execution time", cause: &mysqldriver.MySQLError{Number: 3024}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := mysqlDatabaseError(context.Background(), DBStageQuery, "query", test.cause)
			require.True(t, errors.Is(err, ErrQueryTimeout))
		})
	}
}
