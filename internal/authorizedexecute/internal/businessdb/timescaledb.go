package businessdb

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type TimescaleExecutor struct {
	conn                   *pgx.Conn
	database, datasourceID string
	readOnly               bool
}

func timescaleConfig(ds model.Datasource, password string, readOnly bool) (*pgx.ConnConfig, time.Duration, error) {
	if ds.DBType != "timescaledb" || strings.TrimSpace(ds.Host) == "" {
		return nil, 0, fmt.Errorf("timescaledb host is required")
	}
	if ds.Database == "" {
		return nil, 0, fmt.Errorf("timescaledb database is required")
	}
	if ds.Username == "" || password == "" {
		return nil, 0, fmt.Errorf("timescaledb username and password are required")
	}
	port := ds.Port
	if port == 0 {
		port = 5432
	}
	if port < 1 || port > 65535 {
		return nil, 0, fmt.Errorf("timescaledb port is invalid")
	}
	if ds.TrustServerCertificate {
		return nil, 0, fmt.Errorf("unverified timescaledb TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "disable" && ds.TLSMode != "require" && ds.TLSMode != "verify-full" && ds.TLSMode != "strict" {
		return nil, 0, fmt.Errorf("invalid timescaledb TLS mode")
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, 0, err
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(ds.Username, password), Host: net.JoinHostPort(ds.Host, strconv.Itoa(port)), Path: "/" + ds.Database}
	q := url.Values{}
	sslmode := ds.TLSMode
	if sslmode == "" || sslmode == "strict" {
		sslmode = "verify-full"
	}
	q.Set("sslmode", sslmode)
	if ds.TLSCAFile != "" {
		q.Set("sslrootcert", ds.TLSCAFile)
	}
	if ds.TLSServerName != "" && !strings.EqualFold(ds.TLSServerName, ds.Host) {
		return nil, 0, fmt.Errorf("timescaledb TLS server name must match host")
	}
	u.RawQuery = q.Encode()
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, 0, err
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["statement_timeout"] = strconv.Itoa(timeout)
	if readOnly {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.OnNotice = func(*pgconn.PgConn, *pgconn.Notice) {}
	return cfg, time.Duration(timeout) * time.Millisecond, nil
}
func NewTimescaleExecutor(ds model.Datasource, password string, readOnly bool) (*TimescaleExecutor, error) {
	cfg, timeout, err := timescaleConfig(ds, password, readOnly)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	e := &TimescaleExecutor{conn: conn, database: ds.Database, datasourceID: ds.ID, readOnly: readOnly}
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (*TimescaleExecutor) Category() model.DatasourceCategory { return model.CategoryTimeSeries }
func (*TimescaleExecutor) Dialect() model.DBDialect           { return "timescaledb" }
func (e *TimescaleExecutor) Close() error                     { return e.conn.Close(context.Background()) }
func (e *TimescaleExecutor) Ping(ctx context.Context) error {
	var n int
	return e.conn.QueryRow(ctx, "SELECT 1").Scan(&n)
}
func (e *TimescaleExecutor) ServerVersion(ctx context.Context) (string, error) {
	var pg string
	if err := e.conn.QueryRow(ctx, "SHOW server_version").Scan(&pg); err != nil {
		return "", err
	}
	var ext string
	err := e.conn.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname='timescaledb'").Scan(&ext)
	if err == pgx.ErrNoRows {
		return boundedNativeString("PostgreSQL " + pg + "; TimescaleDB extension unavailable"), nil
	}
	if err != nil {
		return "", err
	}
	return boundedNativeString("PostgreSQL " + pg + "; TimescaleDB " + ext), nil
}
func (e *TimescaleExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	c := NativeCatalog{Category: model.CategoryTimeSeries, ServerVersion: v}
	rows, err := e.conn.Query(ctx, "SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables LIMIT 1000")
	if err == nil {
		for rows.Next() {
			var schema, name string
			if err := rows.Scan(&schema, &name); err != nil {
				rows.Close()
				return c, err
			}
			c.Namespaces = append(c.Namespaces, NativeNamespace{Name: name, Kind: "hypertable", Metadata: map[string]string{"schema": schema, "counts": "row count unavailable"}})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return c, err
		}
	} else {
		c.Namespaces = append(c.Namespaces, NativeNamespace{Name: e.database, Kind: "database", Metadata: map[string]string{"hypertables": "unavailable (extension absent or insufficient privilege)", "counts": "unavailable"}})
	}
	rows, err = e.conn.Query(ctx, "SELECT schemaname, tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema','_timescaledb_internal') LIMIT 1000")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(c.Namespaces) >= nativeMaxRows {
			break
		}
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return c, err
		}
		c.Namespaces = append(c.Namespaces, NativeNamespace{Name: name, Kind: "table", Metadata: map[string]string{"schema": schema, "counts": "row count unavailable"}})
	}
	return c, rows.Err()
}

var timescaleLimit = regexp.MustCompile(`(?i)\bLIMIT\s+([0-9]+)\s*$`)
var timescaleCount = regexp.MustCompile(`(?i)^SELECT\s+COUNT\s*\(\s*\*\s*\)\s+FROM\s+[a-z_][a-z_0-9]*(?:\.[a-z_][a-z_0-9]*)?(?:\s+WHERE\s+[a-z_][a-z_0-9]*\s*=\s*(?:\$[0-9]+|[0-9]+|'[^']*'))?$`)
var timescaleIdentifier = `[a-zA-Z_][a-zA-Z_0-9]*`
var timescaleIndexCreate = regexp.MustCompile(`(?i)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+` + timescaleIdentifier + `\s+ON\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?\s*\(\s*` + timescaleIdentifier + `(?:\s*,\s*` + timescaleIdentifier + `)*\s*\)$`)
var timescaleIndexDrop = regexp.MustCompile(`(?i)^DROP\s+INDEX\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?$`)
var timescaleAddColumn = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?\s+ADD\s+COLUMN\s+` + timescaleIdentifier + `\s+(?:BIGINT|INTEGER|INT|DOUBLE PRECISION|BOOLEAN|TEXT|TIMESTAMPTZ|TIMESTAMP)$`)
var timescaleInsert = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?\s*\(` + timescaleIdentifier + `(?:\s*,\s*` + timescaleIdentifier + `)*\)\s+VALUES\s*\([^()]+\)$`)
var timescaleUpdate = regexp.MustCompile(`(?i)^UPDATE\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?\s+SET\s+` + timescaleIdentifier + `\s*=\s*(?:[0-9]+|'[^']*')\s+WHERE\s+` + timescaleIdentifier + `\s*=\s*(?:[0-9]+|'[^']*')$`)
var timescaleDelete = regexp.MustCompile(`(?i)^DELETE\s+FROM\s+` + timescaleIdentifier + `(?:\.` + timescaleIdentifier + `)?\s+WHERE\s+` + timescaleIdentifier + `\s*=\s*(?:[0-9]+|'[^']*')$`)
var timescaleCTE = regexp.MustCompile(`(?is)^WITH\s+` + timescaleIdentifier + `\s+AS\s*\(\s*SELECT\s+.+\)\s*SELECT\s+.+\s+LIMIT\s+[0-9]+$`)

