package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// limitedSQLExecutor is the deliberately small common substrate for dialects
// whose parser, transaction-state model and EXPLAIN decoder are not yet part
// of the authorization boundary. It supports pool/session lifecycle and typed
// metadata only. Every general SQL operation fails closed.
type limitedSQLExecutor struct {
	database   *sql.DB
	dialect    string
	username   string
	maxConns   int
	timeout    time.Duration
	readOnly   bool
	sessionsMu sync.Mutex
	sessions   map[string]*limitedSQLSession
	closed     bool
}

func newLimitedSQLExecutor(
	ctx context.Context,
	datasource model.Datasource,
	driverName string,
	dsn string,
	dialect string,
	password string,
	readOnly bool,
) (*limitedSQLExecutor, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open %s datasource: context is nil", dialect)
	}
	if err := validateDatasource(datasource); err != nil {
		return nil, fmt.Errorf("open %s datasource: %w", dialect, err)
	}
	if password == "" {
		return nil, fmt.Errorf("open %s datasource: password is required", dialect)
	}
	connectionLimit, err := normalizedConnectionLimit(datasource.ConnLimit)
	if err != nil {
		return nil, fmt.Errorf("open %s datasource: %w", dialect, err)
	}
	timeoutMS, err := normalizedStatementTimeout(datasource.StmtTimeoutMS)
	if err != nil {
		return nil, fmt.Errorf("open %s datasource: %w", dialect, err)
	}
	database, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			DBStageConnect,
			"",
			ErrDatasourceUnreachable,
		)
	}
	database.SetMaxOpenConns(connectionLimit)
	database.SetMaxIdleConns(connectionLimit)
	database.SetConnMaxLifetime(mysqlConnectionMaxLifetime)
	executor := &limitedSQLExecutor{
		database: database,
		dialect:  dialect,
		username: datasource.Username,
		maxConns: connectionLimit,
		timeout:  time.Duration(timeoutMS) * time.Millisecond,
		readOnly: readOnly,
		sessions: make(map[string]*limitedSQLSession),
	}
	if err := executor.Ping(ctx); err != nil {
		closeErr := database.Close()
		if closeErr != nil {
			return nil, errors.Join(err, safeDatabaseError("close failed datasource pool", closeErr))
		}
		return nil, err
	}
	return executor, nil
}

func (executor *limitedSQLExecutor) Dialect() string {
	if executor == nil {
		return ""
	}
	return executor.dialect
}

func (executor *limitedSQLExecutor) Ping(ctx context.Context) error {
	if executor == nil || executor.database == nil || ctx == nil {
		return newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			DBStagePing,
			"",
			ErrDatasourceUnreachable,
		)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	if err := executor.database.PingContext(timedContext); err != nil {
		if errors.Is(timedContext.Err(), context.DeadlineExceeded) {
			return newDBError(DBErrorKindTimeout, DBErrorCodeTimeout, DBStagePing, "", ErrQueryTimeout)
		}
		return newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			DBStagePing,
			"",
			ErrDatasourceUnreachable,
		)
	}
	return nil
}

func (executor *limitedSQLExecutor) OpenSession(ctx context.Context, sessionID string) (Session, error) {
	if executor == nil || executor.database == nil || ctx == nil {
		return nil, fmt.Errorf("open dialect session: executor or context is unavailable")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return nil, fmt.Errorf("open dialect session: invalid session ID")
	}
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if executor.closed {
		return nil, fmt.Errorf("open dialect session: %w", ErrSessionClosed)
	}
	if _, exists := executor.sessions[sessionID]; exists {
		return nil, fmt.Errorf("open dialect session %q: %w", sessionID, ErrSessionExists)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	connection, err := executor.database.Conn(timedContext)
	if err != nil {
		return nil, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageAcquire, "", ErrDatasourceUnreachable)
	}
	session := &limitedSQLSession{id: sessionID, executor: executor, connection: connection}
	executor.sessions[sessionID] = session
	return session, nil
}

func (executor *limitedSQLExecutor) BeginWriteTx(context.Context) (WriteTx, error) {
	if executor != nil && executor.readOnly {
		return nil, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageBeginTx,
			"",
			ErrReadOnlyViolated,
		)
	}
	return nil, executor.unsupported(DBStageBeginTx)
}

func (executor *limitedSQLExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{}, executor.unsupported(DBStageExplain)
}

func (executor *limitedSQLExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{}, executor.unsupported(DBStageQuery)
}

func (executor *limitedSQLExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	if executor != nil && executor.readOnly {
		return model.QueryResult{}, newDBError(
			DBErrorKindReadOnly,
			DBErrorCodeReadOnly,
			DBStageExecute,
			"",
			ErrReadOnlyViolated,
		)
	}
	return model.QueryResult{}, executor.unsupported(DBStageExecute)
}

