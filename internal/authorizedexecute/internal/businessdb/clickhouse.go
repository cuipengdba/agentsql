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

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cuipengdba/agentsql/internal/model"
)

type ClickHouseExecutor struct {
	db                     *sql.DB
	datasourceID, database string
	readOnly               bool
}

func clickHouseOptions(ds model.Datasource, password string) (*clickhouse.Options, error) {
	if ds.DBType != "clickhouse" || strings.TrimSpace(ds.Host) == "" {
		return nil, fmt.Errorf("clickhouse host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 9000
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("clickhouse port is invalid")
	}
	if ds.Username == "" && password != "" {
		return nil, fmt.Errorf("clickhouse password requires username")
	}
	if ds.Database != "" && !olapIdentifier.MatchString(ds.Database) {
		return nil, fmt.Errorf("invalid clickhouse database")
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified clickhouse TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "disable" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" && ds.TLSMode != "require" {
		return nil, fmt.Errorf("invalid clickhouse TLS mode")
	}
	if ds.TLSMode == "require" {
		return nil, fmt.Errorf("clickhouse require mode cannot verify server identity")
	}
	if (ds.TLSCAFile != "" || ds.TLSServerName != "") && (ds.TLSMode == "" || ds.TLSMode == "disable") {
		return nil, fmt.Errorf("clickhouse TLS settings require TLS mode")
	}
	timeoutMS, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	database := ds.Database
	if database == "" {
		database = "default"
	}
	username := ds.Username
	if username == "" {
		username = "default"
	}
	opts := &clickhouse.Options{
		Addr:        []string{net.JoinHostPort(ds.Host, strconv.Itoa(port))},
		Auth:        clickhouse.Auth{Database: database, Username: username, Password: password},
		DialTimeout: time.Duration(timeoutMS) * time.Millisecond,
		Protocol:    clickhouse.Native,
	}
	if ds.TLSMode != "" && ds.TLSMode != "disable" {
		serverName := ds.TLSServerName
		if serverName == "" {
			serverName = ds.Host
		}
		opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
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
				return nil, fmt.Errorf("invalid clickhouse TLS CA")
			}
			opts.TLS.RootCAs = roots
		}
	}
	return opts, nil
}

func NewClickHouseExecutor(ds model.Datasource, password string, readOnly bool) (*ClickHouseExecutor, error) {
	opts, err := clickHouseOptions(ds, password)
	if err != nil {
		return nil, err
	}
	db := clickhouse.OpenDB(opts)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	e := &ClickHouseExecutor{db: db, datasourceID: ds.ID, database: opts.Auth.Database, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), opts.DialTimeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}
