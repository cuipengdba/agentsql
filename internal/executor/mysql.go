package executor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	mysqldriver "github.com/go-sql-driver/mysql"
)

const mysqlConnectionMaxLifetime = 5 * time.Minute

// MySQLExecutor controls one MySQL database/sql connection pool.
type MySQLExecutor struct {
	database   *sql.DB
	maxConns   int
	timeout    time.Duration
	readOnly   bool
	clock      sessionClock
	sessionsMu sync.RWMutex
	sessions   map[string]Session
	closed     bool
}

type mysqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NewMySQLExecutor opens and verifies a MySQL datasource.
func NewMySQLExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*MySQLExecutor, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open MySQL datasource: context is nil")
	}
	if err := validateDatasource(datasource); err != nil {
		return nil, fmt.Errorf("open MySQL datasource: %w", err)
	}
	connectionLimit, err := normalizedConnectionLimit(datasource.ConnLimit)
	if err != nil {
		return nil, fmt.Errorf("open MySQL datasource: %w", err)
	}
	timeoutMS, err := normalizedStatementTimeout(datasource.StmtTimeoutMS)
	if err != nil {
		return nil, fmt.Errorf("open MySQL datasource: %w", err)
	}
	dsn, err := buildMySQLDSN(datasource, password)
	if err != nil {
		return nil, safeError("build MySQL connection configuration", ErrDatasourceUnreachable, err)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, safeError("create MySQL connection pool", ErrDatasourceUnreachable, err)
	}
	database.SetMaxOpenConns(connectionLimit)
	database.SetMaxIdleConns(connectionLimit)
	database.SetConnMaxLifetime(mysqlConnectionMaxLifetime)
	executor := &MySQLExecutor{
		database: database,
		maxConns: connectionLimit,
		timeout:  time.Duration(timeoutMS) * time.Millisecond,
		readOnly: readOnly,
		clock:    wallClock{},
		sessions: make(map[string]Session),
	}
	if err := executor.Ping(ctx); err != nil {
		if closeError := database.Close(); closeError != nil {
			return nil, errors.Join(err, safeDatabaseError("close failed MySQL pool", closeError))
		}
		return nil, err
	}
	return executor, nil
}

func (executor *MySQLExecutor) poolSnapshot() PoolStat {
	if executor == nil {
		return PoolStat{}
	}
	result := PoolStat{MaxOpen: executor.maxConns}
	if executor.database == nil {
		return result
	}
	stat := executor.database.Stats()
	result.InUse = stat.InUse
	result.Idle = stat.Idle
	return result
}

// Dialect returns mysql.
func (*MySQLExecutor) Dialect() string {
	return "mysql"
}

// Ping verifies that the MySQL pool is reachable.
func (executor *MySQLExecutor) Ping(ctx context.Context) error {
	if executor == nil || executor.database == nil || ctx == nil {
		return safeError("ping MySQL datasource", ErrDatasourceUnreachable, nil)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	if err := executor.database.PingContext(timedContext); err != nil {
		return safeError("ping MySQL datasource", ErrDatasourceUnreachable, err)
	}
	return nil
}

// OpenSession binds one acquired MySQL connection to sessionID.
// Duplicate session IDs are rejected with ErrSessionExists.
func (executor *MySQLExecutor) OpenSession(
	ctx context.Context,
	sessionID string,
) (Session, error) {
	if executor == nil || executor.database == nil || ctx == nil {
		return nil, fmt.Errorf("open MySQL session: executor or context is unavailable")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return nil, fmt.Errorf("open MySQL session: invalid session ID")
	}
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if executor.closed {
		return nil, fmt.Errorf("open MySQL session: %w", ErrSessionClosed)
	}
	if _, exists := executor.sessions[sessionID]; exists {
		return nil, fmt.Errorf("open MySQL session %q: %w", sessionID, ErrSessionExists)
	}
	connection, err := executor.database.Conn(ctx)
	if err != nil {
		return nil, mysqlDatabaseError("acquire MySQL session connection", err)
	}
	session := &mysqlSession{
		id:       sessionID,
		executor: executor,
		runner:   connection,
		state:    newTransactionStateMachine(executor.clock),
		release:  connection.Close,
		discard: func() error {
			rawError := connection.Raw(func(any) error {
				return driver.ErrBadConn
			})
			if errors.Is(rawError, driver.ErrBadConn) {
				rawError = nil
			}
			return errors.Join(rawError, connection.Close())
		},
		beginTx: connection.BeginTx,
	}
	executor.sessions[sessionID] = session
	return session, nil
}

// BeginWriteTx starts a MySQL write transaction on a pooled connection.
func (executor *MySQLExecutor) BeginWriteTx(ctx context.Context) (WriteTx, error) {
	if executor == nil || executor.database == nil || ctx == nil {
		return nil, fmt.Errorf("begin MySQL write transaction: executor or context is unavailable")
	}
	if executor.readOnly {
		return nil, fmt.Errorf("begin MySQL write transaction: %w", ErrReadOnlyViolated)
	}
	tx, err := executor.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, mysqlDatabaseError("begin MySQL write transaction", err)
	}
	return &mysqlWriteTx{executor: executor, tx: tx}, nil
}

