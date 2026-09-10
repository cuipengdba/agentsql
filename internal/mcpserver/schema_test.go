package mcpserver

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBuildSchemaQueryUsesConstantsAndWhitelistedIdentifiers(t *testing.T) {
	postgres, err := buildSchemaQuery("postgres", "public.customers")
	require.NoError(t, err)
	require.Contains(t, postgres, postgresSchemaQuery)
	require.Contains(t, postgres, "table_schema = 'public'")
	require.Contains(t, postgres, "table_name = 'customers'")
	mysql, err := buildSchemaQuery("mysql", "customers")
	require.NoError(t, err)
	require.Contains(t, mysql, mysqlSchemaQuery)
	require.Contains(t, mysql, "table_name = 'customers'")
	for _, invalid := range []string{"a.b.c", "users'", "users;DROP", "white space", "schema.*"} {
		_, err := buildSchemaQuery("postgres", invalid)
		require.Error(t, err, invalid)
	}
	_, err = buildSchemaQuery("sqlite", "customers")
	require.Error(t, err)
}

func TestSchemaColumnsFromResultValidatesShape(t *testing.T) {
	columns, err := schemaColumnsFromResult(model.QueryResult{
		Columns: []string{"TABLE_NAME", "column_name", "table_schema"},
		Rows:    [][]string{{"customers", "id", "public"}},
	})
	require.NoError(t, err)
	require.Equal(t, "public", columns[0].Schema)
	require.Equal(t, "customers", columns[0].Table)
	require.Equal(t, "id", columns[0].Column)
	_, err = schemaColumnsFromResult(model.QueryResult{Columns: []string{"table_name"}})
	require.Error(t, err)
	_, err = schemaColumnsFromResult(model.QueryResult{
		Columns: []string{"table_schema", "table_name", "column_name"},
		Rows:    [][]string{{"public"}},
	})
	require.Error(t, err)
}
