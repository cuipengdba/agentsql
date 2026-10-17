package businessdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// MySQLBoundColumn and MySQLBoundCatalog contain only identity facts. A
// catalog obtained without a live PreparedMySQLColumnSelect is not an
// authorization proof.
type MySQLBoundColumn struct {
	Ordinal    int
	Name       string
	TypeDigest string
}

type MySQLBoundCatalog struct {
	ServerUUID  string
	TableID     int64
	Schema      string
	Table       string
	Fingerprint string
	Columns     []MySQLBoundColumn
}

// PreparedMySQLColumnSelect owns the only business connection and transaction
// for a column-authorized request. The first fixed SELECT holds an MDL shared
// read lock until Close, covering discovery, execution, and Fpost.
type PreparedMySQLColumnSelect struct {
	executor  *MySQLExecutor
	conn      *sql.Conn
	tx        *sql.Tx
	inspector *MySQLColumnIdentityInspector
	shape     parser.MySQLColumnSelectShape
	sqlText   string
	catalog   MySQLBoundCatalog
	closed    bool
}

// MySQLColumnCatalogLease is a fixed-query, non-executable catalog lock for
// policy enrollment. Its caller can read identities and release the MDL lock,
// but cannot pass SQL or fetch customer rows through this capability.
type MySQLColumnCatalogLease struct{ prepared *PreparedMySQLColumnSelect }

func (executor *MySQLExecutor) BindMySQLColumnCatalog(ctx context.Context, schema, table string,
	inspector *MySQLColumnIdentityInspector,
) (*MySQLColumnCatalogLease, error) {
	if !mysqlColumnIdentityIdentifier(schema) || !mysqlColumnIdentityIdentifier(table) {
		return nil, NewCapabilityFailure("AUTH_MYSQL_IDENTITY_TARGET_INVALID")
	}
	shape := parser.MySQLColumnSelectShape{Schema: schema, Table: table,
		Uses: []parser.MySQLColumnUse{{Name: "_catalog_probe", Usage: "reference", Site: "catalog", OutputIndex: -1}}}
	prepared, err := executor.PrepareMySQLColumnSelect(ctx, "SELECT 1", shape, inspector)
	if err != nil {
		return nil, err
	}
	return &MySQLColumnCatalogLease{prepared: prepared}, nil
}

func (lease *MySQLColumnCatalogLease) Catalog() MySQLBoundCatalog {
	if lease == nil || lease.prepared == nil {
		return MySQLBoundCatalog{}
	}
	return lease.prepared.Catalog()
}

func (lease *MySQLColumnCatalogLease) Close() error {
	if lease == nil || lease.prepared == nil {
		return nil
	}
	return lease.prepared.Close(context.Background(), false)
}

func (executor *MySQLExecutor) PrepareMySQLColumnSelect(ctx context.Context, sqlText string,
	shape parser.MySQLColumnSelectShape, inspector *MySQLColumnIdentityInspector,
) (_ *PreparedMySQLColumnSelect, returnedErr error) {
	if executor == nil || executor.database == nil || !executor.readOnly || ctx == nil || inspector == nil ||
		!mysqlColumnIdentityIdentifier(shape.Table) || shape.Schema != "" && !mysqlColumnIdentityIdentifier(shape.Schema) ||
		len(shape.Uses) == 0 || sqlText == "" {
		return nil, NewCapabilityFailure("AUTH_MYSQL_COLUMN_BINDER_UNAVAILABLE")
	}
	if shape.Schema == "" {
		return nil, NewCapabilityFailure("AUTH_MYSQL_COLUMN_SCHEMA_UNPROVEN")
	}
	connection, err := executor.database.Conn(ctx)
	if err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageAcquire, "acquire MySQL column connection", err)
	}
	prepared := &PreparedMySQLColumnSelect{executor: executor, conn: connection, inspector: inspector,
		shape: shape, sqlText: sqlText}
	defer func() {
		if returnedErr != nil {
			_ = prepared.Close(context.Background(), false)
		}
	}()
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageBeginTx, "begin MySQL read-only column transaction", err)
	}
	prepared.tx = tx
	// A fresh pool and a fixed, fully-qualified query exclude an old temporary
	// table from replacing the business object. This SELECT holds the MDL lock.
	lockSQL := "SELECT 1 FROM " + quoteMySQLInspectorIdentifier(shape.Schema) + "." +
		quoteMySQLInspectorIdentifier(shape.Table) + " LIMIT 0"
	rows, err := tx.QueryContext(ctx, lockSQL)
	if err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageQuery, "lock MySQL base table", err)
	}
	if err := rows.Close(); err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageReadRows, "close MySQL lock query", err)
	}
	prepared.catalog, err = prepared.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (prepared *PreparedMySQLColumnSelect) Catalog() MySQLBoundCatalog {
	if prepared == nil {
		return MySQLBoundCatalog{}
	}
	copyValue := prepared.catalog
	copyValue.Columns = append([]MySQLBoundColumn(nil), prepared.catalog.Columns...)
	return copyValue
}

