package businessdb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
)

const typedSchemaRowLimit = 10_000

type SchemaTable struct{ Schema, Table string }
type SchemaColumn struct {
	Schema, Table, Column, DataType string
	Ordinal                         int
}
type SampleColumn struct{ Schema, Table, Column string }

func validIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character == '_' || unicode.IsLetter(character) || (index > 0 && unicode.IsDigit(character)) {
			continue
		}
		return false
	}
	return true
}

func quoteIdentifier(dialect, value string) (string, error) {
	if !validIdentifier(value) {
		return "", fmt.Errorf("invalid identifier")
	}
	if dialect == "mysql" {
		return "`" + value + "`", nil
	}
	return `"` + value + `"`, nil
}

// ListSchema is a typed metadata operation. Callers supply identities, never
// SQL text; all placeholders and SQL are fixed inside the capability domain.
func ListSchema(ctx context.Context, executor Executor, database string, tables []SchemaTable) ([]SchemaColumn, error) {
	if ctx == nil || isNilExecutor(executor) {
		return nil, fmt.Errorf("typed schema read unavailable")
	}
	if len(tables) > 256 {
		return nil, resourceError("AUTH_RELATION_LIMIT")
	}
	for _, table := range tables {
		if !validIdentifier(table.Table) || (table.Schema != "" && !validIdentifier(table.Schema)) {
			return nil, fmt.Errorf("invalid schema identity")
		}
	}
	var result model.QueryResult
	var err error
	switch typed := executor.(type) {
	case *PostgresExecutor:
		query, args := postgresSchemaQuery(tables)
		rows, queryErr := typed.pool.Query(ctx, query, args...)
		if queryErr != nil {
			return nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL metadata", queryErr)
		}
		result, err = collectRows(&postgresRowSource{rows: rows}, typedSchemaRowLimit)
	case *MySQLExecutor:
		query, args := mysqlSchemaQuery(database, tables)
		rows, queryErr := typed.database.QueryContext(ctx, query, args...)
		if queryErr != nil {
			return nil, mysqlDatabaseError(ctx, DBStageMetadata, "read MySQL metadata", queryErr)
		}
		result, err = collectRows(&mysqlRowSource{rows: rows}, typedSchemaRowLimit)
	case *DMExecutor:
		query, args := dmSchemaQuery(typed.username, tables)
		metadataContext, cancel := typed.timeoutContext(ctx)
		defer cancel()
		rows, queryErr := typed.database.QueryContext(metadataContext, query, args...)
		if queryErr != nil {
			return nil, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
		}
		result, err = collectRows(&mysqlRowSource{rows: rows}, typedSchemaRowLimit)
	case *OracleExecutor:
		query, args := oracleSchemaQuery(typed.username, tables)
		metadataContext, cancel := typed.timeoutContext(ctx)
		defer cancel()
		rows, queryErr := typed.database.QueryContext(metadataContext, query, args...)
		if queryErr != nil {
			return nil, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
		}
		result, err = collectRows(&mysqlRowSource{rows: rows}, typedSchemaRowLimit)
	case *YashanExecutor:
		query, args := yashanSchemaQuery(typed.username, tables)
		metadataContext, cancel := typed.timeoutContext(ctx)
		defer cancel()
		rows, queryErr := typed.database.QueryContext(metadataContext, query, args...)
		if queryErr != nil {
			return nil, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
		}
		result, err = collectRows(&mysqlRowSource{rows: rows}, typedSchemaRowLimit)
		if err != nil {
			var limitError *ResourceError
			if errors.As(err, &limitError) {
				return nil, limitError
			}
			return nil, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
		}
	default:
		return nil, fmt.Errorf("unsupported typed schema reader")
	}
	if err != nil {
		return nil, err
	}
	if result.Truncated || len(result.Columns) != 6 {
		return nil, resourceError("AUTH_RESULT_LIMIT")
	}
	columns := make([]SchemaColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) != 6 {
			return nil, resourceError("AUTH_DATABASE_ERROR")
		}
		ordinal, parseErr := strconv.Atoi(row[5])
		if parseErr != nil || ordinal < 1 {
			return nil, resourceError("AUTH_DATABASE_ERROR")
		}
		dataType := row[3]
		if row[4] != "" && row[4] != row[3] {
			dataType += "/" + row[4]
		}
		columns = append(columns, SchemaColumn{Schema: row[0], Table: row[1], Column: row[2], DataType: dataType, Ordinal: ordinal})
	}
	return columns, nil
}