// Query executes a bounded MySQL row query with a SELECT timeout hint.
func (executor *MySQLExecutor) Query(
	ctx context.Context,
	sqlText string,
	rowLimit int,
) (model.QueryResult, error) {
	if err := executor.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	if rowLimit <= 0 {
		return model.QueryResult{}, fmt.Errorf("query MySQL datasource: row limit must be positive")
	}
	if executor.readOnly && !isMySQLSelect(sqlText) {
		return model.QueryResult{}, fmt.Errorf("query MySQL non-SELECT: %w", ErrReadOnlyViolated)
	}
	return executor.queryWithRunner(ctx, executor.database, sqlText, rowLimit)
}

func (executor *MySQLExecutor) queryWithRunner(
	ctx context.Context,
	runner mysqlRunner,
	sqlText string,
	rowLimit int,
) (model.QueryResult, error) {
	timeoutMS := int(executor.timeout / time.Millisecond)
	hintedSQL, _, err := injectMySQLMaxExecutionTime(sqlText, timeoutMS)
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("prepare MySQL query timeout: %w", err)
	}
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	rows, err := runner.QueryContext(timedContext, hintedSQL)
	if err != nil {
		return model.QueryResult{}, mysqlDatabaseError("query MySQL datasource", err)
	}
	result, err := collectRows(&mysqlRowSource{rows: rows}, rowLimit)
	if err != nil {
		return model.QueryResult{}, mysqlDatabaseError("read MySQL query result", err)
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

// Execute executes MySQL write or DDL SQL and returns affected rows.
func (executor *MySQLExecutor) Execute(
	ctx context.Context,
	sqlText string,
) (model.QueryResult, error) {
	if executor != nil && executor.readOnly {
		return model.QueryResult{}, fmt.Errorf("execute MySQL write: %w", ErrReadOnlyViolated)
	}
	if err := executor.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	return executor.executeWithRunner(ctx, executor.database, sqlText)
}

func (executor *MySQLExecutor) executeWithRunner(
	ctx context.Context,
	runner mysqlRunner,
	sqlText string,
) (model.QueryResult, error) {
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	result, err := runner.ExecContext(timedContext, sqlText)
	if err != nil {
		return model.QueryResult{}, mysqlDatabaseError("execute MySQL statement", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.QueryResult{}, safeDatabaseError("read MySQL affected rows", err)
	}
	if rowsAffected > int64(maxInt()) {
		return model.QueryResult{}, fmt.Errorf("execute MySQL statement: affected row count overflows int")
	}
	return model.QueryResult{
		RowCount:  int(rowsAffected),
		LatencyMS: time.Since(started).Milliseconds(),
	}, nil
}

// Explain returns aggregate MySQL tabular EXPLAIN signals.
func (executor *MySQLExecutor) Explain(
	ctx context.Context,
	sqlText string,
) (model.ExplainInfo, error) {
	if err := executor.validateOperation(ctx, sqlText); err != nil {
		return model.ExplainInfo{}, err
	}
	return executor.explainWithRunner(ctx, executor.database, sqlText)
}

func (executor *MySQLExecutor) explainWithRunner(
	ctx context.Context,
	runner mysqlRunner,
	sqlText string,
) (model.ExplainInfo, error) {
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	rows, err := runner.QueryContext(timedContext, "EXPLAIN "+sqlText)
	if err != nil {
		return model.ExplainInfo{}, mysqlDatabaseError("explain MySQL statement", err)
	}
	columns, values, err := readMySQLExplainRows(rows)
	if err != nil {
		return model.ExplainInfo{}, mysqlDatabaseError("read MySQL explain result", err)
	}
	info, err := parseMysqlExplainRows(columns, values)
	if err != nil {
		return model.ExplainInfo{}, fmt.Errorf("parse MySQL explain result: %w", err)
	}
	return info, nil
}

// Close closes the MySQL connection pool.
func (executor *MySQLExecutor) Close() error {
	if executor == nil || executor.database == nil {
		return nil
	}
	executor.sessionsMu.Lock()
	if executor.closed {
		executor.sessionsMu.Unlock()
		return nil
	}
	executor.closed = true
	sessions := make([]Session, 0, len(executor.sessions))
	for _, session := range executor.sessions {
		sessions = append(sessions, session)
	}
	executor.sessions = make(map[string]Session)
	executor.sessionsMu.Unlock()
	closeErrors := make([]error, 0)
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if err := executor.database.Close(); err != nil {
		closeErrors = append(closeErrors, safeDatabaseError("close MySQL connection pool", err))
	}
	if len(closeErrors) > 0 {
		return fmt.Errorf("close MySQL executor: %w", errors.Join(closeErrors...))
	}
	return nil
}

// TableHasIndex reports whether information_schema contains an index.
func (executor *MySQLExecutor) TableHasIndex(schema, table string) (bool, error) {
	if err := executor.validateMetadataOperation(schema, table); err != nil {
		return false, err
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var count int64
	err := executor.database.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE())
  AND table_name = ?`, schema, table).Scan(&count)
	if err != nil {
		return false, mysqlDatabaseError("read MySQL index metadata", err)
	}
	return count > 0, nil
}

// TableRowCount returns the information_schema planner row estimate.
func (executor *MySQLExecutor) TableRowCount(schema, table string) (int64, error) {
	if err := executor.validateMetadataOperation(schema, table); err != nil {
		return 0, err
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var rows int64
	err := executor.database.QueryRowContext(ctx, `
SELECT COALESCE(table_rows, 0)
FROM information_schema.tables
WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE())
  AND table_name = ?`, schema, table).Scan(&rows)
	if err != nil {
		return 0, mysqlDatabaseError("read MySQL table row metadata", err)
	}
	if rows < 0 {
		return 0, fmt.Errorf("read MySQL table row metadata: negative estimate")
	}
	return rows, nil
}

// MysqlTransactionState returns no authoritative state for unbound pooled calls.
// Call OpenSession and read its state machine for transaction-aware workflows.
func (executor *MySQLExecutor) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, nil
}

func (executor *MySQLExecutor) snapshotServerTransaction() (rules.MysqlTransactionState, bool) {
	if executor == nil || executor.database == nil {
		return rules.MysqlTransactionState{}, false
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var state rules.MysqlTransactionState
	err := executor.database.QueryRowContext(ctx, `
SELECT @@session.autocommit = 0 OR EXISTS (
         SELECT 1
         FROM information_schema.innodb_trx
         WHERE trx_mysql_thread_id = CONNECTION_ID()
       ),
       COALESCE((
         SELECT TIMESTAMPDIFF(MICROSECOND, trx_started, NOW(6)) DIV 1000
         FROM information_schema.innodb_trx
         WHERE trx_mysql_thread_id = CONNECTION_ID()
       ), 0),
       COALESCE((
         SELECT trx_rows_modified
         FROM information_schema.innodb_trx
         WHERE trx_mysql_thread_id = CONNECTION_ID()
       ), 0)`).Scan(
		&state.InTransaction,
		&state.AgeMS,
		&state.AffectedRows,
	)
	if err != nil {
		return rules.MysqlTransactionState{}, false
	}
	if state.AgeMS < 0 || state.AffectedRows < 0 {
		return rules.MysqlTransactionState{}, false
	}
	return state, true
}

func (executor *MySQLExecutor) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := executor.timeout
	if timeout <= 0 {
		timeout = defaultStatementTimeout * time.Millisecond
	}
	return context.WithTimeout(ctx, timeout+executionTimeoutMargin)
}

func (executor *MySQLExecutor) validateOperation(ctx context.Context, sqlText string) error {
	if executor == nil || executor.database == nil {
		return fmt.Errorf("MySQL executor is closed")
	}
	if ctx == nil {
		return fmt.Errorf("MySQL operation context is nil")
	}
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("MySQL SQL is empty")
	}
	return nil
}

func (executor *MySQLExecutor) validateMetadataOperation(schema, table string) error {
	if executor == nil || executor.database == nil {
		return fmt.Errorf("MySQL executor is closed")
	}
	if err := validateMetadataObject(schema, table); err != nil {
		return fmt.Errorf("read MySQL metadata: %w", err)
	}
	return nil
}

func buildMySQLDSN(datasource model.Datasource, password string) (string, error) {
	if err := validateDatasource(datasource); err != nil {
		return "", err
	}
	config := mysqldriver.NewConfig()
	config.User = datasource.Username
	config.Passwd = password
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port))
	config.DBName = datasource.Database
	config.ParseTime = true
	config.MultiStatements = false
	dsn := config.FormatDSN()
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "multiStatements=false", nil
}

func injectMySQLMaxExecutionTime(sqlText string, timeoutMS int) (string, bool, error) {
	if timeoutMS <= 0 {
		return "", false, fmt.Errorf("MAX_EXECUTION_TIME must be positive")
	}
	start, end, ok := firstSQLKeyword(sqlText)
	if !ok || !strings.EqualFold(sqlText[start:end], "SELECT") {
		return sqlText, false, nil
	}
	if hasLeadingMySQLMaxExecutionHint(sqlText[end:]) {
		return sqlText, true, nil
	}
	hint := " /*+ MAX_EXECUTION_TIME(" + strconv.Itoa(timeoutMS) + ") */"
	return sqlText[:end] + hint + sqlText[end:], true, nil
}

func isMySQLSelect(sqlText string) bool {
	start, end, ok := firstSQLKeyword(sqlText)
	return ok && strings.EqualFold(sqlText[start:end], "SELECT")
}

func firstSQLKeyword(sqlText string) (start int, end int, ok bool) {
	start = 0
	for start < len(sqlText) {
		runeValue, size := utf8.DecodeRuneInString(sqlText[start:])
		if !unicode.IsSpace(runeValue) {
			break
		}
		start += size
	}
	end = start
	for end < len(sqlText) {
		character := sqlText[end]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
			break
		}
		end++
	}
	return start, end, end > start
}

func hasLeadingMySQLMaxExecutionHint(remainder string) bool {
	trimmed := strings.TrimLeftFunc(remainder, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "/*+") {
		return false
	}
	end := strings.Index(trimmed, "*/")
	if end < 0 {
		return false
	}
	hint := strings.ToUpper(trimmed[:end])
	return strings.Contains(hint, "MAX_EXECUTION_TIME(")
}

func readMySQLExplainRows(rows *sql.Rows) ([]string, [][]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, closeSQLRowsAfterError(rows, err)
	}
	values := make([][]any, 0)
	for rows.Next() {
		row := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range row {
			destinations[index] = &row[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, nil, closeSQLRowsAfterError(rows, err)
		}
		values = append(values, row)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, nil, errors.Join(iterationError, closeError)
	}
	return columns, values, nil
}

func parseMysqlExplainRows(columns []string, values [][]any) (model.ExplainInfo, error) {
	columnIndexes := make(map[string]int, len(columns))
	for index, column := range columns {
		columnIndexes[strings.ToLower(column)] = index
	}
	typeIndex, hasType := columnIndexes["type"]
	keyIndex, hasKey := columnIndexes["key"]
	rowsIndex, hasRows := columnIndexes["rows"]
	if !hasType || !hasKey || !hasRows || len(values) == 0 {
		return model.ExplainInfo{}, fmt.Errorf("MySQL EXPLAIN requires type, key, rows and at least one row")
	}

	info := model.ExplainInfo{}
	var raw strings.Builder
	raw.WriteString(strings.Join(columns, "\t"))
	for _, row := range values {
		if len(row) != len(columns) {
			return model.ExplainInfo{}, fmt.Errorf("MySQL EXPLAIN row width mismatch")
		}
		estimatedRows, err := mysqlExplainInteger(row[rowsIndex])
		if err != nil || estimatedRows < 0 || info.EstScanRows > math.MaxInt64-estimatedRows {
			return model.ExplainInfo{}, fmt.Errorf("MySQL EXPLAIN has invalid rows estimate")
		}
		info.EstScanRows += estimatedRows
		if strings.EqualFold(stringifyDatabaseValue(row[typeIndex]), "ALL") {
			info.SeqScan = true
		}
		if strings.TrimSpace(stringifyDatabaseValue(row[keyIndex])) != "" {
			info.UsesIndex = true
		}
		raw.WriteByte('\n')
		for index, value := range row {
			if index > 0 {
				raw.WriteByte('\t')
			}
			raw.WriteString(stringifyDatabaseValue(value))
		}
	}
	info.Raw = raw.String()
	return info, nil
}

func mysqlExplainInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("rows estimate overflows int64")
		}
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, fmt.Errorf("rows estimate overflows int64")
		}
		return int64(typed), nil
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported rows estimate type %T", value)
	}
}

type mysqlRowSource struct {
	rows      *sql.Rows
	columns   []string
	columnErr error
}

func (source *mysqlRowSource) Columns() ([]string, error) {
	if source.columns == nil && source.columnErr == nil {
		source.columns, source.columnErr = source.rows.Columns()
	}
	return source.columns, source.columnErr
}

func (source *mysqlRowSource) Next() bool {
	return source.rows.Next()
}

func (source *mysqlRowSource) Values() ([]any, error) {
	columns, err := source.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := source.rows.Scan(destinations...); err != nil {
		return nil, err
	}
	return values, nil
}

func (source *mysqlRowSource) Err() error {
	return source.rows.Err()
}

func (source *mysqlRowSource) Close() error {
	return source.rows.Close()
}

func closeSQLRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func mysqlDatabaseError(message string, cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		return safeError(message, ErrQueryTimeout, cause)
	}
	var mysqlError *mysqldriver.MySQLError
	if errors.As(cause, &mysqlError) && mysqlError.Number == 3024 {
		return safeError(message, ErrQueryTimeout, cause)
	}
	return safeDatabaseError(message, cause)
}

type mysqlSession struct {
	id          string
	executor    *MySQLExecutor
	runner      mysqlRunner
	state       *transactionStateMachine
	release     func() error
	discard     func() error
	beginTx     func(context.Context, *sql.TxOptions) (*sql.Tx, error)
	operationMu sync.Mutex
	closed      bool
}

func (session *mysqlSession) BeginWriteTx(ctx context.Context) (WriteTx, error) {
	if session == nil {
		return nil, fmt.Errorf("begin MySQL session write transaction: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	if session.closed || session.executor == nil || ctx == nil {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin MySQL session write transaction: session or context is unavailable")
	}
	if session.executor.readOnly {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin MySQL session write transaction: %w", ErrReadOnlyViolated)
	}
	if session.state != nil && session.state.active() {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin MySQL session write transaction: %w", ErrSessionTransactionActive)
	}
	if session.beginTx == nil {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin MySQL session write transaction: transaction starter is unavailable")
	}
	tx, err := session.beginTx(ctx, nil)
	if err != nil {
		session.operationMu.Unlock()
		return nil, mysqlDatabaseError("begin MySQL session write transaction", err)
	}
	return &mysqlWriteTx{
		executor: session.executor,
		tx:       tx,
		terminal: session.operationMu.Unlock,
	}, nil
}

type mysqlWriteTx struct {
	mu       sync.Mutex
	executor *MySQLExecutor
	tx       *sql.Tx
	done     bool
	terminal func()
}

func (tx *mysqlWriteTx) Execute(ctx context.Context, sqlText string) (model.QueryResult, error) {
	if tx == nil {
		return model.QueryResult{}, fmt.Errorf("execute MySQL write transaction: %w", ErrTransactionDone)
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done || tx.tx == nil || tx.executor == nil {
		return model.QueryResult{}, fmt.Errorf("execute MySQL write transaction: %w", ErrTransactionDone)
	}
	if err := tx.executor.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	return tx.executor.executeWithRunner(ctx, tx.tx, sqlText)
}

func (tx *mysqlWriteTx) Commit(ctx context.Context) error {
	return tx.finish(ctx, true)
}

func (tx *mysqlWriteTx) Rollback(ctx context.Context) error {
	return tx.finish(ctx, false)
}

func (tx *mysqlWriteTx) finish(ctx context.Context, commit bool) error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("finish MySQL write transaction: context is nil")
	}
	var err error
	if commit {
		err = tx.tx.Commit()
	} else {
		err = tx.tx.Rollback()
	}
	tx.done = true
	tx.tx = nil
	if tx.terminal != nil {
		tx.terminal()
		tx.terminal = nil
	}
	if err != nil {
		action := "rollback"
		if commit {
			action = "commit"
		}
		return mysqlDatabaseError(action+" MySQL write transaction", err)
	}
	return nil
}

func (session *mysqlSession) Query(
	ctx context.Context,
	sqlText string,
	rowLimit int,
) (model.QueryResult, error) {
	if session == nil {
		return model.QueryResult{}, fmt.Errorf("query MySQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	if rowLimit <= 0 {
		return model.QueryResult{}, fmt.Errorf("query MySQL session: row limit must be positive")
	}
	if session.executor.readOnly && !isMySQLSelect(sqlText) {
		return model.QueryResult{}, fmt.Errorf("query MySQL session non-SELECT: %w", ErrReadOnlyViolated)
	}
	classification, err := classifySessionStatement(sqlText, "mysql")
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("classify MySQL session statement: %w", err)
	}
	started := session.state.clock.Now()
	result, executionError := session.executor.queryWithRunner(
		ctx,
		session.runner,
		sqlText,
		rowLimit,
	)
	session.state.finishStatement(
		classification,
		started,
		int64(result.RowCount),
		executionError == nil,
	)
	return result, executionError
}

func (session *mysqlSession) Execute(
	ctx context.Context,
	sqlText string,
) (model.QueryResult, error) {
	if session == nil {
		return model.QueryResult{}, fmt.Errorf("execute MySQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	if session.executor.readOnly {
		return model.QueryResult{}, fmt.Errorf("execute MySQL session write: %w", ErrReadOnlyViolated)
	}
	classification, err := classifySessionStatement(sqlText, "mysql")
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("classify MySQL session statement: %w", err)
	}
	started := session.state.clock.Now()
	result, executionError := session.executor.executeWithRunner(ctx, session.runner, sqlText)
	session.state.finishStatement(
		classification,
		started,
		int64(result.RowCount),
		executionError == nil,
	)
	return result, executionError
}

func (session *mysqlSession) Explain(
	ctx context.Context,
	sqlText string,
) (model.ExplainInfo, error) {
	if session == nil {
		return model.ExplainInfo{}, fmt.Errorf("explain MySQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sqlText); err != nil {
		return model.ExplainInfo{}, err
	}
	result, err := session.executor.explainWithRunner(ctx, session.runner, sqlText)
	session.state.touchStatementEnd()
	return result, err
}

func (*mysqlSession) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, fmt.Errorf(
		"PostgreSQL transaction state is unavailable for MySQL session",
	)
}

func (session *mysqlSession) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	if session == nil {
		return rules.MysqlTransactionState{}, fmt.Errorf("read MySQL session state: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if session.closed {
		return rules.MysqlTransactionState{}, fmt.Errorf("read MySQL session state: %w", ErrSessionClosed)
	}
	inTransaction, ageMS, _, affectedRows, err := session.state.snapshot()
	if err != nil {
		return rules.MysqlTransactionState{}, fmt.Errorf("read MySQL session state: %w", err)
	}
	return rules.MysqlTransactionState{
		InTransaction: inTransaction,
		AgeMS:         ageMS,
		AffectedRows:  affectedRows,
	}, nil
}

func (session *mysqlSession) Close() error {
	if session == nil {
		return nil
	}
	session.operationMu.Lock()
	if session.closed {
		session.operationMu.Unlock()
		return nil
	}
	session.closed = true
	defer session.operationMu.Unlock()
	if session.executor != nil {
		defer session.executor.removeSession(session.id, session)
	}

	var rollbackError error
	if session.state.active() {
		_, rollbackError = session.executor.executeWithRunner(
			context.Background(),
			session.runner,
			"ROLLBACK",
		)
		if rollbackError == nil {
			session.state.clear()
		}
	}
	if rollbackError != nil {
		var discardError error
		if session.discard != nil {
			discardError = session.discard()
		}
		return fmt.Errorf(
			"close MySQL session %q after rollback failure: %w",
			session.id,
			errors.Join(rollbackError, discardError),
		)
	}
	if session.release != nil {
		if err := session.release(); err != nil {
			return safeDatabaseError("release MySQL session connection", err)
		}
	}
	return nil
}

func (session *mysqlSession) validateOperation(ctx context.Context, sqlText string) error {
	if session.closed || session.executor == nil || isNilValue(session.runner) {
		return fmt.Errorf("MySQL session operation: %w", ErrSessionClosed)
	}
	if ctx == nil {
		return fmt.Errorf("MySQL session operation context is nil")
	}
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("MySQL session SQL is empty")
	}
	return nil
}

func (executor *MySQLExecutor) removeSession(id string, session Session) {
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if current, exists := executor.sessions[id]; exists && current == session {
		delete(executor.sessions, id)
	}
}

var (
	_ Executor                               = (*MySQLExecutor)(nil)
	_ rules.MysqlTransactionMetadataProvider = (*MySQLExecutor)(nil)
	_ Session                                = (*mysqlSession)(nil)
	_ rules.MysqlTransactionMetadataProvider = (*mysqlSession)(nil)
)