func (prepared *PreparedMySQLColumnSelect) Execute(ctx context.Context, rowLimit int) (model.QueryResult, error) {
	if prepared == nil || prepared.closed || prepared.tx == nil || ctx == nil || rowLimit <= 0 {
		return model.QueryResult{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_EXECUTION_UNAVAILABLE")
	}
	started := time.Now()
	timed, cancel := prepared.executor.timeoutContext(ctx)
	defer cancel()
	rows, err := prepared.tx.QueryContext(timed, prepared.sqlText)
	if err != nil {
		return model.QueryResult{}, mysqlDatabaseError(timed, DBStageQuery, "execute MySQL bound SELECT", err)
	}
	result, err := collectRows(&mysqlRowSource{rows: rows}, rowLimit)
	if err != nil {
		return model.QueryResult{}, mysqlDatabaseError(timed, DBStageReadRows, "read MySQL bound SELECT", err)
	}
	if len(result.Columns) != len(prepared.shape.Outputs) {
		return model.QueryResult{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_OUTPUT_MISMATCH")
	}
	for index, column := range result.Columns {
		if column != prepared.shape.Outputs[index] {
			return model.QueryResult{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_OUTPUT_MISMATCH")
		}
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

func (prepared *PreparedMySQLColumnSelect) VerifyPost(ctx context.Context) (string, error) {
	if prepared == nil || prepared.closed || prepared.tx == nil {
		return "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_FPOST_UNAVAILABLE")
	}
	post, err := prepared.readCatalog(ctx)
	if err != nil {
		return "", err
	}
	if post.Fingerprint != prepared.catalog.Fingerprint {
		return "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_CATALOG_CHANGED")
	}
	return post.Fingerprint, nil
}

func (prepared *PreparedMySQLColumnSelect) Close(_ context.Context, allowed bool) error {
	if prepared == nil || prepared.closed {
		return nil
	}
	prepared.closed = true
	var txErr, connErr error
	if prepared.tx != nil {
		if allowed {
			txErr = prepared.tx.Commit()
		} else {
			txErr = prepared.tx.Rollback()
		}
	}
	if prepared.conn != nil {
		connErr = prepared.conn.Close()
	}
	if err := errors.Join(txErr, connErr); err != nil {
		return mysqlDatabaseError(context.Background(), DBStageCommit, "close MySQL column transaction", err)
	}
	return nil
}

func (prepared *PreparedMySQLColumnSelect) readCatalog(ctx context.Context) (MySQLBoundCatalog, error) {
	if ctx == nil || prepared == nil || prepared.tx == nil {
		return MySQLBoundCatalog{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_CATALOG_UNAVAILABLE")
	}
	var catalog MySQLBoundCatalog
	catalog.Schema, catalog.Table = prepared.shape.Schema, prepared.shape.Table
	var version string
	var err error
	catalog.ServerUUID, version, err = probeMySQLColumnBusiness(ctx, prepared.tx)
	if err != nil {
		return MySQLBoundCatalog{}, err
	}
	var tableType, engine string
	if err := prepared.tx.QueryRowContext(ctx, `SELECT TABLE_TYPE, ENGINE FROM information_schema.TABLES
WHERE BINARY TABLE_SCHEMA=BINARY ? AND BINARY TABLE_NAME=BINARY ?`, catalog.Schema, catalog.Table).Scan(
		&tableType, &engine,
	); err != nil {
		return MySQLBoundCatalog{}, mysqlDatabaseError(ctx, DBStageMetadata, "read MySQL base table type", err)
	}
	if tableType != "BASE TABLE" || engine != "InnoDB" {
		return MySQLBoundCatalog{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_OBJECT_UNSUPPORTED")
	}
	rows, err := prepared.tx.QueryContext(ctx, `SELECT ORDINAL_POSITION,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,EXTRA,
COALESCE(GENERATION_EXPRESSION,''),COALESCE(CHARACTER_SET_NAME,''),COALESCE(COLLATION_NAME,'')
FROM information_schema.COLUMNS
WHERE BINARY TABLE_SCHEMA=BINARY ? AND BINARY TABLE_NAME=BINARY ? ORDER BY ORDINAL_POSITION`,
		catalog.Schema, catalog.Table)
	if err != nil {
		return MySQLBoundCatalog{}, mysqlDatabaseError(ctx, DBStageMetadata, "read MySQL column catalog", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal int
		var name, columnType, nullable, extra, generation, charset, collation string
		if err := rows.Scan(&ordinal, &name, &columnType, &nullable, &extra, &generation, &charset, &collation); err != nil {
			return MySQLBoundCatalog{}, mysqlDatabaseError(ctx, DBStageMetadata, "scan MySQL column catalog", err)
		}
		if ordinal != len(catalog.Columns)+1 || len(catalog.Columns) >= 256 || !mysqlColumnIdentityIdentifier(name) ||
			columnType == "" || generation != "" || strings.Contains(strings.ToUpper(extra), "GENERATED") {
			return MySQLBoundCatalog{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_CATALOG_UNPROVEN")
		}
		serialized, _ := json.Marshal([]any{columnType, nullable, charset, collation})
		digest := sha256.Sum256(serialized)
		catalog.Columns = append(catalog.Columns, MySQLBoundColumn{Ordinal: ordinal, Name: name,
			TypeDigest: hex.EncodeToString(digest[:])})
	}
	if rows.Err() != nil || len(catalog.Columns) == 0 {
		return MySQLBoundCatalog{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_CATALOG_UNPROVEN")
	}
	identity, err := prepared.inspector.InspectTable(ctx, catalog.Schema, catalog.Table)
	if err != nil {
		return MySQLBoundCatalog{}, err
	}
	if identity.ServerUUID != catalog.ServerUUID || identity.TableID <= 0 || identity.ServerVersion != version {
		return MySQLBoundCatalog{}, NewCapabilityFailure("AUTH_MYSQL_COLUMN_INSTANCE_MISMATCH")
	}
	catalog.TableID = identity.TableID
	serialized, _ := json.Marshal(catalog)
	digest := sha256.Sum256(serialized)
	catalog.Fingerprint = hex.EncodeToString(digest[:])
	return catalog, nil
}

// ProbeMySQLColumnBusiness checks the exact account that will execute SELECT.
// It is intentionally narrower than the existing table-level MySQL support.
func (executor *MySQLExecutor) ProbeMySQLColumnBusiness(ctx context.Context) (string, string, error) {
	if executor == nil || executor.database == nil || !executor.readOnly || ctx == nil {
		return "", "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_BUSINESS_UNAVAILABLE")
	}
	connection, err := executor.database.Conn(ctx)
	if err != nil {
		return "", "", mysqlDatabaseError(ctx, DBStageAcquire, "acquire MySQL business identity connection", err)
	}
	defer connection.Close()
	return probeMySQLColumnBusiness(ctx, connection)
}

func probeMySQLColumnBusiness(ctx context.Context, runner mysqlRunner) (string, string, error) {
	var uuid, version, currentUser string
	var lowerCase int
	if err := runner.QueryRowContext(ctx, `SELECT @@GLOBAL.server_uuid, VERSION(), @@lower_case_table_names, CURRENT_USER()`).Scan(
		&uuid, &version, &lowerCase, &currentUser,
	); err != nil {
		return "", "", mysqlDatabaseError(ctx, DBStageMetadata, "read MySQL business server", err)
	}
	if uuid == "" || currentUser == "" || lowerCase != 0 ||
		!(strings.HasPrefix(version, "8.0.") || strings.HasPrefix(version, "8.4.")) {
		return "", "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_SERVER_UNSUPPORTED")
	}
	rows, err := runner.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return "", "", mysqlDatabaseError(ctx, DBStageMetadata, "read MySQL business grants", err)
	}
	defer rows.Close()
	var grantCount int
	var selectGrant bool
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return "", "", mysqlDatabaseError(ctx, DBStageMetadata, "scan MySQL business grant", err)
		}
		grantCount++
		upper := strings.ToUpper(strings.TrimSpace(grant))
		if grantCount > 16 || strings.Contains(upper, " WITH ") {
			return "", "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_BUSINESS_PRIVILEGE_INVALID")
		}
		switch {
		case strings.HasPrefix(upper, "GRANT SELECT ON "):
			selectGrant = true
		case strings.HasPrefix(upper, "GRANT USAGE ON *.* TO "):
		default:
			return "", "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_BUSINESS_PRIVILEGE_INVALID")
		}
	}
	if rows.Err() != nil || !selectGrant {
		return "", "", NewCapabilityFailure("AUTH_MYSQL_COLUMN_BUSINESS_PRIVILEGE_INVALID")
	}
	return uuid, version, nil
}