func (executor *limitedSQLExecutor) Close() error {
	if executor == nil || executor.database == nil {
		return nil
	}
	executor.sessionsMu.Lock()
	if executor.closed {
		executor.sessionsMu.Unlock()
		return nil
	}
	executor.closed = true
	sessions := make([]*limitedSQLSession, 0, len(executor.sessions))
	for _, session := range executor.sessions {
		sessions = append(sessions, session)
	}
	executor.sessions = make(map[string]*limitedSQLSession)
	executor.sessionsMu.Unlock()
	closeErrors := make([]error, 0, len(sessions)+1)
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if err := executor.database.Close(); err != nil {
		closeErrors = append(closeErrors, safeDatabaseError("close datasource pool", err))
	}
	if len(closeErrors) > 0 {
		return errors.Join(closeErrors...)
	}
	return nil
}

func (executor *limitedSQLExecutor) poolSnapshot() PoolStat {
	if executor == nil {
		return PoolStat{}
	}
	result := PoolStat{MaxOpen: executor.maxConns}
	if executor.database == nil {
		return result
	}
	stats := executor.database.Stats()
	result.InUse = stats.InUse
	result.Idle = stats.Idle
	return result
}

func (executor *limitedSQLExecutor) probeCurrentSchema(ctx context.Context, query string) error {
	if executor == nil || executor.database == nil || ctx == nil || strings.TrimSpace(query) == "" {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageMetadata, "", ErrDatasourceUnreachable)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	var currentSchema string
	if err := executor.database.QueryRowContext(timedContext, query).Scan(&currentSchema); err != nil {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
	}
	if strings.TrimSpace(currentSchema) == "" {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
	}
	executor.username = currentSchema
	return nil
}

func (executor *limitedSQLExecutor) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if executor == nil || executor.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, executor.timeout)
}

func (executor *limitedSQLExecutor) unsupported(stage DBStage) error {
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func (executor *limitedSQLExecutor) removeSession(id string, session *limitedSQLSession) {
	if executor == nil {
		return
	}
	executor.sessionsMu.Lock()
	defer executor.sessionsMu.Unlock()
	if current, exists := executor.sessions[id]; exists && current == session {
		delete(executor.sessions, id)
	}
}

type limitedSQLSession struct {
	id         string
	executor   *limitedSQLExecutor
	connection *sql.Conn
	mu         sync.Mutex
	closed     bool
}

func (session *limitedSQLSession) BeginWriteTx(context.Context) (WriteTx, error) {
	if err := session.validate(); err != nil {
		return nil, err
	}
	if session.executor.readOnly {
		return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageBeginTx, "", ErrReadOnlyViolated)
	}
	return nil, session.executor.unsupported(DBStageBeginTx)
}

func (session *limitedSQLSession) Query(context.Context, string, int) (model.QueryResult, error) {
	if err := session.validate(); err != nil {
		return model.QueryResult{}, err
	}
	return model.QueryResult{}, session.executor.unsupported(DBStageQuery)
}

func (session *limitedSQLSession) Execute(context.Context, string) (model.QueryResult, error) {
	if err := session.validate(); err != nil {
		return model.QueryResult{}, err
	}
	if session.executor.readOnly {
		return model.QueryResult{}, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageExecute, "", ErrReadOnlyViolated)
	}
	return model.QueryResult{}, session.executor.unsupported(DBStageExecute)
}

func (session *limitedSQLSession) Explain(context.Context, string) (model.ExplainInfo, error) {
	if err := session.validate(); err != nil {
		return model.ExplainInfo{}, err
	}
	return model.ExplainInfo{}, session.executor.unsupported(DBStageExplain)
}

func (*limitedSQLSession) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, fmt.Errorf("PostgreSQL transaction state is unavailable for limited dialect session")
}

func (*limitedSQLSession) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, fmt.Errorf("MySQL transaction state is unavailable for limited dialect session")
}

func (session *limitedSQLSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	connection := session.connection
	session.connection = nil
	executor := session.executor
	session.mu.Unlock()
	if executor != nil {
		executor.removeSession(session.id, session)
	}
	if connection != nil {
		if err := connection.Close(); err != nil {
			return safeDatabaseError("close dialect session connection", err)
		}
	}
	return nil
}

func (session *limitedSQLSession) validate() error {
	if session == nil {
		return fmt.Errorf("dialect session operation: %w", ErrSessionClosed)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.executor == nil || session.connection == nil {
		return fmt.Errorf("dialect session operation: %w", ErrSessionClosed)
	}
	return nil
}

var _ Session = (*limitedSQLSession)(nil)