func timescaleStatementAllowed(command, stmt string, readOnly bool) error {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || len(stmt) > nativeMaxValueBytes || strings.ContainsAny(stmt, ";\r\n") || strings.Contains(stmt, "--") || strings.Contains(stmt, "/*") || strings.Contains(stmt, "*/") || strings.Contains(stmt, "$") && strings.Contains(stmt, "$$") {
		return fmt.Errorf("invalid or multiple timescaledb statements")
	}
	// Quoted identifiers and dollar strings would require a full parser. Reject them.
	if strings.ContainsAny(stmt, "\"`\\") {
		return fmt.Errorf("quoted identifiers or escapes are unsupported")
	}
	mask := strings.ToUpper(stmt)
	fields := strings.Fields(mask)
	if len(fields) == 0 || fields[0] != command {
		return fmt.Errorf("timescaledb command mismatch")
	}
	if command == "SELECT" && strings.Contains(" "+mask+" ", " INTO ") {
		return fmt.Errorf("SELECT INTO is not allowed")
	}
	for _, bad := range []string{" COPY ", " GRANT ", " REVOKE ", " TRUNCATE ", " DROP TABLE ", " CREATE TABLE ", " CREATE EXTENSION ", " VACUUM ", " REINDEX ", " RETURNING ", " FOR UPDATE ", " FOR SHARE "} {
		if strings.Contains(" "+mask+" ", bad) {
			return fmt.Errorf("timescaledb clause is not allowed")
		}
	}
	limited := func() bool {
		m := timescaleLimit.FindStringSubmatch(stmt)
		if m == nil {
			return false
		}
		n, _ := strconv.Atoi(m[1])
		return n >= 1 && n <= nativeMaxRows
	}
	switch command {
	case "SELECT":
		if timescaleCount.MatchString(stmt) || limited() {
			return nil
		}
		return fmt.Errorf("SELECT requires LIMIT 1..1000")
	case "SHOW":
		if mask == "SHOW SERVER_VERSION" {
			return nil
		}
	case "EXPLAIN":
		if strings.HasPrefix(mask, "EXPLAIN SELECT ") && limited() && !strings.Contains(mask, "ANALYZE") {
			return nil
		}
	case "WITH":
		if timescaleCTE.MatchString(stmt) && limited() {
			for _, bad := range []string{" INSERT ", " UPDATE ", " DELETE ", " CREATE ", " DROP ", " COPY ", " CALL ", " EXECUTE ", " RECURSIVE "} {
				if strings.Contains(" "+mask+" ", bad) {
					return fmt.Errorf("WITH clause is not read-only")
				}
			}
			return nil
		}
	case "INSERT":
		if !readOnly && timescaleInsert.MatchString(stmt) && !strings.Contains(mask, " SELECT ") {
			return nil
		}
	case "UPDATE", "DELETE":
		if !readOnly && ((command == "UPDATE" && timescaleUpdate.MatchString(stmt)) || (command == "DELETE" && timescaleDelete.MatchString(stmt))) {
			return nil
		}
	case "CREATE":
		if !readOnly && timescaleIndexCreate.MatchString(stmt) {
			return nil
		}
	case "DROP":
		if !readOnly && timescaleIndexDrop.MatchString(stmt) {
			return nil
		}
	case "ALTER":
		if !readOnly && timescaleAddColumn.MatchString(stmt) {
			return nil
		}
	}
	if readOnly && (command == "INSERT" || command == "UPDATE" || command == "DELETE" || command == "CREATE" || command == "DROP" || command == "ALTER") {
		return fmt.Errorf("native write is forbidden in read-only mode")
	}
	return fmt.Errorf("timescaledb statement is not allowed")
}
func (e *TimescaleExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("timescaledb", e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if req.Namespace != "" && req.Namespace != e.database {
		return result, fmt.Errorf("native namespace differs from connected database")
	}
	if len(args) != 1 {
		return result, fmt.Errorf("timescaledb requires one SQL statement")
	}
	stmt := strings.TrimSpace(args[0])
	if err = timescaleStatementAllowed(command, stmt, e.readOnly); err != nil {
		return result, err
	}
	if command == "INSERT" || command == "UPDATE" || command == "DELETE" || command == "CREATE" || command == "DROP" || command == "ALTER" {
		tag, x := e.conn.Exec(ctx, stmt)
		if x != nil {
			return result, x
		}
		return NativeQueryResult{Raw: boundedNativeString(tag.String())}, nil
	}
	tx, x := e.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if x != nil {
		return result, x
	}
	defer tx.Rollback(ctx)
	rows, x := tx.Query(ctx, stmt)
	if x != nil {
		return result, x
	}
	defer rows.Close()
	cols := make([]string, len(rows.FieldDescriptions()))
	for i, fd := range rows.FieldDescriptions() {
		cols[i] = boundedNativeString(fd.Name)
	}
	data := make([][]string, 0)
	for rows.Next() {
		if len(data) >= nativeMaxRows {
			break
		}
		vals, x := rows.Values()
		if x != nil {
			return result, x
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = boundedNativeString(fmt.Sprint(v))
		}
		data = append(data, row)
	}
	if x := rows.Err(); x != nil {
		return result, x
	}
	rows.Close()
	if x := tx.Commit(ctx); x != nil {
		return result, x
	}
	return boundedNativeRows(cols, data), nil
}
