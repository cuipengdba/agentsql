package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	mysqldriver "github.com/go-sql-driver/mysql"
)

// DorisExecutor exposes only connection-level operations through the FE's
// MySQL protocol. StarRocks uses the same transport and conservative policy.
type DorisExecutor struct{ *olapMySQLExecutor }

type olapMySQLExecutor struct {
	db                     *sql.DB
	dialect                model.DBDialect
	datasourceID, database string
	readOnly               bool
}

func olapMySQLConfig(ds model.Datasource, password string) (*mysqldriver.Config, error) {
	if (ds.DBType != "doris" && ds.DBType != "starrocks") || strings.TrimSpace(ds.Host) == "" {
		return nil, fmt.Errorf("OLAP MySQL-protocol host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 9030
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("OLAP port is invalid")
	}
	if ds.Username == "" && password != "" {
		return nil, fmt.Errorf("password requires username")
	}
	if ds.Database != "" && !olapIdentifier.MatchString(ds.Database) {
		return nil, fmt.Errorf("invalid OLAP database")
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified OLAP TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "disable" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" && ds.TLSMode != "require" {
		return nil, fmt.Errorf("invalid OLAP TLS mode")
	}
	if (ds.TLSCAFile != "" || ds.TLSServerName != "") && (ds.TLSMode == "" || ds.TLSMode == "disable") {
		return nil, fmt.Errorf("OLAP TLS settings require TLS mode")
	}
	if ds.TLSMode == "require" {
		return nil, fmt.Errorf("OLAP require mode cannot verify server identity")
	}
	timeoutMS, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	cfg := mysqldriver.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net = ds.Username, password, "tcp"
	cfg.Addr = net.JoinHostPort(ds.Host, strconv.Itoa(port))
	cfg.DBName = ds.Database
	cfg.ParseTime = true
	cfg.MultiStatements = false
	cfg.Timeout = time.Duration(timeoutMS) * time.Millisecond
	cfg.ReadTimeout, cfg.WriteTimeout = cfg.Timeout, cfg.Timeout
	if ds.TLSMode != "" && ds.TLSMode != "disable" {
		serverName := ds.TLSServerName
		if serverName == "" {
			serverName = ds.Host
		}
		cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
		cfg.TLSConfig = "true"
		if ds.TLSCAFile != "" {
			pem, readErr := os.ReadFile(ds.TLSCAFile)
			if readErr != nil {
				return nil, readErr
			}
			roots, poolErr := x509.SystemCertPool()
			if poolErr != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid OLAP TLS CA")
			}
			cfg.TLS.RootCAs = roots
		}
	}
	return cfg, nil
}

func olapMySQLDSN(cfg *mysqldriver.Config) string {
	dsn := cfg.FormatDSN()
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "multiStatements=false"
}