func (*ClickHouseExecutor) Category() model.DatasourceCategory { return model.CategoryOLAP }
func (*ClickHouseExecutor) Dialect() model.DBDialect           { return "clickhouse" }
func (e *ClickHouseExecutor) Close() error                     { return e.db.Close() }
func (e *ClickHouseExecutor) Ping(ctx context.Context) error {
	var n int
	if err := e.db.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("clickhouse ping returned %d", n)
	}
	return nil
}
func (e *ClickHouseExecutor) ServerVersion(ctx context.Context) (string, error) {
	var version string
	err := e.db.QueryRowContext(ctx, "SELECT version()").Scan(&version)
	return boundedNativeString(version), err
}
func (e *ClickHouseExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, _ := e.ServerVersion(ctx)
	c := NativeCatalog{Category: model.CategoryOLAP, ServerVersion: version}
	rows, err := e.db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return c, err
	}
	for rows.Next() && len(c.Namespaces) < nativeMaxRows {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return c, err
		}
		if !olapIdentifier.MatchString(name) {
			continue
		}
		c.Namespaces = append(c.Namespaces, NativeNamespace{Name: boundedNativeString(name), Kind: "database", Metadata: map[string]string{"counts": "row count unavailable"}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return c, err
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	databases := append([]NativeNamespace(nil), c.Namespaces...)
	for _, database := range databases {
		if len(c.Namespaces) >= nativeMaxRows {
			break
		}
		tables, err := e.db.QueryContext(ctx, "SHOW TABLES FROM "+database.Name)
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

var clickHouseShow = regexp.MustCompile(`(?i)^SHOW\s+(?:DATABASES|TABLES(?:\s+FROM\s+` + olapName + `)?|CREATE\s+TABLE\s+` + olapName + `)$`)
var clickHouseExists = regexp.MustCompile(`(?i)^EXISTS\s+(?:TABLE\s+)?` + olapName + `$`)
var clickHouseInsert = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+` + olapName + `\s*\(\s*[A-Za-z_][A-Za-z_0-9]*(?:\s*,\s*[A-Za-z_][A-Za-z_0-9]*)*\s*\)\s+VALUES\s*\(\s*` + olapValue + `(?:\s*,\s*` + olapValue + `)*\s*\)$`)
var clickHouseAddColumn = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+` + olapName + `\s+ADD\s+COLUMN\s+[A-Za-z_][A-Za-z_0-9]*\s+(?:UInt8|UInt16|UInt32|UInt64|Int32|Int64|String|Float64|Date|DateTime)$`)
var clickHouseModifyComment = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+` + olapName + `\s+MODIFY\s+COMMENT\s+'[^']{0,256}'$`)
var clickHouseOptimize = regexp.MustCompile(`(?i)^OPTIMIZE\s+TABLE\s+` + olapName + `\s+PARTITION\s+` + olapValue + `$`)
var clickHouseDropPartition = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+` + olapName + `\s+DROP\s+PARTITION\s+` + olapValue + `$`)

func clickHouseStatementAllowed(command, statement string, readOnly bool) error {
	statement = strings.TrimSpace(statement)
	if statement == "" || len(statement) > nativeMaxValueBytes || strings.ContainsAny(statement, ";\r\n`\\\x00") || strings.Contains(statement, "--") || strings.Contains(statement, "/*") || strings.Contains(statement, "*/") || strings.Contains(statement, "#") || strings.Contains(statement, "\"") {
		return fmt.Errorf("invalid or multiple clickhouse statements")
	}
	fields := strings.Fields(statement)
	if len(fields) == 0 || !strings.EqualFold(fields[0], command) {
		return fmt.Errorf("clickhouse command mismatch")
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
		allowed = clickHouseShow.MatchString(statement)
	case "DESCRIBE":
		allowed = olapDescribe.MatchString(statement)
	case "EXISTS":
		allowed = clickHouseExists.MatchString(statement)
	case "INSERT":
		allowed = !readOnly && (clickHouseInsert.MatchString(statement) || olapBoundedInsertSelect(statement))
	case "ALTER":
		allowed = !readOnly && (clickHouseAddColumn.MatchString(statement) || clickHouseModifyComment.MatchString(statement) || clickHouseDropPartition.MatchString(statement))
	case "OPTIMIZE":
		allowed = !readOnly && clickHouseOptimize.MatchString(statement)
	}
	if allowed {
		return nil
	}
	if readOnly && (command == "INSERT" || command == "ALTER" || command == "OPTIMIZE") {
		return fmt.Errorf("native write is forbidden in read-only mode")
	}
	return fmt.Errorf("clickhouse statement is not allowed")
}
func (e *ClickHouseExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("clickhouse", e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if req.Namespace != "" && req.Namespace != e.database {
		return result, fmt.Errorf("native namespace differs from connected database")
	}
	if len(args) != 1 {
		return result, fmt.Errorf("clickhouse requires one SQL statement")
	}
	statement := strings.TrimSpace(args[0])
	if err = clickHouseStatementAllowed(command, statement, e.readOnly); err != nil {
		return result, err
	}
	if command == "INSERT" || command == "ALTER" || command == "OPTIMIZE" {
		_, err = e.db.ExecContext(ctx, statement)
		return result, err
	}
	return queryOLAPRows(ctx, e.db, statement)
}
