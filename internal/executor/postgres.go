package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const executionTimeoutMargin = 250 * time.Millisecond

// PostgresExecutor controls one PostgreSQL connection pool.
type PostgresExecutor struct {
	pool       *pgxpool.Pool
	timeout    time.Duration
	readOnly   bool
	clock      sessionClock
	sessionsMu sync.RWMutex
	sessions   map[string]Session
	closed     bool
}

type postgresRunner interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewPostgresExecutor opens and verifies a PostgreSQL datasource.
func NewPostgresExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*PostgresExecutor, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open PostgreSQL datasource: context is nil")
	}
	if err := validateDatasource(datasource); err != nil {
		return nil, fmt.Errorf("open PostgreSQL datasource: %w", err)
	}
	connectionLimit, err := normalizedConnectionLimit(datasource.ConnLimit)
	if err != nil || connectionLimit > math.MaxInt32 {
		return nil, fmt.Errorf("open PostgreSQL datasource: invalid connection limit")
	}
	timeoutMS, err := normalizedStatementTimeout(datasource.StmtTimeoutMS)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL datasource: %w", err)
	}

	connectionURL := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(datasource.Username, password),
		Host:   net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)),
		Path:   "/" + datasource.Database,
	}
	config, err := pgxpool.ParseConfig(connectionURL.String())
	if err != nil {
		return nil, safeError(
			"parse PostgreSQL connection configuration",
			ErrDatasourceUnreachable,
			err,
		)
	}
	config.MaxConns = int32(connectionLimit)
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(timeoutMS)
	if readOnly {
		config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, safeError("create PostgreSQL connection pool", ErrDatasourceUnreachable, err)
	}
	executor := &PostgresExecutor{
		pool:     pool,
		timeout:  time.Duration(timeoutMS) * time.Millisecond,
		readOnly: readOnly,
		clock:    wallClock{},
		sessions: make(map[string]Session),
	}
	if err := executor.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return executor, nil
}

// Dialect returns postgres.
func (*PostgresExecutor) Dialect() string {
	return "postgres"
}

// Ping verifies that the PostgreSQL pool is reachable.
func (executor *PostgresExecutor) Ping(ctx context.Context) error {
	if executor == nil || executor.pool == nil || ctx == nil {
		return safeError("ping PostgreSQL datasource", ErrDatasourceUnreachable, nil)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	if err := executor.pool.Ping(timedContext); err != nil {
		return safeError("ping PostgreSQL datasource", ErrDatasourceUnreachable, err)
	}
	return nil
}

// OpenSession binds one acquired PostgreSQL connection to sessionID.
// Duplicate session IDs are rejected with ErrSessionExists.
func (executor *PostgresExecutor) OpenSession(
	ctx context.Context,
	sessionID string,
) (Session, error) {
	if executor == nil || executor.pool == nil || ctx == nil {
		return nil, fmt.Errorf("open PostgreSQL session: executor or context is unavailable")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return nil, fmt.Errorf("open PostgreSQL session: invalid session ID")
	}
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if executor.closed {
		return nil, fmt.Errorf("open PostgreSQL session: %w", ErrSessionClosed)
	}
	if _, exists := executor.sessions[sessionID]; exists {
		return nil, fmt.Errorf("open PostgreSQL session %q: %w", sessionID, ErrSessionExists)
	}
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return nil, postgresDatabaseError("acquire PostgreSQL session connection", err)
	}
	session := &postgresSession{
		id:       sessionID,
		executor: executor,
		runner:   connection,
		state:    newTransactionStateMachine(executor.clock),
		release:  connection.Release,
		discard: func() error {
			physical := connection.Hijack()
			closeContext, cancel := executor.timeoutContext(context.Background())
			defer cancel()
			return physical.Close(closeContext)
		},
	}
	executor.sessions[sessionID] = session
	return session, nil
}

// Query executes a bounded PostgreSQL row query without rewriting SQL.
func (executor *PostgresExecutor) Query(
	ctx context.Context,
	sql string,
	rowLimit int,
) (model.QueryResult, error) {
	if err := executor.validateOperation(ctx, sql); err != nil {
		return model.QueryResult{}, err
	}
	if rowLimit <= 0 {
		return model.QueryResult{}, fmt.Errorf("query PostgreSQL datasource: row limit must be positive")
	}
	return executor.queryWithRunner(ctx, executor.pool, sql, rowLimit)
}

