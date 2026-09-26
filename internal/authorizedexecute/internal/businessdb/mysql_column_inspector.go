package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	// MySQLColumnAuthorizationUnsupportedCode is the stable fail-closed result
	// for every MySQL column-authorization request in v0.4. Catalog visibility
	// alone is not an authority-bearing binder proof.
	MySQLColumnAuthorizationUnsupportedCode    = BinderCodeModeRequired
	MySQLColumnAuthorizationUnsupportedMessage = "当前版本 MySQL 列级授权未闭合，建议使用 PostgreSQL 或保持表级策略"

	mysqlInspectorPrivilegeExcessCode = "AUTH_INSPECTOR_PRIVILEGE_EXCESS"
	mysqlInspectorProbeTimeout        = 5 * time.Second
)

// MySQLColumnAuthorizationVerdict is deliberately separate from an execution
// result. The v0.4 inspector can collect evidence, but can never mint a proof
// or silently downgrade a column authorization request to table authorization.
type MySQLColumnAuthorizationVerdict struct {
	Supported bool
	Code      string
	Message   string
}

// MySQLInspectorTarget contains identifiers only. It is not an arbitrary SQL
// ingress: Probe builds a fixed catalog query and a fixed EXPLAIN statement.
type MySQLInspectorTarget struct {
	Schema    string
	BaseTable string
	View      string
}

// MySQLInspectorReport records facts that MySQL 8.x exposes to an isolated
// metadata credential and, separately, the proof primitives it does not
// expose. The MissingProofPrimitives values are part of the versioned v0.4
// boundary rather than claims inferred from an EXPLAIN plan.
type MySQLInspectorReport struct {
	ServerVersion       string
	ServerUUID          string
	CurrentUser         string
	Database            string
	LowerCaseTableNames int
	GTIDMode            string
	GTIDExecuted        string
	LogBin              bool
	BinlogFormat        string
	Grants              []string

	InformationSchemaReadable bool
	ExplainJSONAvailable      bool
	TableExists               bool
	ColumnCount               int
	ViewCount                 int
	ViewDefinitionBytes       int
	ShowCreateViewAvailable   bool
	TriggerCount              int
	TriggerDefinitionBytes    int
	EventCount                int
	EventDefinitionBytes      int
	ForeignKeyCount           int

	BackupLockAttempted bool
	GTIDWatcherActive   bool
	BinlogWatcherActive bool

	MissingProofPrimitives []string
}

// ColumnAuthorizationVerdict is intentionally independent of SQL shape. This
// makes views, multiple statements, triggers, and seemingly-simple base-table
// SELECTs converge on the same tested fail-closed boundary.
func (MySQLInspectorReport) ColumnAuthorizationVerdict() MySQLColumnAuthorizationVerdict {
	return MySQLColumnAuthorizationVerdict{
		Supported: false,
		Code:      MySQLColumnAuthorizationUnsupportedCode,
		Message:   MySQLColumnAuthorizationUnsupportedMessage,
	}
}

// MySQLColumnInspector owns a pool that is distinct from MySQLExecutor. It
// deliberately does not implement Executor, Session, mysqlRunner, or any
// interface that accepts caller SQL. Its sole database operation is Probe.
type MySQLColumnInspector struct {
	database *sql.DB
	timeout  time.Duration
}

