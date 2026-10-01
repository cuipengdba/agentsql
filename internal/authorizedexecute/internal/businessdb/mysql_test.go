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
	_, err = parseMysqlExplainRows(
		[]string{"mystery"},
		[][]any{{"unrecognized plan"}},
	)
	require.Error(t, err)
	_, err = parseMysqlExplainRows(
		[]string{"Query Plan"},
		[][]any{{strings.Repeat("x", maxExplainPlanBytes+1)}},
	)
	require.Error(t, err)
}

func TestParseTiDB751ExplainRows(t *testing.T) {
	columns := []string{"id", "estRows", "task", "access object", "operator info"}
	values := [][]any{
		{"Limit_11", "0.67", "root", "", "offset:0, count:10"},
		{"└─TableReader_18", "0.67", "root", "", "data:Limit_17"},
		{"  └─Limit_17", "0.67", "cop[tikv]", "", "offset:0, count:10"},
		{"    └─TableRangeScan_16", "0.67", "cop[tikv]", "table:agentsql_v05_customers", "range:(0,+inf], keep order:true, stats:pseudo"},
	}

	info, err := parseMysqlExplainRows(columns, values)
	require.NoError(t, err)
	require.Equal(t, int64(1), info.EstScanRows)
	require.False(t, info.SeqScan)
	require.True(t, info.UsesIndex)
	require.Equal(t, "tidb-v7 nodes=4 scan_rows=1 seq_scan=false uses_index=true", info.Raw)
	require.NotContains(t, info.Raw, "range:")

	values[3][0] = "    └─UnknownScan_99"
	_, err = parseMysqlExplainRows(columns, values)
	require.Error(t, err)
}

func TestParseOceanBase4421ExplainRows(t *testing.T) {
	plan := `==================================================================
|ID|OPERATOR        |NAME                  |EST.ROWS|EST.TIME(us)|
------------------------------------------------------------------
|0 |TABLE RANGE SCAN|agentsql_v05_customers|2       |3           |
==================================================================
Outputs & filters:
-------------------------------------
  0 - output([agentsql_v05_customers.id], [agentsql_v05_customers.name], [agentsql_v05_customers.phone], [agentsql_v05_customers.email]), filter(nil), rowset=16
      access([agentsql_v05_customers.id], [agentsql_v05_customers.name], [agentsql_v05_customers.phone], [agentsql_v05_customers.email]), partitions(p0)
      limit(10), offset(nil), is_index_back=false, is_global_index=false,
      range_key([agentsql_v05_customers.id]), range(0 ; MAX),
      range_cond([agentsql_v05_customers.id > 0])`

	planRows := strings.Split(plan, "\n")
	values := make([][]any, len(planRows))
	for index, row := range planRows {
		values[index] = []any{row}
	}
	info, err := parseMysqlExplainRows([]string{"Query Plan"}, values)
	require.NoError(t, err)
	require.Equal(t, int64(2), info.EstScanRows)
	require.False(t, info.SeqScan)
	require.True(t, info.UsesIndex)
	require.Equal(t, "oceanbase-v4 nodes=1 scan_rows=2 seq_scan=false uses_index=true", info.Raw)
	require.NotContains(t, info.Raw, "id > 0")

	unknown := strings.Replace(plan, "TABLE RANGE SCAN", "UNKNOWN OPERATOR", 1)
	unknownRows := strings.Split(unknown, "\n")
	unknownValues := make([][]any, len(unknownRows))
	for index, row := range unknownRows {
		unknownValues[index] = []any{row}
	}
	_, err = parseMysqlExplainRows([]string{"Query Plan"}, unknownValues)
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
