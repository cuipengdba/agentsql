package controlledread

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestQuoteIdentifier(t *testing.T) {
	postgres, err := quoteIdentifier("postgres", `odd"name`)
	require.NoError(t, err)
	require.Equal(t, `"odd""name"`, postgres)
	mysql, err := quoteIdentifier("mysql", "odd`name")
	require.NoError(t, err)
	require.Equal(t, "`odd``name`", mysql)
	sqlserver, err := quoteIdentifier("sqlserver", "odd]name")
	require.NoError(t, err)
	require.Equal(t, "[odd]]name]", sqlserver)
}

func TestBuildSQLServerSampleAndMetadataSQL(t *testing.T) {
	table := discovery.TableRef{Schema: "dbo", Table: "customers"}
	columns := []discovery.ColumnRef{{Schema: "dbo", Table: "customers", Column: "phone"}}
	sample, err := buildSampleSQL("sqlserver", table, columns, 20)
	require.NoError(t, err)
	require.Equal(t, "SELECT TOP 20 [phone] FROM [dbo].[customers]", sample)
	require.NoError(t, assertSampleSQL("sqlserver", sample, table, columns))

	metadata, err := buildMetadataSQL("sqlserver", "app", []discovery.TableRef{table})
	require.NoError(t, err)
	require.Contains(t, metadata, "FROM sys.tables")
	require.Contains(t, metadata, "JOIN sys.columns")
	require.Contains(t, metadata, "s.name = 'dbo'")
}

func TestBuildSampleSQLAndASTInvariant(t *testing.T) {
	for _, dialect := range []model.DBDialect{"postgres", "mysql"} {
		t.Run(string(dialect), func(t *testing.T) {
			table := discovery.TableRef{Schema: "app", Table: "customers"}
			columns := []discovery.ColumnRef{
				{Schema: "app", Table: "customers", Column: "phone"},
				{Schema: "app", Table: "customers", Column: "email"},
			}
			sqlText, err := buildSampleSQL(dialect, table, columns, 20)
			require.NoError(t, err)
			require.Contains(t, sqlText, " LIMIT 20")
			require.NoError(t, assertSampleSQL(dialect, sqlText, table, columns))
			require.NotContains(t, sqlText, "WHERE")
			require.NotContains(t, sqlText, "OFFSET")
		})
	}
}

func TestBuildMetadataSQL(t *testing.T) {
	tables := []discovery.TableRef{{Schema: "public", Table: "customers"}}
	postgres, err := buildMetadataSQL("postgres", "app", tables)
	require.NoError(t, err)
	require.Contains(t, postgres, "table_catalog = current_database()")
	require.Contains(t, postgres, "table_type = 'BASE TABLE'")
	require.NoError(t, assertMetadataSQL("postgres", postgres))

	mysqlTables := []discovery.TableRef{{Schema: "app", Table: "customers"}}
	mysql, err := buildMetadataSQL("mysql", "app", mysqlTables)
	require.NoError(t, err)
	require.Contains(t, mysql, "table_schema = DATABASE()")
	require.Contains(t, mysql, "table_type = 'BASE TABLE'")
	require.NoError(t, assertMetadataSQL("mysql", mysql))
	_, err = buildMetadataSQL("mysql", "other", mysqlTables)
	require.Error(t, err)
}

func TestBuildSampleSQLRejectsInvalidBounds(t *testing.T) {
	table := discovery.TableRef{Schema: "app", Table: "customers"}
	column := []discovery.ColumnRef{{Schema: "app", Table: "customers", Column: "phone"}}
	for _, limit := range []int{-1, 0, 21, int(^uint(0) >> 1)} {
		_, err := buildSampleSQL("postgres", table, column, limit)
		require.Error(t, err)
	}
}
