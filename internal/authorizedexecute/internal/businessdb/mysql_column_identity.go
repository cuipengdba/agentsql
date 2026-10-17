package businessdb

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// MySQLColumnIdentityInspector uses a separate PROCESS-only account to read
// InnoDB's stable TABLE_ID. It never receives or executes caller SQL.
type MySQLColumnIdentityInspector struct {
	database *sql.DB
	username string
}

type MySQLColumnIdentityFact struct {
	ServerUUID    string
	ServerVersion string
	TableID       int64
}

// ProbeServer proves the dedicated account's identity and PROCESS-only grants
// before a datasource is enrolled in the optional MySQL runtime.
func (inspector *MySQLColumnIdentityInspector) ProbeServer(ctx context.Context) (MySQLColumnIdentityFact, error) {
	if inspector == nil || inspector.database == nil || ctx == nil {
		return MySQLColumnIdentityFact{}, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_ACCOUNT_INVALID")
	}
	timed, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := inspector.database.Conn(timed)
	if err != nil {
		return MySQLColumnIdentityFact{}, mysqlDatabaseError(timed, DBStageAcquire, "acquire MySQL identity connection", err)
	}
	defer connection.Close()
	return inspector.probeServer(timed, connection)
}

func NewMySQLColumnIdentityInspector(ctx context.Context, datasource model.Datasource, username, password string) (*MySQLColumnIdentityInspector, error) {
	if ctx == nil || datasource.DBType != "mysql" || username == "" || strings.TrimSpace(username) != username || len(username) > 128 {
		return nil, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_ACCOUNT_INVALID")
	}
	target := datasource
	target.Username = username
	// A PROCESS-only account has no USAGE on the business schema. Connect to
	// the read-only catalog instead; all table lookups below are fully qualified.
	target.Database = "information_schema"
	dsn, err := buildMySQLDSN(target, password)
	if err != nil {
		return nil, safeError("build MySQL identity inspector configuration", ErrDatasourceUnreachable, err)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, mysqlConnectionError(ctx, DBStageConnect, "open MySQL identity inspector", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	inspector := &MySQLColumnIdentityInspector{database: database, username: username}
	timed, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := database.PingContext(timed); err != nil {
		_ = database.Close()
		return nil, mysqlConnectionError(timed, DBStagePing, "ping MySQL identity inspector", err)
	}
	return inspector, nil
}

// InspectTable returns the UUID and ID from one physical metadata connection.
// The caller must separately compare this UUID with the business transaction's
// server UUID while its metadata lock is held.
func (inspector *MySQLColumnIdentityInspector) InspectTable(ctx context.Context, schema, table string) (MySQLColumnIdentityFact, error) {
	var fact MySQLColumnIdentityFact
	if inspector == nil || inspector.database == nil || ctx == nil ||
		!mysqlColumnIdentityIdentifier(schema) || !mysqlColumnIdentityIdentifier(table) {
		return fact, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_TARGET_INVALID")
	}
	timed, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := inspector.database.Conn(timed)
	if err != nil {
		return fact, mysqlDatabaseError(timed, DBStageAcquire, "acquire MySQL identity connection", err)
	}
	defer connection.Close()
	fact, err = inspector.probeServer(timed, connection)
	if err != nil {
		return MySQLColumnIdentityFact{}, err
	}
	rows, err := connection.QueryContext(timed, `SELECT TABLE_ID FROM information_schema.INNODB_TABLES
WHERE BINARY NAME = BINARY ?`, schema+"/"+table)
	if err != nil {
		return MySQLColumnIdentityFact{}, mysqlDatabaseError(timed, DBStageQuery, "read InnoDB table identity", err)
	}
	defer rows.Close()
	if !rows.Next() || rows.Scan(&fact.TableID) != nil || fact.TableID <= 0 || rows.Next() || rows.Err() != nil {
		return MySQLColumnIdentityFact{}, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_UNPROVEN")
	}
	return fact, nil
}

func (inspector *MySQLColumnIdentityInspector) probeServer(ctx context.Context, connection *sql.Conn) (MySQLColumnIdentityFact, error) {
	var fact MySQLColumnIdentityFact
	var currentUser string
	if err := connection.QueryRowContext(ctx, `SELECT @@GLOBAL.server_uuid, VERSION(), CURRENT_USER()`).Scan(
		&fact.ServerUUID, &fact.ServerVersion, &currentUser,
	); err != nil {
		return MySQLColumnIdentityFact{}, mysqlDatabaseError(ctx, DBStageQuery, "read MySQL identity server", err)
	}
	if fact.ServerUUID == "" || !strings.HasPrefix(currentUser, inspector.username+"@") {
		return MySQLColumnIdentityFact{}, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_ACCOUNT_INVALID")
	}
	grants, err := readMySQLInspectorGrants(ctx, connection)
	if err != nil {
		return MySQLColumnIdentityFact{}, err
	}
	if !mysqlProcessOnlyGrants(grants) {
		return MySQLColumnIdentityFact{}, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_PRIVILEGE_INVALID")
	}
	return fact, nil
}

func (inspector *MySQLColumnIdentityInspector) Close() error {
	if inspector == nil || inspector.database == nil {
		return nil
	}
	return inspector.database.Close()
}

func mysqlProcessOnlyGrants(grants []string) bool {
	if len(grants) == 0 || len(grants) > 4 {
		return false
	}
	process := false
	for _, grant := range grants {
		upper := strings.ToUpper(strings.TrimSpace(grant))
		switch {
		case strings.HasPrefix(upper, "GRANT PROCESS ON *.* TO ") && !strings.Contains(upper, " WITH "):
			process = true
		case strings.HasPrefix(upper, "GRANT USAGE ON *.* TO ") && !strings.Contains(upper, " WITH "):
		default:
			return false
		}
	}
	return process
}

func mysqlColumnIdentityIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
