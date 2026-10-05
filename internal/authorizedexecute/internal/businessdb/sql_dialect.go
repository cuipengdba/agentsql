package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// limitedSQLExecutor is the deliberately small common substrate for dialects
// whose full parser and transaction-state model are not yet part of the
// authorization boundary. It supports lifecycle, typed metadata and a narrow
// read-only SELECT subset. Writes and unavailable plan decoders fail closed.
type limitedSQLExecutor struct {
	database   *sql.DB
	dialect    string
	username   string
	maxConns   int
	timeout    time.Duration
	readOnly   bool
	classifier limitedDialectErrorClassifier
	explainer  limitedDialectExplainer
	sessionsMu sync.Mutex
	sessions   map[string]*limitedSQLSession
	closed     bool
}

type limitedDialectErrorClassifier func(context.Context, DBStage, error) (error, bool)

type limitedDialectRunner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type limitedDialectExplainer func(context.Context, limitedDialectRunner, string) (model.ExplainInfo, error)

func newLimitedSQLExecutor(
	ctx context.Context,
	datasource model.Datasource,
	driverName string,
	dsn string,
	dialect string,
	password string,
	readOnly bool,
) (*limitedSQLExecutor, error) {
	return newLimitedSQLExecutorWithDialect(
		ctx, datasource, driverName, dsn, dialect, password, readOnly, nil, nil,
	)
}

func newLimitedSQLExecutorWithDialect(
	ctx context.Context,
	datasource model.Datasource,
	driverName string,
	dsn string,
	dialect string,
	password string,
	readOnly bool,
	classifier limitedDialectErrorClassifier,
	explainer limitedDialectExplainer,
) (*limitedSQLExecutor, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open %s datasource: context is nil", dialect)
	}
	if (dialect == "dm" || dialect == "oracle") &&
		(datasource.TLSMode != "" || datasource.TLSServerName != "" || datasource.TLSCAFile != "" || datasource.TrustServerCertificate) {
		return nil, fmt.Errorf("open %s datasource: TLS options are not supported by the current driver", dialect)
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
		database:   database,
		dialect:    dialect,
		username:   datasource.Username,
		maxConns:   connectionLimit,
		timeout:    time.Duration(timeoutMS) * time.Millisecond,
		readOnly:   readOnly,
		classifier: classifier,
		explainer:  explainer,
		sessions:   make(map[string]*limitedSQLSession),
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
		return executor.databaseError(timedContext, DBStagePing, err)
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
		return nil, executor.databaseError(timedContext, DBStageAcquire, err)
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

func (executor *limitedSQLExecutor) Explain(ctx context.Context, sqlText string) (model.ExplainInfo, error) {
	if err := executor.validateReadOnlyQuery(ctx, sqlText, DBStageExplain); err != nil {
		return model.ExplainInfo{}, err
	}
	if executor.explainer == nil {
		return model.ExplainInfo{}, executor.unsupported(DBStageExplain)
	}
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	connection, err := executor.database.Conn(timedContext)
	if err != nil {
		return model.ExplainInfo{}, executor.databaseError(timedContext, DBStageAcquire, err)
	}
	defer connection.Close()
	return executor.explainer(timedContext, connection, sqlText)
}

func (executor *limitedSQLExecutor) Query(ctx context.Context, sqlText string, rowLimit int) (model.QueryResult, error) {
	if err := executor.validateReadOnlyQuery(ctx, sqlText, DBStageQuery); err != nil {
		return model.QueryResult{}, err
	}
	return executor.queryWithRunner(ctx, executor.database, sqlText, rowLimit)
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
		return executor.databaseError(timedContext, DBStageMetadata, err)
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

func (executor *limitedSQLExecutor) databaseError(ctx context.Context, stage DBStage, cause error) error {
	var resource *ResourceError
	if errors.As(cause, &resource) {
		return resource
	}
	if executor != nil && executor.classifier != nil {
		if classified, ok := executor.classifier(ctx, stage, cause); ok {
			return classified
		}
	}
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified
	}
	if isNetworkConnectionError(cause) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, "", ErrDatasourceUnreachable)
	}
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func (executor *limitedSQLExecutor) validateReadOnlyQuery(ctx context.Context, sqlText string, stage DBStage) error {
	if executor == nil || executor.database == nil || ctx == nil {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, "", ErrDatasourceUnreachable)
	}
	if err := validateLimitedSelectForDialect(executor.dialect, sqlText); err != nil {
		return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	return nil
}