// NewMySQLColumnInspector creates the isolated inspection pool. Callers must
// supply the dedicated metadata credential, not the proxy/execution account.
// The pool has one physical connection and multi-statements remain disabled.
func NewMySQLColumnInspector(
	ctx context.Context,
	datasource model.Datasource,
	password string,
) (*MySQLColumnInspector, error) {
	if ctx == nil {
		return nil, errors.New("open MySQL column inspector: context is nil")
	}
	if datasource.DBType != "mysql" {
		return nil, errors.New("open MySQL column inspector: datasource is not mysql")
	}
	dsn, err := buildMySQLDSN(datasource, password)
	if err != nil {
		return nil, safeError("build MySQL inspector connection configuration", ErrDatasourceUnreachable, err)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, mysqlConnectionError(ctx, DBStageConnect, "create MySQL inspector pool", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(mysqlConnectionMaxLifetime)
	inspector := &MySQLColumnInspector{database: database, timeout: mysqlInspectorProbeTimeout}
	probeContext, cancel := inspector.timeoutContext(ctx)
	defer cancel()
	if err := database.PingContext(probeContext); err != nil {
		return nil, errors.Join(
			mysqlConnectionError(probeContext, DBStagePing, "ping MySQL inspector", err),
			database.Close(),
		)
	}
	return inspector, nil
}

// Probe reads a bounded, fixed catalog fact set. It never takes a backup lock,
// starts a transaction, waits on a customer-table metadata lock, reads the
// binlog, or executes DDL. Those mechanisms require separate operational
// acceptance and cannot make this v0.4 verdict supported.
func (inspector *MySQLColumnInspector) Probe(
	ctx context.Context,
	target MySQLInspectorTarget,
) (MySQLInspectorReport, error) {
	if inspector == nil || inspector.database == nil || ctx == nil {
		return MySQLInspectorReport{}, NewCapabilityFailure("AUTH_INSPECTOR_UNAVAILABLE")
	}
	if err := validateMySQLInspectorTarget(target); err != nil {
		return MySQLInspectorReport{}, NewCapabilityFailure("AUTH_INSPECTOR_TARGET_INVALID")
	}
	probeContext, cancel := inspector.timeoutContext(ctx)
	defer cancel()
	connection, err := inspector.database.Conn(probeContext)
	if err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageAcquire, "acquire MySQL inspector connection", err)
	}
	defer connection.Close()

	report := MySQLInspectorReport{
		MissingProofPrimitives: []string{
			"typed_analyzed_tree",
			"per_output_view_lineage",
			"serializable_catalog_lock",
			"stable_semantic_digest",
			"prepared_invalidation_algebra",
		},
	}
	var logBin int
	err = connection.QueryRowContext(probeContext, `SELECT VERSION(), @@GLOBAL.server_uuid,
@@lower_case_table_names, @@GLOBAL.gtid_mode, @@GLOBAL.gtid_executed,
@@GLOBAL.log_bin, @@GLOBAL.binlog_format, CURRENT_USER(), DATABASE()`).Scan(
		&report.ServerVersion,
		&report.ServerUUID,
		&report.LowerCaseTableNames,
		&report.GTIDMode,
		&report.GTIDExecuted,
		&logBin,
		&report.BinlogFormat,
		&report.CurrentUser,
		&report.Database,
	)
	if err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL inspector server facts", err)
	}
	report.LogBin = logBin != 0

	report.Grants, err = readMySQLInspectorGrants(probeContext, connection)
	if err != nil {
		return MySQLInspectorReport{}, err
	}
	if mysqlInspectorHasExcessPrivilege(report.Grants) {
		return MySQLInspectorReport{}, NewCapabilityFailure(mysqlInspectorPrivilegeExcessCode)
	}

	var tableCount int
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*)
FROM information_schema.tables
WHERE BINARY table_schema = BINARY ? AND BINARY table_name = BINARY ? AND table_type = 'BASE TABLE'`,
		target.Schema, target.BaseTable).Scan(&tableCount); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL table metadata", err)
	}
	report.TableExists = tableCount == 1
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*)
FROM information_schema.columns
WHERE BINARY table_schema = BINARY ? AND BINARY table_name = BINARY ?`,
		target.Schema, target.BaseTable).Scan(&report.ColumnCount); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL column metadata", err)
	}
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*), COALESCE(SUM(CHAR_LENGTH(view_definition)),0)
FROM information_schema.views WHERE BINARY table_schema = BINARY ?`, target.Schema).Scan(
		&report.ViewCount, &report.ViewDefinitionBytes,
	); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL view metadata", err)
	}
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*), COALESCE(SUM(CHAR_LENGTH(action_statement)),0)
FROM information_schema.triggers WHERE BINARY trigger_schema = BINARY ?`, target.Schema).Scan(
		&report.TriggerCount, &report.TriggerDefinitionBytes,
	); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL trigger metadata", err)
	}
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*), COALESCE(SUM(CHAR_LENGTH(event_definition)),0)
FROM information_schema.events WHERE BINARY event_schema = BINARY ?`, target.Schema).Scan(
		&report.EventCount, &report.EventDefinitionBytes,
	); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL event metadata", err)
	}
	if err := connection.QueryRowContext(probeContext, `SELECT COUNT(*)