func (executor *PostgresExecutor) queryWithRunner(
	ctx context.Context,
	runner postgresRunner,
	sql string,
	rowLimit int,
) (model.QueryResult, error) {
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	rows, err := runner.Query(timedContext, sql)
	if err != nil {
		return model.QueryResult{}, postgresDatabaseError("query PostgreSQL datasource", err)
	}
	result, err := collectRows(&postgresRowSource{rows: rows}, rowLimit)
	if err != nil {
		return model.QueryResult{}, postgresDatabaseError("read PostgreSQL query result", err)
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

// Execute executes PostgreSQL write or DDL SQL and returns affected rows.
func (executor *PostgresExecutor) Execute(
	ctx context.Context,
	sql string,
) (model.QueryResult, error) {
	if executor != nil && executor.readOnly {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL write: %w", ErrReadOnlyViolated)
	}
	if err := executor.validateOperation(ctx, sql); err != nil {
		return model.QueryResult{}, err
	}
	return executor.executeWithRunner(ctx, executor.pool, sql)
}

func (executor *PostgresExecutor) executeWithRunner(
	ctx context.Context,
	runner postgresRunner,
	sql string,
) (model.QueryResult, error) {
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	commandTag, err := runner.Exec(timedContext, sql)
	if err != nil {
		return model.QueryResult{}, postgresDatabaseError("execute PostgreSQL statement", err)
	}
	rowsAffected := commandTag.RowsAffected()
	if rowsAffected > int64(maxInt()) {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL statement: affected row count overflows int")
	}
	return model.QueryResult{
		RowCount:  int(rowsAffected),
		LatencyMS: time.Since(started).Milliseconds(),
	}, nil
}

// Explain returns the root PostgreSQL JSON plan signals.
func (executor *PostgresExecutor) Explain(
	ctx context.Context,
	sql string,
) (model.ExplainInfo, error) {
	if err := executor.validateOperation(ctx, sql); err != nil {
		return model.ExplainInfo{}, err
	}
	return executor.explainWithRunner(ctx, executor.pool, sql)
}

func (executor *PostgresExecutor) explainWithRunner(
	ctx context.Context,
	runner postgresRunner,
	sql string,
) (model.ExplainInfo, error) {
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	var raw []byte
	if err := runner.QueryRow(
		timedContext,
		"EXPLAIN (FORMAT JSON) "+sql,
	).Scan(&raw); err != nil {
		return model.ExplainInfo{}, postgresDatabaseError("explain PostgreSQL statement", err)
	}
	info, err := parsePostgresExplainJSON(raw)
	if err != nil {
		return model.ExplainInfo{}, fmt.Errorf("parse PostgreSQL explain result: %w", err)
	}
	return info, nil
}

// Close closes the PostgreSQL connection pool.
func (executor *PostgresExecutor) Close() error {
	if executor == nil || executor.pool == nil {
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
	executor.pool.Close()
	if len(closeErrors) > 0 {
		return fmt.Errorf("close PostgreSQL sessions: %w", errors.Join(closeErrors...))
	}
	return nil
}

// TableHasIndex implements rules.MetadataProvider using PostgreSQL catalogs.
func (executor *PostgresExecutor) TableHasIndex(schema, table string) (bool, error) {
	if executor == nil || executor.pool == nil {
		return false, fmt.Errorf("check PostgreSQL table index: executor is not initialized")
	}
	if err := validateMetadataObject(schema, table); err != nil {
		return false, fmt.Errorf("check PostgreSQL table index: %w", err)
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var exists bool
	err := executor.pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_index AS i
  JOIN pg_class AS c ON c.oid = i.indrelid
  JOIN pg_namespace AS n ON n.oid = c.relnamespace
  WHERE n.nspname = COALESCE(NULLIF($1, ''), current_schema())
    AND c.relname = $2
    AND i.indisvalid
)`, schema, table).Scan(&exists)
	if err != nil {
		return false, postgresDatabaseError("read PostgreSQL index metadata", err)
	}
	return exists, nil
}

// TableRowCount implements rules.MetadataProvider using planner reltuples.
func (executor *PostgresExecutor) TableRowCount(schema, table string) (int64, error) {
	if executor == nil || executor.pool == nil {
		return 0, fmt.Errorf("read PostgreSQL table rows: executor is not initialized")
	}
	if err := validateMetadataObject(schema, table); err != nil {
		return 0, fmt.Errorf("read PostgreSQL table rows: %w", err)
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var rows int64
	err := executor.pool.QueryRow(ctx, `
SELECT c.reltuples::bigint
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = COALESCE(NULLIF($1, ''), current_schema())
  AND c.relname = $2`, schema, table).Scan(&rows)
	if err != nil {
		return 0, postgresDatabaseError("read PostgreSQL table row metadata", err)
	}
	if rows < 0 {
		return 0, fmt.Errorf("read PostgreSQL table row metadata: negative estimate")
	}
	return rows, nil
}

// TransactionState returns no authoritative state for unbound pooled calls.
// Call OpenSession and read its state machine for transaction-aware workflows.
func (executor *PostgresExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}

func (executor *PostgresExecutor) snapshotServerTransaction() (rules.TransactionState, bool) {
	if executor == nil || executor.pool == nil {
		return rules.TransactionState{}, false
	}
	ctx, cancel := executor.timeoutContext(context.Background())
	defer cancel()
	var state rules.TransactionState
	err := executor.pool.QueryRow(ctx, `
SELECT xact_start IS NOT NULL AND xact_start < query_start,
       CASE WHEN xact_start IS NOT NULL AND xact_start < query_start
            THEN COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - xact_start)) * 1000, 0)::bigint
            ELSE 0 END,
       CASE WHEN xact_start IS NOT NULL AND xact_start < query_start
                 AND state = 'idle in transaction'
            THEN COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - state_change)) * 1000, 0)::bigint
            ELSE 0 END