func postgresSchemaQuery(tables []SchemaTable) (string, []any) {
	query := `SELECT table_schema,table_name,column_name,data_type,udt_name,ordinal_position::text FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema')`
	args := make([]any, 0, len(tables)*2)
	if len(tables) > 0 {
		parts := make([]string, 0, len(tables))
		for _, table := range tables {
			if table.Schema == "" {
				args = append(args, table.Table)
				parts = append(parts, "table_name=$"+strconv.Itoa(len(args)))
			} else {
				args = append(args, table.Schema, table.Table)
				parts = append(parts, "(table_schema=$"+strconv.Itoa(len(args)-1)+" AND table_name=$"+strconv.Itoa(len(args))+")")
			}
		}
		query += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	return query + " ORDER BY table_schema,table_name,ordinal_position", args
}

func mysqlSchemaQuery(database string, tables []SchemaTable) (string, []any) {
	query := "SELECT table_schema,table_name,column_name,data_type,column_type,CAST(ordinal_position AS CHAR) FROM information_schema.columns WHERE table_schema=?"
	args := []any{database}
	if len(tables) > 0 {
		parts := make([]string, 0, len(tables))
		for _, table := range tables {
			args = append(args, table.Table)
			parts = append(parts, "table_name=?")
		}
		query += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	return query + " ORDER BY table_schema,table_name,ordinal_position", args
}

func dmSchemaQuery(defaultOwner string, tables []SchemaTable) (string, []any) {
	query := "SELECT OWNER,TABLE_NAME,COLUMN_NAME,DATA_TYPE,DATA_TYPE,CAST(COLUMN_ID AS VARCHAR(20)) FROM SYS.ALL_TAB_COLUMNS WHERE "
	filter, args := oracleStyleTableFilter(defaultOwner, tables, func(int) string { return "?" })
	return query + filter + " ORDER BY OWNER,TABLE_NAME,COLUMN_ID", args
}

func oracleSchemaQuery(defaultOwner string, tables []SchemaTable) (string, []any) {
	query := "SELECT OWNER,TABLE_NAME,COLUMN_NAME,DATA_TYPE,DATA_TYPE,TO_CHAR(COLUMN_ID) FROM ALL_TAB_COLUMNS WHERE "
	filter, args := oracleStyleTableFilter(defaultOwner, tables, func(index int) string {
		return ":" + strconv.Itoa(index)
	})
	return query + filter + " ORDER BY OWNER,TABLE_NAME,COLUMN_ID", args
}

func yashanSchemaQuery(defaultOwner string, tables []SchemaTable) (string, []any) {
	// YashanDB 23.4 reports ALL_TAB_COLUMNS.COLUMN_ID from zero, while the
	// authorization model exposes ordinals from one. Normalize in fixed SQL so
	// the global row validator remains strict.
	query := "SELECT OWNER,TABLE_NAME,COLUMN_NAME,DATA_TYPE,DATA_TYPE,TO_CHAR(COLUMN_ID + 1) FROM ALL_TAB_COLUMNS WHERE "
	filter, args := oracleStyleTableFilter(defaultOwner, tables, func(int) string { return "?" })
	return query + filter + " ORDER BY OWNER,TABLE_NAME,COLUMN_ID", args
}

func oracleStyleTableFilter(
	defaultOwner string,
	tables []SchemaTable,
	placeholder func(int) string,
) (string, []any) {
	args := make([]any, 0, max(1, len(tables)*2))
	if len(tables) == 0 {
		args = append(args, defaultOwner)
		return "OWNER=" + placeholder(1), args
	}
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		owner := table.Schema
		if owner == "" {
			owner = defaultOwner
		}
		args = append(args, owner, table.Table)
		parts = append(parts, "(OWNER="+placeholder(len(args)-1)+" AND TABLE_NAME="+placeholder(len(args))+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// Sample is a typed, bounded SELECT constructed entirely inside businessdb.
func Sample(ctx context.Context, executor Executor, table SchemaTable, columns []SampleColumn, limit int) (model.QueryResult, error) {
	if ctx == nil || isNilExecutor(executor) || len(columns) == 0 || len(columns) > maxResultColumns || limit <= 0 || limit > 1_000 {
		return model.QueryResult{}, resourceError("AUTH_REQUEST_TOO_LARGE")
	}
	dialect := executor.Dialect()
	tableName, err := quoteIdentifier(dialect, table.Table)
	if err != nil {
		return model.QueryResult{}, err
	}
	if table.Schema != "" {
		schema, quoteErr := quoteIdentifier(dialect, table.Schema)
		if quoteErr != nil {
			return model.QueryResult{}, quoteErr
		}
		tableName = schema + "." + tableName
	}
	projections := make([]string, len(columns))
	for index, column := range columns {
		if column.Table != table.Table || column.Schema != table.Schema {
			return model.QueryResult{}, fmt.Errorf("sample column is outside table")
		}
		projections[index], err = quoteIdentifier(dialect, column.Column)
		if err != nil {
			return model.QueryResult{}, err
		}
	}
	query := buildSampleQuery(dialect, tableName, projections, limit)
	return executor.Query(ctx, query, limit)
}

func buildSampleQuery(dialect, tableName string, projections []string, limit int) string {
	query := "SELECT " + strings.Join(projections, ",") + " FROM " + tableName
	if dialect == "oracle" {
		return query + " FETCH FIRST " + strconv.Itoa(limit) + " ROWS ONLY"
	}
	return query + " LIMIT " + strconv.Itoa(limit)
}