func newOLAPMySQLExecutor(ds model.Datasource, password string, readOnly bool) (*olapMySQLExecutor, error) {
	cfg, err := olapMySQLConfig(ds, password)
	if err != nil {
		return nil, err
	}
	parsed, err := mysqldriver.ParseDSN(olapMySQLDSN(cfg))
	if err != nil {
		return nil, err
	}
	// A custom CA and server name cannot be represented by the DSN itself.
	parsed.TLS = cfg.TLS
	connector, err := mysqldriver.NewConnector(parsed)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	e := &olapMySQLExecutor{db: db, dialect: model.DBDialect(ds.DBType), datasourceID: ds.ID, database: ds.Database, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

func NewDorisExecutor(ds model.Datasource, password string, readOnly bool) (*DorisExecutor, error) {
	if ds.DBType != "doris" {
		return nil, fmt.Errorf("invalid Doris dialect")
	}
	e, err := newOLAPMySQLExecutor(ds, password, readOnly)
	if err != nil {
		return nil, err
	}
	return &DorisExecutor{e}, nil
}

func (*olapMySQLExecutor) Category() model.DatasourceCategory { return model.CategoryOLAP }
func (e *olapMySQLExecutor) Dialect() model.DBDialect         { return e.dialect }
func (e *olapMySQLExecutor) Close() error                     { return e.db.Close() }
func (e *olapMySQLExecutor) Ping(ctx context.Context) error {
	var n int
	if err := e.db.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("OLAP ping returned %d", n)
	}
	return nil
}
func (e *olapMySQLExecutor) ServerVersion(ctx context.Context) (string, error) {
	queries := []string{"SELECT current_version()"}
	if e.dialect == "doris" {
		queries = append(queries, "SELECT version()")
	}
	for _, query := range queries {
		var version string
		if err := e.db.QueryRowContext(ctx, query).Scan(&version); err == nil && version != "" {
			if reported := olapReportedVersion(version); reported != "" {
				return reported, nil
			}
		}
	}
	// FE implementations vary; absence of a version is not a connection failure.
	return "", nil
}

func olapReportedVersion(version string) string {
	version = strings.TrimSpace(version)
	// Doris and StarRocks can report a MySQL protocol compatibility version.
	// It is not the FE software version and must not be presented as one.
	if version == "" || strings.HasPrefix(version, "5.7.99") {
		return ""
	}
	return boundedNativeString(version)
}
func (e *olapMySQLExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, _ := e.ServerVersion(ctx)
	c := NativeCatalog{Category: model.CategoryOLAP, ServerVersion: version}
	rows, err := e.db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() && len(c.Namespaces) < nativeMaxRows {
		var name string
		if err := rows.Scan(&name); err != nil {
			return c, err
		}
		if !olapIdentifier.MatchString(name) {
			continue
		}
		c.Namespaces = append(c.Namespaces, NativeNamespace{Name: boundedNativeString(name), Kind: "database", Metadata: map[string]string{"counts": "row count unavailable"}})
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	// Close the outer cursor before issuing further commands on the single connection.
	if err := rows.Close(); err != nil {
		return c, err
	}
	databases := append([]NativeNamespace(nil), c.Namespaces...)
	for _, database := range databases {
		if len(c.Namespaces) >= nativeMaxRows {
			break
		}
		tables, err := e.db.QueryContext(ctx, "SHOW TABLES FROM `"+database.Name+"`")
		if err != nil {
			c.Namespaces = append(c.Namespaces, NativeNamespace{Name: database.Name, Kind: "metadata", Metadata: map[string]string{"tables": "unavailable (privilege or dialect)"}})
			continue
		}
		for tables.Next() && len(c.Namespaces) < nativeMaxRows {
			var table string
			if err := tables.Scan(&table); err != nil {
				tables.Close()
				return c, err
			}
			c.Namespaces = append(c.Namespaces, NativeNamespace{Name: boundedNativeString(table), Kind: "table", Metadata: map[string]string{"database": database.Name, "counts": "row count unavailable"}})
		}
		if err := tables.Err(); err != nil {
			tables.Close()
			return c, err
		}
		tables.Close()
	}
	return c, nil
}

var olapIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

const olapName = `[A-Za-z_][A-Za-z_0-9]*(?:\.[A-Za-z_][A-Za-z_0-9]*)?`
const olapValue = `(?:[0-9]+|'[^']{0,256}')`

var olapLimit = regexp.MustCompile(`(?i)\s+LIMIT\s+([0-9]+)$`)
var olapSelect = regexp.MustCompile(`(?i)^SELECT\s+(?:\*|[A-Za-z_][A-Za-z_0-9]*(?:\s*,\s*[A-Za-z_][A-Za-z_0-9]*)*)\s+FROM\s+` + olapName + `(?:\s+WHERE\s+[A-Za-z_][A-Za-z_0-9]*\s*=\s*` + olapValue + `)?\s+LIMIT\s+[0-9]+$`)
var olapAggregate = regexp.MustCompile(`(?i)^SELECT\s+(?:COUNT\s*\(\s*\*\s*\)|SUM\s*\(\s*[A-Za-z_][A-Za-z_0-9]*\s*\))\s+FROM\s+` + olapName + `(?:\s+WHERE\s+[A-Za-z_][A-Za-z_0-9]*\s*=\s*` + olapValue + `)?$`)
var olapShow = regexp.MustCompile(`(?i)^SHOW\s+(?:DATABASES|TABLES(?:\s+FROM\s+` + olapName + `)?|CREATE\s+TABLE\s+` + olapName + `|COLUMN\s+STATS\s+` + olapName + `)$`)
var olapDescribe = regexp.MustCompile(`(?i)^DESCRIBE\s+` + olapName + `$`)
var olapInsert = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+` + olapName + `\s*\(\s*[A-Za-z_][A-Za-z_0-9]*(?:\s*,\s*[A-Za-z_][A-Za-z_0-9]*)*\s*\)\s+VALUES\s*\(\s*` + olapValue + `(?:\s*,\s*` + olapValue + `)*\s*\)$`)
var olapUpdate = regexp.MustCompile(`(?i)^UPDATE\s+` + olapName + `\s+SET\s+[A-Za-z_][A-Za-z_0-9]*\s*=\s*` + olapValue + `\s+WHERE\s+[A-Za-z_][A-Za-z_0-9]*\s*=\s*` + olapValue + `$`)
var olapDelete = regexp.MustCompile(`(?i)^DELETE\s+FROM\s+` + olapName + `\s+WHERE\s+[A-Za-z_][A-Za-z_0-9]*\s*=\s*` + olapValue + `$`)
var olapAddColumn = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+` + olapName + `\s+ADD\s+COLUMN\s+[A-Za-z_][A-Za-z_0-9]*\s+(?:INT|BIGINT|BOOLEAN|DOUBLE|FLOAT|DATE|DATETIME|VARCHAR\([1-9][0-9]{0,3}\))$`)
var olapInsertSelectPrefix = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+` + olapName + `\s*\(\s*[A-Za-z_][A-Za-z_0-9]*(?:\s*,\s*[A-Za-z_][A-Za-z_0-9]*)*\s*\)\s+(SELECT\s+.+)$`)

func olapBoundedInsertSelect(statement string) bool {
	match := olapInsertSelectPrefix.FindStringSubmatch(statement)
	return match != nil && olapMySQLStatementAllowed("SELECT", match[1], true) == nil
}

func olapMySQLStatementAllowed(command, statement string, readOnly bool) error {
	statement = strings.TrimSpace(statement)
	if statement == "" || len(statement) > nativeMaxValueBytes || strings.ContainsAny(statement, ";\r\n`\\\x00") || strings.Contains(statement, "--") || strings.Contains(statement, "/*") || strings.Contains(statement, "*/") || strings.Contains(statement, "#") || strings.Contains(statement, "\"") {
		return fmt.Errorf("invalid or multiple OLAP statements")
	}
	fields := strings.Fields(statement)
	if len(fields) == 0 || !strings.EqualFold(fields[0], command) {
		return fmt.Errorf("OLAP command mismatch")
	}
	allowed := false
	switch command {
	case "SELECT":
		if olapAggregate.MatchString(statement) {
			allowed = true
		} else if olapSelect.MatchString(statement) {
			match := olapLimit.FindStringSubmatch(statement)
			if match != nil {
				n, _ := strconv.Atoi(match[1])
				allowed = n > 0 && n <= nativeMaxRows
			}
		}
	case "SHOW":
		allowed = olapShow.MatchString(statement)
	case "DESCRIBE":
		allowed = olapDescribe.MatchString(statement)
	case "EXPLAIN":
		allowed = strings.HasPrefix(strings.ToUpper(statement), "EXPLAIN SELECT ") && olapMySQLStatementAllowed("SELECT", strings.TrimSpace(statement[len("EXPLAIN "):]), true) == nil
	case "INSERT":
		allowed = !readOnly && (olapInsert.MatchString(statement) || olapBoundedInsertSelect(statement))
	case "UPDATE":
		allowed = !readOnly && olapUpdate.MatchString(statement)
	case "DELETE":
		allowed = !readOnly && olapDelete.MatchString(statement)
	case "ALTER":
		allowed = !readOnly && olapAddColumn.MatchString(statement)
	}
	if allowed {
		return nil
	}
	if readOnly && (command == "INSERT" || command == "UPDATE" || command == "DELETE" || command == "ALTER") {
		return fmt.Errorf("native write is forbidden in read-only mode")
	}
	return fmt.Errorf("OLAP statement is not allowed")
}