FROM pg_stat_activity
WHERE pid = pg_backend_pid()`).Scan(&state.InTransaction, &state.AgeMS, &state.IdleMS)
	if err != nil {
		return rules.TransactionState{}, false
	}
	return state, true
}

func (executor *PostgresExecutor) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := executor.timeout
	if timeout <= 0 {
		timeout = defaultStatementTimeout * time.Millisecond
	}
	return context.WithTimeout(ctx, timeout+executionTimeoutMargin)
}

func (executor *PostgresExecutor) validateOperation(ctx context.Context, sql string) error {
	if executor == nil || executor.pool == nil {
		return fmt.Errorf("PostgreSQL executor is closed")
	}
	if ctx == nil {
		return fmt.Errorf("PostgreSQL operation context is nil")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("PostgreSQL SQL is empty")
	}
	return nil
}

type postgresExplainDocument struct {
	Plan postgresExplainPlan `json:"Plan"`
}

type postgresExplainPlan struct {
	NodeType  string  `json:"Node Type"`
	PlanRows  float64 `json:"Plan Rows"`
	TotalCost float64 `json:"Total Cost"`
	IndexName string  `json:"Index Name"`
}

func parsePostgresExplainJSON(raw []byte) (model.ExplainInfo, error) {
	var documents []postgresExplainDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		return model.ExplainInfo{}, fmt.Errorf("decode JSON plan: %w", err)
	}
	if len(documents) != 1 || strings.TrimSpace(documents[0].Plan.NodeType) == "" {
		return model.ExplainInfo{}, fmt.Errorf("JSON plan must contain one root Plan")
	}
	plan := documents[0].Plan
	if plan.PlanRows < 0 || plan.PlanRows > math.MaxInt64 || math.IsNaN(plan.PlanRows) ||
		math.IsInf(plan.PlanRows, 0) || plan.TotalCost < 0 || math.IsNaN(plan.TotalCost) ||
		math.IsInf(plan.TotalCost, 0) {
		return model.ExplainInfo{}, fmt.Errorf("JSON plan contains invalid estimates")
	}
	return model.ExplainInfo{
		EstScanRows: int64(plan.PlanRows),
		EstCost:     plan.TotalCost,
		SeqScan:     plan.NodeType == "Seq Scan",
		UsesIndex: plan.IndexName != "" || strings.Contains(plan.NodeType, "Index") ||
			strings.Contains(plan.NodeType, "Bitmap"),
		Raw: string(raw),
	}, nil
}

type postgresRowSource struct {
	rows pgx.Rows
}

func (source *postgresRowSource) Columns() ([]string, error) {
	fields := source.rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for index, field := range fields {
		columns[index] = field.Name
	}
	return columns, nil
}

func (source *postgresRowSource) Next() bool {
	return source.rows.Next()
}

func (source *postgresRowSource) Values() ([]any, error) {
	return source.rows.Values()
}

func (source *postgresRowSource) Err() error {
	return source.rows.Err()
}

func (source *postgresRowSource) Close() error {
	source.rows.Close()
	return nil
}

func postgresDatabaseError(message string, cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		return safeError(message, ErrQueryTimeout, cause)
	}
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.Code {
		case "57014":
			return safeError(message, ErrQueryTimeout, cause)
		case "25006":
			return safeError(message, ErrReadOnlyViolated, cause)
		}
	}
	return safeDatabaseError(message, cause)
}

func safeDatabaseError(message string, cause error) error {
	return redactedError{
		message: message + ": database operation failed",
		cause:   sanitizedCause(cause),
	}
}

func validateMetadataObject(schema, table string) error {
	if strings.TrimSpace(table) == "" || strings.TrimSpace(table) != table ||
		strings.TrimSpace(schema) != schema {
		return fmt.Errorf("invalid schema or table name")
	}
	return nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

type postgresSession struct {
	id          string
	executor    *PostgresExecutor
	runner      postgresRunner
	state       *transactionStateMachine
	release     func()
	discard     func() error
	operationMu sync.Mutex
	closed      bool
}

func (session *postgresSession) Query(
	ctx context.Context,
	sql string,
	rowLimit int,
) (model.QueryResult, error) {
	if session == nil {
		return model.QueryResult{}, fmt.Errorf("query PostgreSQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sql); err != nil {
		return model.QueryResult{}, err
	}
	if rowLimit <= 0 {
		return model.QueryResult{}, fmt.Errorf("query PostgreSQL session: row limit must be positive")
	}
	classification, err := classifySessionStatement(sql, "postgres")
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("classify PostgreSQL session statement: %w", err)
	}
	started := session.state.clock.Now()
	result, executionError := session.executor.queryWithRunner(ctx, session.runner, sql, rowLimit)
	session.state.finishStatement(
		classification,
		started,
		int64(result.RowCount),
		executionError == nil,
	)
	return result, executionError
}

func (session *postgresSession) Execute(
	ctx context.Context,
	sql string,
) (model.QueryResult, error) {
	if session == nil {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sql); err != nil {
		return model.QueryResult{}, err
	}
	if session.executor.readOnly {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL session write: %w", ErrReadOnlyViolated)
	}
	classification, err := classifySessionStatement(sql, "postgres")
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("classify PostgreSQL session statement: %w", err)
	}
	started := session.state.clock.Now()
	result, executionError := session.executor.executeWithRunner(ctx, session.runner, sql)
	session.state.finishStatement(
		classification,
		started,
		int64(result.RowCount),
		executionError == nil,
	)
	return result, executionError
}

func (session *postgresSession) Explain(
	ctx context.Context,
	sql string,
) (model.ExplainInfo, error) {
	if session == nil {
		return model.ExplainInfo{}, fmt.Errorf("explain PostgreSQL session: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.validateOperation(ctx, sql); err != nil {
		return model.ExplainInfo{}, err
	}
	result, err := session.executor.explainWithRunner(ctx, session.runner, sql)
	session.state.touchStatementEnd()
	return result, err
}

func (session *postgresSession) TransactionState() (rules.TransactionState, error) {
	if session == nil {
		return rules.TransactionState{}, fmt.Errorf("read PostgreSQL session state: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if session.closed {
		return rules.TransactionState{}, fmt.Errorf("read PostgreSQL session state: %w", ErrSessionClosed)
	}
	inTransaction, ageMS, idleMS, _, err := session.state.snapshot()
	if err != nil {
		return rules.TransactionState{}, fmt.Errorf("read PostgreSQL session state: %w", err)
	}
	return rules.TransactionState{
		InTransaction: inTransaction,
		AgeMS:         ageMS,
		IdleMS:        idleMS,
	}, nil
}

func (*postgresSession) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, fmt.Errorf(
		"MySQL transaction state is unavailable for PostgreSQL session",
	)
}

func (session *postgresSession) TableHasIndex(schema, table string) (bool, error) {
	if session == nil {
		return false, fmt.Errorf("check PostgreSQL session table index: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if session.closed || session.executor == nil {
		return false, fmt.Errorf("check PostgreSQL session table index: %w", ErrSessionClosed)
	}
	return session.executor.TableHasIndex(schema, table)
}

func (session *postgresSession) TableRowCount(schema, table string) (int64, error) {
	if session == nil {
		return 0, fmt.Errorf("read PostgreSQL session table rows: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if session.closed || session.executor == nil {
		return 0, fmt.Errorf("read PostgreSQL session table rows: %w", ErrSessionClosed)
	}
	return session.executor.TableRowCount(schema, table)
}

func (session *postgresSession) Close() error {
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
			"close PostgreSQL session %q after rollback failure: %w",
			session.id,
			errors.Join(rollbackError, discardError),
		)
	}
	if session.release != nil {
		session.release()
	}
	return nil
}

func (session *postgresSession) validateOperation(ctx context.Context, sql string) error {
	if session.closed || session.executor == nil || isNilValue(session.runner) {
		return fmt.Errorf("PostgreSQL session operation: %w", ErrSessionClosed)
	}
	if ctx == nil {
		return fmt.Errorf("PostgreSQL session operation context is nil")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("PostgreSQL session SQL is empty")
	}
	return nil
}

func (executor *PostgresExecutor) removeSession(id string, session Session) {
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if current, exists := executor.sessions[id]; exists && current == session {
		delete(executor.sessions, id)
	}
}

var (
	_ Executor                          = (*PostgresExecutor)(nil)
	_ rules.MetadataProvider            = (*PostgresExecutor)(nil)
	_ rules.TransactionMetadataProvider = (*PostgresExecutor)(nil)
	_ Session                           = (*postgresSession)(nil)
	_ rules.TransactionMetadataProvider = (*postgresSession)(nil)
)
