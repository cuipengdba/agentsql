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
	pool     *pgxpool.Pool
	timeout  time.Duration
	readOnly bool
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
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	rows, err := executor.pool.Query(timedContext, sql)
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
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	commandTag, err := executor.pool.Exec(timedContext, sql)
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
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	var raw []byte
	if err := executor.pool.QueryRow(
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
	executor.pool.Close()
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

// TransactionState implements rules.TransactionMetadataProvider.
func (executor *PostgresExecutor) TransactionState() (rules.TransactionState, error) {
	if executor == nil || executor.pool == nil {
		return rules.TransactionState{}, fmt.Errorf("read PostgreSQL transaction state: executor is closed")
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
		return rules.TransactionState{}, postgresDatabaseError("read PostgreSQL transaction state", err)
	}
	return state, nil
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

var (
	_ Executor                          = (*PostgresExecutor)(nil)
	_ rules.MetadataProvider            = (*PostgresExecutor)(nil)
	_ rules.TransactionMetadataProvider = (*PostgresExecutor)(nil)
)
