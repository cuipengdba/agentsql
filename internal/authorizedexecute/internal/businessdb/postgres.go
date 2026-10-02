package businessdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	executionTimeoutMargin  = 250 * time.Millisecond
	maxDatabaseFrameBytes   = 1 << 20
	postgresFrameHeaderSize = 5
	postgresCloseTimeout    = 2 * time.Second
	maxExplainPlanBytes     = 512 << 10
	maxExplainPlanNodes     = 1024
	openTenBaseV2Version    = "10.0 OpenTenBase V2"
)

var openTenBaseMissingComma = regexp.MustCompile(
	`("Node/s"\s*:\s*"[A-Za-z][A-Za-z0-9_-]{0,63}")(\s*)("Remote plan"\s*:)`,
)

var openTenBaseNodeList = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_-]{0,63}(?:\s*,\s*[A-Za-z][A-Za-z0-9_-]{0,63})*$`,
)

type postgresExecutorResource interface {
	closeForExecutor(context.Context) error
}

// PostgresExecutor controls one PostgreSQL connection pool.
type PostgresExecutor struct {
	pool          *pgxpool.Pool
	maxConns      int32
	timeout       time.Duration
	readOnly      bool
	clock         sessionClock
	sessionsMu    sync.RWMutex
	sessions      map[string]Session
	resourcesMu   sync.Mutex
	resources     map[postgresExecutorResource]struct{}
	serverVersion string
	closed        bool
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
	config.ConnConfig.BuildFrontend = newBoundedPostgresFrontend
	// NOTICE/WARNING payloads are database-controlled diagnostics and must not
	// cross the capability boundary or enter ordinary logs.
	config.ConnConfig.OnNotice = func(*pgconn.PgConn, *pgconn.Notice) {}
	if readOnly {
		config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, postgresConnectionError(ctx, DBStageConnect, "create PostgreSQL connection pool", err)
	}
	executor := &PostgresExecutor{
		pool:      pool,
		maxConns:  int32(connectionLimit),
		timeout:   time.Duration(timeoutMS) * time.Millisecond,
		readOnly:  readOnly,
		clock:     wallClock{},
		sessions:  make(map[string]Session),
		resources: make(map[postgresExecutorResource]struct{}),
	}
	if err := executor.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	// server_version is capability evidence, not a datasource-type guess. A
	// failed probe leaves the standard PostgreSQL parser selected (fail closed
	// if the server later returns non-standard JSON).
	versionContext, versionCancel := executor.timeoutContext(ctx)
	defer versionCancel()
	_ = pool.QueryRow(versionContext, "SHOW server_version").Scan(&executor.serverVersion)
	return executor, nil
}

func newBoundedPostgresFrontend(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
	frontend := pgproto3.NewFrontend(reader, writer)
	frontend.SetMaxBodyLen(maxDatabaseFrameBytes - postgresFrameHeaderSize)
	return frontend
}

func (executor *PostgresExecutor) poolSnapshot() PoolStat {
	if executor == nil {
		return PoolStat{}
	}
	result := PoolStat{MaxOpen: int(executor.maxConns)}
	if executor.pool == nil {
		return result
	}
	stat := executor.pool.Stat()
	result.InUse = int(stat.AcquiredConns())
	result.Idle = int(stat.IdleConns())
	return result
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
		return postgresConnectionError(timedContext, DBStagePing, "ping PostgreSQL datasource", err)
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
		return nil, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL session connection", err)
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
		beginTx: func(ctx context.Context) (pgx.Tx, error) {
			return connection.BeginTx(ctx, pgx.TxOptions{})
		},
	}
	executor.sessions[sessionID] = session
	return session, nil
}

// BeginWriteTx starts a PostgreSQL write transaction on a pooled connection.
// The caller's request context is bound to BeginTx; statement timeouts are
// applied separately by Execute.
func (executor *PostgresExecutor) BeginWriteTx(ctx context.Context) (WriteTx, error) {
	if executor == nil || executor.pool == nil || ctx == nil {
		return nil, fmt.Errorf("begin PostgreSQL write transaction: executor or context is unavailable")
	}
	if executor.readOnly {
		return nil, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageBeginTx,
			"",
			ErrReadOnlyViolated,
		)
	}
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL write transaction", err)
	}
	return &postgresWriteTx{executor: executor, tx: tx}, nil
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
		return model.QueryResult{}, postgresDatabaseError(timedContext, DBStageQuery, "query PostgreSQL datasource", err)
	}
	result, err := collectRows(&postgresRowSource{rows: rows}, rowLimit)
	if err != nil {
		return model.QueryResult{}, postgresDatabaseError(timedContext, DBStageReadRows, "read PostgreSQL query result", err)
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
		return model.QueryResult{}, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageExecute,
			"",
			ErrReadOnlyViolated,
		)
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
	return executor.executeWithRunnerAtStage(ctx, runner, sql, DBStageExecute)
}

func (executor *PostgresExecutor) executeWithRunnerAtStage(
	ctx context.Context,
	runner postgresRunner,
	sql string,
	stage DBStage,
) (model.QueryResult, error) {
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	commandTag, err := runner.Exec(timedContext, sql)
	if err != nil {
		return model.QueryResult{}, postgresDatabaseError(timedContext, stage, "execute PostgreSQL statement", err)
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
		return model.ExplainInfo{}, postgresDatabaseError(timedContext, DBStageExplain, "explain PostgreSQL statement", err)
	}
	var info model.ExplainInfo
	var err error
	if isSupportedOpenTenBaseVersion(executor.serverVersion) {
		info, err = parseOpenTenBaseExplainJSON(raw, executor.serverVersion)
	} else {
		info, err = parsePostgresExplainJSON(raw)
	}
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
	executor.resourcesMu.Lock()
	resources := make([]postgresExecutorResource, 0, len(executor.resources))
	for resource := range executor.resources {
		resources = append(resources, resource)
	}
	executor.resourcesMu.Unlock()
	closeContext, cancel := context.WithTimeout(context.Background(), postgresCloseTimeout)
	defer cancel()
	gracefulDone := make(chan []error, 1)
	go func() {
		gracefulErrors := make([]error, 0)
		for _, session := range sessions {
			if err := session.Close(); err != nil {
				gracefulErrors = append(gracefulErrors, err)
			}
		}
		for _, resource := range resources {
			if err := resource.closeForExecutor(closeContext); err != nil {
				gracefulErrors = append(gracefulErrors, err)
			}
		}
		gracefulDone <- gracefulErrors
	}()
	closeErrors := make([]error, 0)
	select {
	case gracefulErrors := <-gracefulDone:
		closeErrors = append(closeErrors, gracefulErrors...)
	case <-closeContext.Done():
		closeErrors = append(closeErrors, fmt.Errorf("close PostgreSQL managed resources: %w", closeContext.Err()))
	}
	poolClosed := make(chan struct{})
	go func() {
		executor.pool.Close()
		close(poolClosed)
	}()
	select {
	case <-poolClosed:
	case <-time.After(postgresCloseTimeout):
		// Reset immediately destroys idle connections and marks any connection
		// returned after this point for destruction. Managed request resources
		// were explicitly closed above, so reaching this branch indicates an
		// unowned acquisition rather than a request-handle leak.
		executor.pool.Reset()
		closeErrors = append(closeErrors, fmt.Errorf("close PostgreSQL pool: timed out with %d acquired connections", executor.pool.Stat().AcquiredConns()))
	}
	if len(closeErrors) > 0 {
		return fmt.Errorf("close PostgreSQL executor: %w", errors.Join(closeErrors...))
	}
	return nil
}

func (executor *PostgresExecutor) registerResource(resource postgresExecutorResource) error {
	if executor == nil || resource == nil {
		return fmt.Errorf("register PostgreSQL resource: executor or resource is unavailable")
	}
	executor.sessionsMu.RLock()
	defer executor.sessionsMu.RUnlock()
	if executor.closed {
		return fmt.Errorf("register PostgreSQL resource: %w", ErrSessionClosed)
	}
	executor.resourcesMu.Lock()
	defer executor.resourcesMu.Unlock()
	if executor.resources == nil {
		executor.resources = make(map[postgresExecutorResource]struct{})
	}
	executor.resources[resource] = struct{}{}
	return nil
}

func (executor *PostgresExecutor) unregisterResource(resource postgresExecutorResource) {
	if executor == nil || resource == nil {
		return
	}
	executor.resourcesMu.Lock()
	delete(executor.resources, resource)
	executor.resourcesMu.Unlock()
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
		return false, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL index metadata", err)
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
		return 0, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL table row metadata", err)
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
	NodeType   string                    `json:"Node Type"`
	PlanRows   *float64                  `json:"Plan Rows"`
	TotalCost  *float64                  `json:"Total Cost"`
	IndexName  string                    `json:"Index Name"`
	Nodes      string                    `json:"Node/s"`
	Plans      []postgresExplainPlan     `json:"Plans"`
	RemotePlan []postgresExplainDocument `json:"Remote plan"`
}

func parsePostgresExplainJSON(raw []byte) (model.ExplainInfo, error) {
	if err := validateExplainPayload(raw); err != nil {
		return model.ExplainInfo{}, err
	}
	var documents []postgresExplainDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		return model.ExplainInfo{}, fmt.Errorf("decode JSON plan: %w", err)
	}
	if len(documents) != 1 || strings.TrimSpace(documents[0].Plan.NodeType) == "" {
		return model.ExplainInfo{}, fmt.Errorf("JSON plan must contain one root Plan")
	}
	plan := documents[0].Plan
	if plan.PlanRows == nil || plan.TotalCost == nil || invalidPlanEstimate(*plan.PlanRows) ||
		invalidPlanEstimate(*plan.TotalCost) {
		return model.ExplainInfo{}, fmt.Errorf("JSON plan contains invalid estimates")
	}
	return model.ExplainInfo{
		EstScanRows: int64(*plan.PlanRows),
		EstCost:     *plan.TotalCost,
		SeqScan:     plan.NodeType == "Seq Scan",
		UsesIndex: plan.IndexName != "" || strings.Contains(plan.NodeType, "Index") ||
			strings.Contains(plan.NodeType, "Bitmap"),
		Raw: string(raw),
	}, nil
}

func isSupportedOpenTenBaseVersion(version string) bool {
	return strings.TrimSpace(version) == openTenBaseV2Version
}

func parseOpenTenBaseExplainJSON(raw []byte, serverVersion string) (model.ExplainInfo, error) {
	if !isSupportedOpenTenBaseVersion(serverVersion) {
		return model.ExplainInfo{}, fmt.Errorf("unsupported OpenTenBase server version")
	}
	if err := validateExplainPayload(raw); err != nil {
		return model.ExplainInfo{}, err
	}

	normalized := raw
	if !json.Valid(normalized) {
		matches := openTenBaseMissingComma.FindAllIndex(normalized, -1)
		if len(matches) != 1 {
			return model.ExplainInfo{}, fmt.Errorf("unsupported OpenTenBase JSON plan shape")
		}
		normalized = openTenBaseMissingComma.ReplaceAll(normalized, []byte("$1,$2$3"))
		if !json.Valid(normalized) {
			return model.ExplainInfo{}, fmt.Errorf("decode OpenTenBase JSON plan")
		}
	}

	var documents []postgresExplainDocument
	if err := json.Unmarshal(normalized, &documents); err != nil {
		return model.ExplainInfo{}, fmt.Errorf("decode OpenTenBase JSON plan: %w", err)
	}
	if len(documents) != 1 {
		return model.ExplainInfo{}, fmt.Errorf("OpenTenBase JSON plan must contain one root Plan")
	}
	wrapper := documents[0].Plan
	if wrapper.PlanRows == nil || wrapper.TotalCost == nil || invalidPlanEstimate(*wrapper.PlanRows) ||
		invalidPlanEstimate(*wrapper.TotalCost) {
		return model.ExplainInfo{}, fmt.Errorf("OpenTenBase root plan contains invalid estimates")
	}

	root := wrapper
	if wrapper.NodeType == "Remote Fast Query Execution" {
		if !openTenBaseNodeList.MatchString(wrapper.Nodes) || len(wrapper.Plans) != 0 ||
			len(wrapper.RemotePlan) != 1 {
			return model.ExplainInfo{}, fmt.Errorf("unsupported OpenTenBase remote root plan")
		}
		root = wrapper.RemotePlan[0].Plan
	} else if !json.Valid(raw) || wrapper.Nodes != "" || len(wrapper.RemotePlan) != 0 {
		// The missing-comma repair is only valid for the observed distributed
		// wrapper. A local OpenTenBase plan must already be standard JSON.
		return model.ExplainInfo{}, fmt.Errorf("unsupported OpenTenBase local root plan")
	}
	if strings.TrimSpace(root.NodeType) == "" {
		return model.ExplainInfo{}, fmt.Errorf("OpenTenBase remote plan is empty")
	}
	info, nodeCount, scanCount, err := summarizePostgresCompatPlan(root, 0)
	if err != nil {
		return model.ExplainInfo{}, err
	}
	if nodeCount == 0 || nodeCount > maxExplainPlanNodes {
		return model.ExplainInfo{}, fmt.Errorf("OpenTenBase plan has invalid node count")
	}
	if scanCount == 0 {
		info.EstScanRows = int64(math.Ceil(*root.PlanRows))
	}
	info.Raw = fmt.Sprintf(
		"opentenbase-v2 nodes=%d scan_rows=%d cost=%.6g seq_scan=%t uses_index=%t",
		nodeCount, info.EstScanRows, info.EstCost, info.SeqScan, info.UsesIndex,
	)
	return info, nil
}

func validateExplainPayload(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxExplainPlanBytes || !utf8.Valid(raw) {
		return fmt.Errorf("explain plan payload is empty, oversized, or invalid UTF-8")
	}
	return nil
}

func summarizePostgresCompatPlan(plan postgresExplainPlan, depth int) (model.ExplainInfo, int, int, error) {
	if depth > maxExplainPlanNodes || !knownPostgresPlanNode(plan.NodeType) || len(plan.RemotePlan) != 0 {
		return model.ExplainInfo{}, 0, 0, fmt.Errorf("unsupported PostgreSQL plan node")
	}
	if plan.PlanRows == nil || plan.TotalCost == nil || invalidPlanEstimate(*plan.PlanRows) ||
		invalidPlanEstimate(*plan.TotalCost) {
		return model.ExplainInfo{}, 0, 0, fmt.Errorf("OpenTenBase plan contains invalid estimates")
	}
	info := model.ExplainInfo{EstCost: *plan.TotalCost}
	nodes := 1
	scans := 0
	if postgresScanNode(plan.NodeType) {
		if *plan.PlanRows > math.MaxInt64 || *plan.PlanRows > float64(math.MaxInt64-info.EstScanRows) {
			return model.ExplainInfo{}, 0, 0, fmt.Errorf("OpenTenBase rows estimate overflows int64")
		}
		info.EstScanRows = int64(math.Ceil(*plan.PlanRows))
		scans = 1
	}
	info.SeqScan = plan.NodeType == "Seq Scan"
	info.UsesIndex = plan.IndexName != "" || strings.Contains(plan.NodeType, "Index") ||
		strings.Contains(plan.NodeType, "Bitmap")
	for _, child := range plan.Plans {
		childInfo, childNodes, childScans, err := summarizePostgresCompatPlan(child, depth+1)
		if err != nil {
			return model.ExplainInfo{}, 0, 0, err
		}
		if info.EstScanRows > math.MaxInt64-childInfo.EstScanRows {
			return model.ExplainInfo{}, 0, 0, fmt.Errorf("OpenTenBase rows estimate overflows int64")
		}
		info.EstScanRows += childInfo.EstScanRows
		info.EstCost = math.Max(info.EstCost, childInfo.EstCost)
		info.SeqScan = info.SeqScan || childInfo.SeqScan
		info.UsesIndex = info.UsesIndex || childInfo.UsesIndex
		nodes += childNodes
		scans += childScans
		if nodes > maxExplainPlanNodes {
			return model.ExplainInfo{}, 0, 0, fmt.Errorf("OpenTenBase plan has too many nodes")
		}
	}
	return info, nodes, scans, nil
}

func invalidPlanEstimate(value float64) bool {
	return value < 0 || value > math.MaxInt64 || math.IsNaN(value) || math.IsInf(value, 0)
}

func postgresScanNode(nodeType string) bool {
	return strings.Contains(nodeType, "Scan")
}

func knownPostgresPlanNode(nodeType string) bool {
	switch nodeType {
	case "Aggregate", "Append", "Bitmap Heap Scan", "Bitmap Index Scan", "CTE Scan",
		"Foreign Scan", "Function Scan", "Gather", "Gather Merge", "GroupAggregate",
		"Hash", "Hash Join", "HashAggregate", "Index Only Scan", "Index Scan", "Limit",
		"LockRows", "Materialize", "Merge Append", "Merge Join", "Nested Loop", "ProjectSet",
		"Result", "Seq Scan", "SetOp", "Sort", "Subquery Scan", "Tid Scan", "Unique",
		"Values Scan", "WindowAgg", "WorkTable Scan":
		return true
	default:
		return false
	}
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

func postgresDatabaseError(ctx context.Context, stage DBStage, message string, cause error) error {
	var resource *ResourceError
	if errors.As(cause, &resource) {
		return resource
	}
	if classified, ok := classifyPostgresError(ctx, stage, cause); ok {
		return classified
	}
	return safeDatabaseError(message, cause)
}

func classifyPostgresError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified, true
	}

	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		// The native binder deliberately uses a fixed SQLSTATE/message pair for
		// this authorization precondition. Match both values exactly: 0A000 is
		// otherwise a broad PostgreSQL feature-not-supported class and must keep
		// its ordinary database-error semantics.
		if postgresError.Code == "0A000" &&
			postgresError.Message == "AgentSQL function must be explicitly pg_catalog qualified" {
			return catalogAuthError("AUTH_FUNCTION_QUALIFICATION_REQUIRED"), true
		}
		kind, code, compat := postgresErrorCode(postgresError.Code)
		return newDBError(kind, code, stage, postgresError.Code, compat), true
	}

	var connectError *pgconn.ConnectError
	if errors.As(cause, &connectError) || isNetworkConnectionError(cause) {
		return newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			stage,
			"",
			ErrDatasourceUnreachable,
		), true
	}
	return nil, false
}

func postgresErrorCode(driverCode string) (DBErrorKind, DBErrorCode, error) {
	switch driverCode {
	case "42P01", "42704", "42883", "3F000":
		return DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound, nil
	case "42703":
		return DBErrorKindColumnNotFound, DBErrorCodeColumnNotFound, nil
	case "42P04", "42P06", "42P07", "42710", "42723":
		return DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists, nil
	case "42601":
		return DBErrorKindSyntax, DBErrorCodeSyntax, nil
	case "44000":
		return DBErrorKindConstraint, DBErrorCodeConstraint, nil
	case "40001", "40P01", "55P03":
		return DBErrorKindRetryable, DBErrorCodeRetryable, nil
	case "2D000":
		return DBErrorKindTransaction, DBErrorCodeTransaction, nil
	case "57014":
		return DBErrorKindInterrupted, DBErrorCodeInterrupted, ErrQueryTimeout
	case "42501":
		return DBErrorKindPermission, DBErrorCodePermission, ErrPermissionDenied
	case "25006":
		return DBErrorKindReadOnly, DBErrorCodeReadOnly, ErrReadOnlyViolated
	case "28000", "28P01":
		return DBErrorKindAuthentication, DBErrorCodeAuthentication, nil
	case "3D000":
		return DBErrorKindDatabaseNotFound, DBErrorCodeDatabaseNotFound, nil
	case "57P01", "57P02", "57P03", "57P04", "57P05":
		return DBErrorKindConnection, DBErrorCodeConnection, ErrDatasourceUnreachable
	}

	switch {
	case strings.HasPrefix(driverCode, "42"):
		return DBErrorKindSemantic, DBErrorCodeSemantic, nil
	case strings.HasPrefix(driverCode, "22"):
		return DBErrorKindData, DBErrorCodeData, nil
	case strings.HasPrefix(driverCode, "23"):
		return DBErrorKindConstraint, DBErrorCodeConstraint, nil
	case strings.HasPrefix(driverCode, "25"):
		return DBErrorKindTransaction, DBErrorCodeTransaction, nil
	case strings.HasPrefix(driverCode, "53"), strings.HasPrefix(driverCode, "54"):
		return DBErrorKindResource, DBErrorCodeResource, nil
	case strings.HasPrefix(driverCode, "08"):
		return DBErrorKindConnection, DBErrorCodeConnection, ErrDatasourceUnreachable
	default:
		return DBErrorKindExecution, DBErrorCodeExecution, nil
	}
}

func classifyPostgresPermission(cause error) bool {
	var postgresError *pgconn.PgError
	return errors.As(cause, &postgresError) && postgresError.Code == "42501"
}

func postgresConnectionError(
	ctx context.Context,
	stage DBStage,
	message string,
	cause error,
) error {
	if classified, ok := classifyPostgresError(ctx, stage, cause); ok {
		return addDBErrorCompat(classified, ErrDatasourceUnreachable)
	}
	return safeError(message, ErrDatasourceUnreachable, cause)
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
	beginTx     func(context.Context) (pgx.Tx, error)
	operationMu sync.Mutex
	closed      bool
}

func (session *postgresSession) BeginWriteTx(ctx context.Context) (WriteTx, error) {
	if session == nil {
		return nil, fmt.Errorf("begin PostgreSQL session write transaction: %w", ErrSessionClosed)
	}
	session.operationMu.Lock()
	if session.closed || session.executor == nil || ctx == nil {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin PostgreSQL session write transaction: session or context is unavailable")
	}
	if session.executor.readOnly {
		session.operationMu.Unlock()
		return nil, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageBeginTx,
			"",
			ErrReadOnlyViolated,
		)
	}
	if session.state != nil && session.state.active() {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin PostgreSQL session write transaction: %w", ErrSessionTransactionActive)
	}
	if session.beginTx == nil {
		session.operationMu.Unlock()
		return nil, fmt.Errorf("begin PostgreSQL session write transaction: transaction starter is unavailable")
	}
	tx, err := session.beginTx(ctx)
	if err != nil {
		session.operationMu.Unlock()
		return nil, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL session write transaction", err)
	}
	return &postgresWriteTx{
		executor: session.executor,
		tx:       tx,
		terminal: session.operationMu.Unlock,
	}, nil
}

type postgresWriteTx struct {
	mu       sync.Mutex
	executor *PostgresExecutor
	tx       pgx.Tx
	done     bool
	terminal func()
}

func (tx *postgresWriteTx) Execute(ctx context.Context, sqlText string) (model.QueryResult, error) {
	if tx == nil {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL write transaction: %w", ErrTransactionDone)
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done || tx.tx == nil || tx.executor == nil {
		return model.QueryResult{}, fmt.Errorf("execute PostgreSQL write transaction: %w", ErrTransactionDone)
	}
	if err := tx.executor.validateOperation(ctx, sqlText); err != nil {
		return model.QueryResult{}, err
	}
	return tx.executor.executeWithRunner(ctx, tx.tx, sqlText)
}

func (tx *postgresWriteTx) Commit(ctx context.Context) error {
	return tx.finish(ctx, true)
}

func (tx *postgresWriteTx) Rollback(ctx context.Context) error {
	return tx.finish(ctx, false)
}

func (tx *postgresWriteTx) finish(ctx context.Context, commit bool) error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("finish PostgreSQL write transaction: context is nil")
	}
	var err error
	if commit {
		err = tx.tx.Commit(ctx)
	} else {
		err = tx.tx.Rollback(ctx)
	}
	tx.done = true
	tx.tx = nil
	if tx.terminal != nil {
		tx.terminal()
		tx.terminal = nil
	}
	if err != nil {
		action := "rollback"
		stage := DBStageRollback
		if commit {
			action = "commit"
			stage = DBStageCommit
		}
		return postgresDatabaseError(ctx, stage, action+" PostgreSQL write transaction", err)
	}
	return nil
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
		return model.QueryResult{}, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageExecute,
			"",
			ErrReadOnlyViolated,
		)
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
		_, rollbackError = session.executor.executeWithRunnerAtStage(
			context.Background(),
			session.runner,
			"ROLLBACK",
			DBStageRollback,
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
