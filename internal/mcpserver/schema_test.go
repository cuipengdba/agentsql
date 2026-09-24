package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/stretchr/testify/require"
)

const postgresSchemaQuery = `SELECT table_schema, table_name, column_name
FROM information_schema.columns
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')`

const mysqlSchemaQuery = `SELECT table_schema, table_name, column_name
FROM information_schema.columns
WHERE table_schema = DATABASE()`

var schemaObjectPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)?$`)

func buildSchemaQuery(dialect, object string) (string, error) {
	var query string
	switch dialect {
	case "postgres":
		query = postgresSchemaQuery
	case "mysql":
		query = mysqlSchemaQuery
	default:
		return "", fmt.Errorf("unsupported schema dialect %q", dialect)
	}
	if object == "" {
		return query + " ORDER BY table_schema, table_name, ordinal_position", nil
	}
	if !schemaObjectPattern.MatchString(object) {
		return "", fmt.Errorf("invalid schema table identifier")
	}
	parts := strings.Split(object, ".")
	if len(parts) == 2 {
		query += " AND table_schema = '" + parts[0] + "' AND table_name = '" + parts[1] + "'"
	} else {
		query += " AND table_name = '" + parts[0] + "'"
	}
	return query + " ORDER BY table_schema, table_name, ordinal_position", nil
}

func schemaColumnsFromResult(result model.QueryResult) ([]policy.SchemaColumn, error) {
	indexes := make(map[string]int, len(result.Columns))
	for index, column := range result.Columns {
		indexes[strings.ToLower(strings.TrimSpace(column))] = index
	}
	schemaIndex, hasSchema := indexes["table_schema"]
	tableIndex, hasTable := indexes["table_name"]
	columnIndex, hasColumn := indexes["column_name"]
	if !hasSchema || !hasTable || !hasColumn {
		return nil, fmt.Errorf("schema query result is missing required columns")
	}
	columns := make([]policy.SchemaColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		if schemaIndex >= len(row) || tableIndex >= len(row) || columnIndex >= len(row) {
			return nil, fmt.Errorf("schema query result row has invalid width")
		}
		columns = append(columns, policy.SchemaColumn{Schema: row[schemaIndex], Table: row[tableIndex], Column: row[columnIndex]})
	}
	return columns, nil
}

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