FROM information_schema.referential_constraints
WHERE BINARY constraint_schema = BINARY ? OR BINARY unique_constraint_schema = BINARY ?`,
		target.Schema, target.Schema).Scan(&report.ForeignKeyCount); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageQuery, "read MySQL foreign-key metadata", err)
	}
	report.InformationSchemaReadable = true

	quotedSchema := quoteMySQLInspectorIdentifier(target.Schema)
	quotedTable := quoteMySQLInspectorIdentifier(target.BaseTable)
	var explainJSON string
	if err := connection.QueryRowContext(probeContext,
		"EXPLAIN FORMAT=JSON SELECT 1 FROM "+quotedSchema+"."+quotedTable+" WHERE 1=0",
	).Scan(&explainJSON); err != nil {
		return MySQLInspectorReport{}, mysqlDatabaseError(probeContext, DBStageExplain, "run fixed MySQL JSON explain", err)
	}
	report.ExplainJSONAvailable = strings.HasPrefix(strings.TrimSpace(explainJSON), "{")
	if target.View != "" {
		report.ShowCreateViewAvailable, err = probeMySQLShowCreateView(
			probeContext,
			connection,
			quotedSchema+"."+quoteMySQLInspectorIdentifier(target.View),
		)
		if err != nil {
			return MySQLInspectorReport{}, err
		}
	}
	return report, nil
}

func (inspector *MySQLColumnInspector) Close() error {
	if inspector == nil || inspector.database == nil {
		return nil
	}
	database := inspector.database
	inspector.database = nil
	return database.Close()
}

func (inspector *MySQLColumnInspector) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := inspector.timeout
	if timeout <= 0 || timeout > mysqlInspectorProbeTimeout {
		timeout = mysqlInspectorProbeTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func validateMySQLInspectorTarget(target MySQLInspectorTarget) error {
	if err := validateMySQLInspectorIdentifier(target.Schema); err != nil {
		return err
	}
	if err := validateMySQLInspectorIdentifier(target.BaseTable); err != nil {
		return err
	}
	if target.View != "" {
		return validateMySQLInspectorIdentifier(target.View)
	}
	return nil
}

func validateMySQLInspectorIdentifier(value string) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > 64 {
		return errors.New("invalid MySQL inspector identifier")
	}
	for _, character := range value {
		if character == 0 || character == ';' || unicode.IsControl(character) {
			return errors.New("invalid MySQL inspector identifier")
		}
	}
	return nil
}

func quoteMySQLInspectorIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func readMySQLInspectorGrants(ctx context.Context, connection *sql.Conn) ([]string, error) {
	rows, err := connection.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageQuery, "read MySQL inspector grants", err)
	}
	defer rows.Close()
	grants := make([]string, 0, 4)
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return nil, mysqlDatabaseError(ctx, DBStageReadRows, "scan MySQL inspector grant", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, mysqlDatabaseError(ctx, DBStageReadRows, "read MySQL inspector grants", err)
	}
	if len(grants) == 0 {
		return nil, NewCapabilityFailure("AUTH_INSPECTOR_GRANTS_INCOMPLETE")
	}
	return grants, nil
}

func mysqlInspectorHasExcessPrivilege(grants []string) bool {
	for _, grant := range grants {
		normalized := " " + strings.ToUpper(strings.Join(strings.Fields(grant), " ")) + " "
		for _, forbidden := range []string{
			" ALL PRIVILEGES ", " SUPER ", " FILE ", " PROCESS ", " RELOAD ",
			" SHUTDOWN ", " CREATE USER ", " CREATE ROLE ", " DROP ROLE ",
			" REPLICATION SLAVE ", " REPLICATION CLIENT ", " BACKUP_ADMIN ",
			" CONNECTION_ADMIN ", " SYSTEM_USER ", " SYSTEM_VARIABLES_ADMIN ",
			" CREATE ", " ALTER ", " DROP ", " INDEX ", " INSERT ", " UPDATE ",
			" DELETE ", " EXECUTE ", " CREATE ROUTINE ", " ALTER ROUTINE ",
			" CREATE TEMPORARY TABLES ", " LOCK TABLES ", " GRANT OPTION ",
		} {
			if strings.Contains(normalized, forbidden) {
				return true
			}
		}
	}
	return false
}

func probeMySQLShowCreateView(ctx context.Context, connection *sql.Conn, quotedView string) (bool, error) {
	rows, err := connection.QueryContext(ctx, "SHOW CREATE VIEW "+quotedView)
	if err != nil {
		return false, mysqlDatabaseError(ctx, DBStageQuery, "read fixed MySQL view definition", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, mysqlDatabaseError(ctx, DBStageReadRows, "read MySQL SHOW CREATE VIEW columns", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, mysqlDatabaseError(ctx, DBStageReadRows, "read MySQL SHOW CREATE VIEW row", err)
		}
		return false, nil
	}
	values := make([]sql.NullString, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return false, mysqlDatabaseError(ctx, DBStageReadRows, "scan MySQL SHOW CREATE VIEW row", err)
	}
	for index, column := range columns {
		if strings.EqualFold(column, "Create View") {
			return values[index].Valid && strings.TrimSpace(values[index].String) != "", nil
		}
	}
	return false, nil
}

func (report MySQLInspectorReport) String() string {
	return fmt.Sprintf("mysql=%s information_schema=%t explain_json=%t table=%t columns=%d views=%d triggers=%d events=%d fks=%d",
		report.ServerVersion, report.InformationSchemaReadable, report.ExplainJSONAvailable,
		report.TableExists, report.ColumnCount, report.ViewCount, report.TriggerCount,
		report.EventCount, report.ForeignKeyCount)
}

var _ interface{ Close() error } = (*MySQLColumnInspector)(nil)
