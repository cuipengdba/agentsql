package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cuipengdba/agentsql/internal/model"
)

// ClickHouseReadRequest is a deliberately smaller capability than native SQL.
// Names are validated before they enter the server-built statement; values are
// passed to the driver as parameters.
type ClickHouseReadRequest struct {
	Table  string
	Fields []string
	Filter map[string]string
	Limit  int
}

var clickHouseControlledIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]{0,63}$`)

func ValidateClickHouseRead(request ClickHouseReadRequest) error {
	if !clickHouseControlledIdentifier.MatchString(request.Table) ||
		len(request.Fields) == 0 || len(request.Fields) > 16 ||
		len(request.Filter) == 0 || len(request.Filter) > 8 || request.Limit < 1 || request.Limit > 100 {
		return errors.New("invalid controlled ClickHouse request")
	}
	seen := make(map[string]bool, len(request.Fields))
	for _, field := range request.Fields {
		if !clickHouseControlledIdentifier.MatchString(field) || seen[field] {
			return errors.New("invalid controlled ClickHouse field")
		}
		seen[field] = true
	}
	for field, value := range request.Filter {
		if !clickHouseControlledIdentifier.MatchString(field) || len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) ||
			strings.ContainsRune(value, 0) {
			return errors.New("invalid controlled ClickHouse filter")
		}
	}
	return nil
}

// ClickHouseReadControlled opens a fresh connection so datasource edits take
// effect on the next request. The caller supplies a bounded context and
// persists its preflight audit before entering this function.
func ClickHouseReadControlled(ctx context.Context, ds model.Datasource, password string, request ClickHouseReadRequest) (model.QueryResult, error) {
	if err := ValidateClickHouseRead(request); err != nil {
		return model.QueryResult{}, err
	}
	if ds.DBType != "clickhouse" || ds.Database == "" || !clickHouseControlledIdentifier.MatchString(ds.Database) ||
		(ds.TLSMode != "strict" && ds.TLSMode != "verify-full") || ds.TrustServerCertificate ||
		ds.TLSCAFile == "" || ds.Username == "" || password == "" {
		return model.QueryResult{}, errors.New("controlled ClickHouse connection is unavailable")
	}
	opts, err := clickHouseOptions(ds, password)
	if err != nil {
		return model.QueryResult{}, err
	}
	db := clickhouse.OpenDB(opts)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return model.QueryResult{}, err
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil || version != "26.8.31.2" {
		return model.QueryResult{}, errors.New("unverified ClickHouse server version")
	}
	var readOnlySetting string
	if err := db.QueryRowContext(ctx, "SELECT toString(getSetting('readonly'))").Scan(&readOnlySetting); err != nil || readOnlySetting != "1" {
		return model.QueryResult{}, errors.New("ClickHouse account must enforce readonly=1")
	}
	var engine string
	if err := db.QueryRowContext(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = ?", ds.Database, request.Table).Scan(&engine); err != nil || engine != "MergeTree" {
		return model.QueryResult{}, errors.New("ClickHouse table engine is not allowed")
	}
	columns, err := controlledClickHouseColumns(ctx, db, ds.Database, request.Table)
	if err != nil {
		return model.QueryResult{}, err
	}
	for _, field := range request.Fields {
		if !controlledClickHouseOutputType(columns[field]) {
			return model.QueryResult{}, errors.New("ClickHouse output column is not allowed")
		}
	}
	filters := make([]string, 0, len(request.Filter))
	for field := range request.Filter {
		if columns[field] != "String" {
			return model.QueryResult{}, errors.New("ClickHouse filter column must be String")
		}
		filters = append(filters, field)
	}
	sort.Strings(filters)
	projection := make([]string, len(request.Fields))
	for i, field := range request.Fields {
		projection[i] = "`" + field + "`"
	}
	conditions := make([]string, len(filters))
	args := make([]any, len(filters))
	for i, field := range filters {
		conditions[i] = "`" + field + "` = ?"
		args[i] = request.Filter[field]
	}
	statement := fmt.Sprintf("SELECT %s FROM `%s`.`%s` WHERE %s LIMIT %d SETTINGS max_execution_time=2, max_rows_to_read=10000, max_bytes_to_read=1048576, max_memory_usage=134217728, max_result_rows=100, max_threads=2", strings.Join(projection, ","), ds.Database, request.Table, strings.Join(conditions, " AND "), request.Limit)
	start := time.Now()
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return model.QueryResult{}, err
	}
	defer rows.Close()
	actual, err := rows.Columns()
	if err != nil || len(actual) != len(request.Fields) {
		return model.QueryResult{}, errors.New("ClickHouse result columns differ from request")
	}
	for i, field := range request.Fields {
		if actual[i] != field {
			return model.QueryResult{}, errors.New("ClickHouse result columns differ from request")
		}
	}
	result := model.QueryResult{Columns: append([]string(nil), request.Fields...), Rows: make([][]string, 0, request.Limit)}
	totalBytes := 0
	for rows.Next() {
		if len(result.Rows) >= request.Limit {
			return model.QueryResult{}, errors.New("ClickHouse result exceeds row limit")
		}
		values := make([]any, len(actual))
		dest := make([]any, len(actual))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return model.QueryResult{}, err
		}
		line := make([]string, len(values))
		for i, value := range values {
			if value == nil {
				return model.QueryResult{}, errors.New("ClickHouse null output is unsupported")
			}
			if bytes, ok := value.([]byte); ok {
				line[i] = string(bytes)
			} else {
				line[i] = fmt.Sprint(value)
			}
			totalBytes += len(line[i])
			if len(line[i]) > 64*1024 || totalBytes > 1024*1024 || !utf8.ValidString(line[i]) {
				return model.QueryResult{}, errors.New("ClickHouse result exceeds byte limit")
			}
		}
		result.Rows = append(result.Rows, line)
	}
	if err := rows.Err(); err != nil {
		return model.QueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	result.LatencyMS = time.Since(start).Milliseconds()
	return result, nil
}

func controlledClickHouseColumns(ctx context.Context, db *sql.DB, database, table string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name, type FROM system.columns WHERE database = ? AND table = ?", database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]string)
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return nil, err
		}
		columns[name] = dataType
	}
	return columns, rows.Err()
}

func controlledClickHouseOutputType(dataType string) bool {
	switch dataType {
	case "String", "UInt8", "UInt16", "UInt32", "UInt64", "Int8", "Int16", "Int32", "Int64", "Float32", "Float64", "Date", "DateTime", "UUID":
		return true
	default:
		return false
	}
}
