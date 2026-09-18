package controlledread

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
)

const metadataRowLimit = discovery.MaxMetadataColumns + 1

func validateIdentifier(value string, required bool) error {
	if value != strings.TrimSpace(value) || (required && value == "") {
		return fmt.Errorf("invalid identifier")
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return fmt.Errorf("invalid identifier")
		}
	}
	return nil
}

func quoteIdentifier(dialect model.DBDialect, identifier string) (string, error) {
	if err := validateIdentifier(identifier, true); err != nil {
		return "", err
	}
	switch dialect {
	case "postgres":
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`, nil
	case "mysql":
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
	default:
		return "", fmt.Errorf("unsupported dialect")
	}
}

func sqlLiteral(value string) (string, error) {
	if err := validateIdentifier(value, true); err != nil {
		return "", err
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
}

func buildMetadataSQL(dialect model.DBDialect, database string, tables []discovery.TableRef) (string, error) {
	if len(tables) < discovery.MinTables || len(tables) > discovery.MaxTables {
		return "", fmt.Errorf("invalid table scope")
	}
	conditions := make([]string, 0, len(tables))
	for _, table := range tables {
		if err := validateIdentifier(table.Schema, true); err != nil {
			return "", err
		}
		if err := validateIdentifier(table.Table, true); err != nil {
			return "", err
		}
		schema, _ := sqlLiteral(table.Schema)
		name, _ := sqlLiteral(table.Table)
		conditions = append(conditions, "(c.table_schema = "+schema+" AND c.table_name = "+name+")")
	}
	scope := "(" + strings.Join(conditions, " OR ") + ")"
	switch dialect {
	case "postgres":
		return "SELECT c.table_schema, c.table_name, c.column_name, c.data_type, c.udt_name, c.ordinal_position " +
			"FROM information_schema.columns AS c JOIN information_schema.tables AS t " +
			"ON t.table_catalog = c.table_catalog AND t.table_schema = c.table_schema AND t.table_name = c.table_name " +
			"WHERE c.table_catalog = current_database() AND t.table_type = 'BASE TABLE' " +
			"AND c.table_schema NOT IN ('pg_catalog', 'information_schema') AND " + scope +
			" ORDER BY c.table_schema, c.table_name, c.ordinal_position", nil
	case "mysql":
		if err := validateIdentifier(database, true); err != nil {
			return "", err
		}
		for _, table := range tables {
			if table.Schema != database {
				return "", fmt.Errorf("MySQL schema is outside current database")
			}
		}
		return "SELECT c.table_schema, c.table_name, c.column_name, c.data_type, c.column_type, c.ordinal_position " +
			"FROM information_schema.columns AS c JOIN information_schema.tables AS t " +
			"ON t.table_schema = c.table_schema AND t.table_name = c.table_name " +
			"WHERE c.table_schema = DATABASE() AND t.table_type = 'BASE TABLE' AND " + scope +
			" ORDER BY c.table_name, c.ordinal_position", nil
	default:
		return "", fmt.Errorf("unsupported dialect")
	}
}

func buildSampleSQL(dialect model.DBDialect, table discovery.TableRef, columns []discovery.ColumnRef, limit int) (string, error) {
	if limit < 1 || limit > discovery.MaxSampleRows || len(columns) < 1 || len(columns) > discovery.MaxColumnsPerSampleQuery {
		return "", fmt.Errorf("invalid sample bounds")
	}
	schema, err := quoteIdentifier(dialect, table.Schema)
	if err != nil {
		return "", err
	}
	tableName, err := quoteIdentifier(dialect, table.Table)
	if err != nil {
		return "", err
	}
	projection := make([]string, len(columns))
	for index, column := range columns {
		if column.Schema != table.Schema || column.Table != table.Table {
			return "", fmt.Errorf("column is outside table")
		}
		projection[index], err = quoteIdentifier(dialect, column.Column)
		if err != nil {
			return "", err
		}
	}
	return "SELECT " + strings.Join(projection, ", ") + " FROM " + schema + "." + tableName + " LIMIT " + strconv.Itoa(limit), nil
}