func (e *olapMySQLExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.dialect, e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if req.Namespace != "" && req.Namespace != e.database {
		return result, fmt.Errorf("native namespace differs from connected database")
	}
	if len(args) != 1 {
		return result, fmt.Errorf("OLAP requires one SQL statement")
	}
	statement := strings.TrimSpace(args[0])
	if err = olapMySQLStatementAllowed(command, statement, e.readOnly); err != nil {
		return result, err
	}
	if command == "INSERT" || command == "UPDATE" || command == "DELETE" || command == "ALTER" {
		var x sql.Result
		x, err = e.db.ExecContext(ctx, statement)
		if err != nil {
			return result, err
		}
		n, countErr := x.RowsAffected()
		if countErr == nil {
			result.Info = map[string]string{"affected_rows": strconv.FormatInt(n, 10)}
		}
		return result, nil
	}
	return queryOLAPRows(ctx, e.db, statement)
}

func queryOLAPRows(ctx context.Context, db *sql.DB, statement string) (NativeQueryResult, error) {
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return NativeQueryResult{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return NativeQueryResult{}, err
	}
	for i := range columns {
		columns[i] = boundedNativeString(columns[i])
	}
	result := NativeQueryResult{Columns: columns, Rows: make([][]string, 0)}
	for rows.Next() {
		if len(result.Rows) >= nativeMaxRows {
			result.Info = map[string]string{"truncated": "true"}
			break
		}
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return NativeQueryResult{}, err
		}
		line := make([]string, len(values))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				line[i] = boundedNativeString(string(b))
			} else if v != nil {
				line[i] = boundedNativeString(fmt.Sprint(v))
			}
		}
		result.Rows = append(result.Rows, line)
	}
	return result, rows.Err()
}