func (executor *limitedSQLExecutor) queryWithRunner(
	ctx context.Context,
	runner limitedDialectRunner,
	sqlText string,
	rowLimit int,
) (model.QueryResult, error) {
	if rowLimit <= 0 {
		return model.QueryResult{}, fmt.Errorf("query %s datasource: row limit must be positive", executor.dialect)
	}
	started := time.Now()
	timedContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	rows, err := runner.QueryContext(timedContext, sqlText)
	if err != nil {
		return model.QueryResult{}, executor.databaseError(timedContext, DBStageQuery, err)
	}
	result, err := collectRows(&mysqlRowSource{rows: rows}, rowLimit)
	if err != nil {
		return model.QueryResult{}, executor.databaseError(timedContext, DBStageReadRows, err)
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

// validateLimitedSelect is intentionally narrower than either vendor grammar.
// It admits one plain SELECT without comments, bind markers, statement
// separators, function calls or write-capable clauses. Unsupported valid SQL
// fails closed until a vendor parser can prove its semantics.
func validateLimitedSelect(sqlText string) error {
	_, err := validateLimitedSelectWords(sqlText)
	return err
}

// validateLimitedSelectForDialect keeps the common fail-closed SELECT subset,
// then rejects vendor-incompatible pagination and sequence forms. Dialects not
// listed here retain the common validator's behavior.
func validateLimitedSelectForDialect(dialect, sqlText string) error {
	words, err := validateLimitedSelectWords(sqlText)
	if err != nil {
		return err
	}
	if dialect != "dm" && dialect != "oracle" && dialect != "sqlserver" {
		return nil
	}
	for _, word := range words[1:] {
		if word == "CURRVAL" {
			return fmt.Errorf("sequence pseudocolumns are unsupported")
		}
		if dialect == "oracle" && (word == "LIMIT" || word == "TOP") {
			return fmt.Errorf("unsupported Oracle pagination form")
		}
		if dialect == "sqlserver" {
			switch word {
			case "LIMIT", "CURRVAL", "OPENROWSET", "OPENDATASOURCE", "OPENQUERY", "BULK", "WAITFOR",
				"DBCC", "USE", "SET", "XP_CMDSHELL", "SP_OACREATE", "SP_OAMETHOD", "SP_OAGETPROPERTY":
				return fmt.Errorf("unsupported SQL Server SELECT form")
			}
		}
	}
	return nil
}

func validateLimitedSelectWords(sqlText string) ([]string, error) {
	if strings.TrimSpace(sqlText) == "" || !utf8.ValidString(sqlText) {
		return nil, fmt.Errorf("empty or invalid SQL")
	}
	words := make([]string, 0, 16)
	for index := 0; index < len(sqlText); {
		character := sqlText[index]
		switch {
		case character == '\'' || character == '"':
			quote := character
			index++
			closed := false
			for index < len(sqlText) {
				if sqlText[index] != quote {
					_, size := utf8.DecodeRuneInString(sqlText[index:])
					index += size
					continue
				}
				if index+1 < len(sqlText) && sqlText[index+1] == quote {
					index += 2
					continue
				}
				index++
				closed = true
				break
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted value")
			}
		case character == ';' || character == '?' || character == ':' || character == '$':
			return nil, fmt.Errorf("unsupported statement separator or bind marker")
		case character == '(' || character == ')':
			return nil, fmt.Errorf("function calls and subqueries are unsupported")
		case character == '-' && index+1 < len(sqlText) && sqlText[index+1] == '-':
			return nil, fmt.Errorf("comments are unsupported")
		case character == '/' && index+1 < len(sqlText) && sqlText[index+1] == '*':
			return nil, fmt.Errorf("comments are unsupported")
		default:
			runeValue, size := utf8.DecodeRuneInString(sqlText[index:])
			if unicode.IsControl(runeValue) && !unicode.IsSpace(runeValue) {
				return nil, fmt.Errorf("control character is unsupported")
			}
			if unicode.IsLetter(runeValue) || runeValue == '_' {
				start := index
				index += size
				for index < len(sqlText) {
					next, nextSize := utf8.DecodeRuneInString(sqlText[index:])
					if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_' && next != '$' && next != '#' {
						break
					}
					index += nextSize
				}
				words = append(words, strings.ToUpper(sqlText[start:index]))
				continue
			}
			index += size
		}
	}
	if len(words) == 0 || words[0] != "SELECT" {
		return nil, fmt.Errorf("only SELECT is supported")
	}
	for _, word := range words[1:] {
		switch word {
		case "INSERT", "UPDATE", "DELETE", "MERGE", "INTO", "CALL", "EXEC", "EXECUTE",
			"BEGIN", "DECLARE", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE",
			"COMMIT", "ROLLBACK", "SAVEPOINT", "LOCK", "NEXTVAL":
			return nil, fmt.Errorf("write-capable SELECT form is unsupported")
		}
	}
	return words, nil
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

func (session *limitedSQLSession) Query(ctx context.Context, sqlText string, rowLimit int) (model.QueryResult, error) {
	if session == nil {
		return model.QueryResult{}, fmt.Errorf("query dialect session: %w", ErrSessionClosed)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.executor == nil || session.connection == nil {
		return model.QueryResult{}, fmt.Errorf("query dialect session: %w", ErrSessionClosed)
	}
	if err := session.executor.validateReadOnlyQuery(ctx, sqlText, DBStageQuery); err != nil {
		return model.QueryResult{}, err
	}
	return session.executor.queryWithRunner(ctx, session.connection, sqlText, rowLimit)
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

func (session *limitedSQLSession) Explain(ctx context.Context, sqlText string) (model.ExplainInfo, error) {
	if session == nil {
		return model.ExplainInfo{}, fmt.Errorf("explain dialect session: %w", ErrSessionClosed)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.executor == nil || session.connection == nil {
		return model.ExplainInfo{}, fmt.Errorf("explain dialect session: %w", ErrSessionClosed)
	}
	if err := session.executor.validateReadOnlyQuery(ctx, sqlText, DBStageExplain); err != nil {
		return model.ExplainInfo{}, err
	}
	if session.executor.explainer == nil {
		return model.ExplainInfo{}, session.executor.unsupported(DBStageExplain)
	}
	timedContext, cancel := session.executor.timeoutContext(ctx)
	defer cancel()
	return session.executor.explainer(timedContext, session.connection, sqlText)
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
