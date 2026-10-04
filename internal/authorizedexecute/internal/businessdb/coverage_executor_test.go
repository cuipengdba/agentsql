package businessdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestCoverageExecutorManagerAndValidation(t *testing.T) {
	manager := NewManager(true)
	require.NotNil(t, manager)
	require.True(t, manager.readOnly)
	require.NotNil(t, manager.executors)
	require.NotNil(t, manager.opener)

	var nilManager *Manager
	require.ErrorContains(t, nilManager.Close("x"), "manager is nil")
	require.ErrorContains(t, nilManager.CloseAll(), "manager is nil")
	require.NoError(t, manager.Close("missing"))
	require.NoError(t, manager.CloseAll())

	firstErr := errors.New("first close failure")
	secondErr := errors.New("second close failure")
	var typedNil *closeErrorExecutor
	manager.executors = map[string]Executor{
		"first":  &closeErrorExecutor{err: firstErr},
		"second": &closeErrorExecutor{err: secondErr},
		"nil":    typedNil,
	}
	err := manager.CloseAll()
	require.Error(t, err)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.ErrorContains(t, err, `datasource "first"`)
	require.ErrorContains(t, err, `datasource "second"`)
	require.Empty(t, manager.executors)

	single := &closeErrorExecutor{err: firstErr}
	manager.executors["single"] = single
	err = manager.Close("single")
	require.ErrorIs(t, err, firstErr)
	require.Equal(t, 1, single.closed)
	require.NotContains(t, manager.executors, "single")

	for _, test := range []struct {
		name    string
		value   int
		want    int
		wantErr string
	}{
		{name: "default connection limit", value: 0, want: defaultConnectionLimit},
		{name: "explicit connection limit", value: 17, want: 17},
		{name: "negative connection limit", value: -1, wantErr: "cannot be negative"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, normalizeErr := normalizedConnectionLimit(test.value)
			if test.wantErr != "" {
				require.ErrorContains(t, normalizeErr, test.wantErr)
				require.Zero(t, got)
				return
			}
			require.NoError(t, normalizeErr)
			require.Equal(t, test.want, got)
		})
	}

	for _, test := range []struct {
		name    string
		value   int
		want    int
		wantErr string
	}{
		{name: "default timeout", value: 0, want: defaultStatementTimeout},
		{name: "explicit timeout", value: 23, want: 23},
		{name: "negative timeout", value: -1, wantErr: "cannot be negative"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, normalizeErr := normalizedStatementTimeout(test.value)
			if test.wantErr != "" {
				require.ErrorContains(t, normalizeErr, test.wantErr)
				require.Zero(t, got)
				return
			}
			require.NoError(t, normalizeErr)
			require.Equal(t, test.want, got)
		})
	}

	valid := model.Datasource{ID: "ds", Host: "localhost", Port: 5432, Database: "app", Username: "agent"}
	require.NoError(t, validateDatasource(valid))
	for _, mutate := range []func(*model.Datasource){
		func(value *model.Datasource) { value.ID = " " },
		func(value *model.Datasource) { value.Host = "" },
		func(value *model.Datasource) { value.Database = "\t" },
		func(value *model.Datasource) { value.Username = "" },
		func(value *model.Datasource) { value.Port = 0 },
		func(value *model.Datasource) { value.Port = 65_536 },
	} {
		invalid := valid
		mutate(&invalid)
		require.Error(t, validateDatasource(invalid), "%+v", invalid)
	}
}

func TestCoverageExecutorOpenersRejectInvalidConfiguration(t *testing.T) {
	_, err := openExecutor(model.Datasource{DBType: "sqlite"}, "secret", false)
	require.ErrorContains(t, err, `unsupported datasource type "sqlite"`)

	for _, dialect := range []string{"postgres", "mysql", "dm", "oracle"} {
		t.Run(dialect, func(t *testing.T) {
			_, openErr := openExecutor(model.Datasource{DBType: dialect}, "secret", false)
			require.Error(t, openErr)
			require.Contains(t, openErr.Error(), "datasource")
		})
	}

	validMySQL := model.Datasource{
		ID: "mysql", DBType: "mysql", Host: "127.0.0.1", Port: 1,
		Database: "app", Username: "agent", ConnLimit: 1, StmtTimeoutMS: 1,
	}
	_, err = NewMySQLExecutor(nil, validMySQL, "password=visible", false)
	require.ErrorContains(t, err, "context is nil")
	invalid := validMySQL
	invalid.ConnLimit = -1
	_, err = NewMySQLExecutor(context.Background(), invalid, "password=visible", false)
	require.ErrorContains(t, err, "connection limit")
	invalid = validMySQL
	invalid.StmtTimeoutMS = -1
	_, err = NewMySQLExecutor(context.Background(), invalid, "password=visible", false)
	require.ErrorContains(t, err, "statement timeout")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewMySQLExecutor(canceled, validMySQL, "password=visible", false)
	require.ErrorIs(t, err, ErrDatasourceUnreachable)
	require.NotContains(t, err.Error(), "password=visible")

	validPostgres := validMySQL
	validPostgres.ID = "postgres"
	validPostgres.DBType = "postgres"
	_, err = NewPostgresExecutor(nil, validPostgres, "password=visible", false)
	require.ErrorContains(t, err, "context is nil")
	invalid = validPostgres
	invalid.ConnLimit = -1
	_, err = NewPostgresExecutor(context.Background(), invalid, "password=visible", false)
	require.ErrorContains(t, err, "invalid connection limit")
	invalid = validPostgres
	invalid.ConnLimit = math.MaxInt32 + 1
	_, err = NewPostgresExecutor(context.Background(), invalid, "password=visible", false)
	require.ErrorContains(t, err, "invalid connection limit")
	invalid = validPostgres
	invalid.StmtTimeoutMS = -1
	_, err = NewPostgresExecutor(context.Background(), invalid, "password=visible", false)
	require.ErrorContains(t, err, "statement timeout")
	canceled, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = NewPostgresExecutor(canceled, validPostgres, "password=visible", true)
	require.ErrorIs(t, err, ErrDatasourceUnreachable)
	require.NotContains(t, err.Error(), "password=visible")
}

func TestCoverageExecutorRedactionRowsAndValues(t *testing.T) {
	require.Nil(t, sanitizedCause(nil))
	cause := errors.New("postgres://agent:super-secret@db.internal/app?password=super-secret")
	sanitized := sanitizedCause(cause)
	require.NotNil(t, sanitized)
	require.NotContains(t, sanitized.Error(), "super-secret")
	require.NotContains(t, sanitized.Error(), "db.internal")
	require.Contains(t, sanitized.Error(), "errorString")

	closeErr := errors.New("rows close failed")
	readErr := errors.New("read failed")
	source := &coverageRowSource{closeErr: closeErr}
	err := closeRowSource(source, readErr)
	require.ErrorIs(t, err, readErr)
	require.ErrorIs(t, err, closeErr)
	require.True(t, source.closed)
	source = &coverageRowSource{}
	err = closeRowSource(source, readErr)
	require.Same(t, readErr, err)
	require.True(t, source.closed)

	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil", value: nil, want: ""},
		{name: "string", value: "value", want: "value"},
		{name: "bytes", value: []byte("bytes"), want: "bytes"},
		{name: "integer", value: int64(42), want: "42"},
		{name: "decimal", value: 3.5, want: "3.5"},
		{name: "boolean", value: true, want: "true"},
		{name: "time", value: time.Date(2026, 9, 16, 10, 11, 12, 13, time.UTC), want: "2026-09-16T10:11:12.000000013Z"},
		{name: "unknown", value: struct{ N int }{N: 7}, want: "{7}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, stringifyDatabaseValue(test.value))
		})
	}

	for _, test := range []struct {
		name    string
		value   any
		want    int64
		wantErr bool
	}{
		{name: "int", value: int(1), want: 1},
		{name: "int8", value: int8(2), want: 2},
		{name: "int16", value: int16(3), want: 3},
		{name: "int32", value: int32(4), want: 4},
		{name: "int64", value: int64(5), want: 5},
		{name: "uint", value: uint(6), want: 6},
		{name: "uint8", value: uint8(7), want: 7},
		{name: "uint16", value: uint16(8), want: 8},
		{name: "uint32", value: uint32(9), want: 9},
		{name: "uint64", value: uint64(10), want: 10},
		{name: "bytes", value: []byte("11"), want: 11},
		{name: "string", value: "12", want: 12},
		{name: "nil", value: nil, wantErr: true},
		{name: "empty", value: "", wantErr: true},
		{name: "decimal", value: "1.5", wantErr: true},
		{name: "scientific", value: "1e3", wantErr: true},
		{name: "overflow string", value: "9223372036854775808", wantErr: true},
		{name: "overflow uint", value: uint64(math.MaxInt64) + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, integerErr := mysqlExplainInteger(test.value)
			if test.wantErr {
				require.Error(t, integerErr)
				return
			}
			require.NoError(t, integerErr)
			require.Equal(t, test.want, got)
		})
	}
}

func TestCoverageExecutorDialectPoolTimeoutAndValidation(t *testing.T) {
	require.Equal(t, "mysql", (&MySQLExecutor{}).Dialect())
	require.Equal(t, "postgres", (&PostgresExecutor{}).Dialect())
	require.Equal(t, PoolStat{}, (*MySQLExecutor)(nil).poolSnapshot())
	require.Equal(t, PoolStat{}, (*PostgresExecutor)(nil).poolSnapshot())
	require.Equal(t, PoolStat{MaxOpen: 3}, (&MySQLExecutor{maxConns: 3}).poolSnapshot())
	require.Equal(t, PoolStat{MaxOpen: 4}, (&PostgresExecutor{maxConns: 4}).poolSnapshot())

	for _, timeout := range []time.Duration{0, 5 * time.Millisecond} {
		mysqlExecutor := &MySQLExecutor{timeout: timeout}
		ctx, cancel := mysqlExecutor.timeoutContext(context.Background())
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.True(t, deadline.After(time.Now()))
		cancel()

		postgresExecutor := &PostgresExecutor{timeout: timeout}
		ctx, cancel = postgresExecutor.timeoutContext(context.Background())
		deadline, ok = ctx.Deadline()
		require.True(t, ok)
		require.True(t, deadline.After(time.Now()))
		cancel()
	}

	require.ErrorContains(t, (*MySQLExecutor)(nil).validateOperation(context.Background(), "SELECT 1"), "closed")
	require.ErrorContains(t, (&MySQLExecutor{}).validateOperation(context.Background(), "SELECT 1"), "closed")
	mysqlExecutor := &MySQLExecutor{database: &sql.DB{}}
	require.ErrorContains(t, mysqlExecutor.validateOperation(nil, "SELECT 1"), "context is nil")
	require.ErrorContains(t, mysqlExecutor.validateOperation(context.Background(), " \t"), "SQL is empty")
	require.NoError(t, mysqlExecutor.validateOperation(context.Background(), "SELECT 1"))

	require.ErrorContains(t, (*PostgresExecutor)(nil).validateOperation(context.Background(), "SELECT 1"), "closed")
	postgresExecutor := &PostgresExecutor{pool: newZeroPostgresPool()}
	require.ErrorContains(t, postgresExecutor.validateOperation(nil, "SELECT 1"), "context is nil")
	require.ErrorContains(t, postgresExecutor.validateOperation(context.Background(), " \t"), "SQL is empty")
	require.NoError(t, postgresExecutor.validateOperation(context.Background(), "SELECT 1"))

	for _, test := range []struct {
		schema string
		table  string
		valid  bool
	}{
		{schema: "", table: "orders", valid: true},
		{schema: "public", table: "orders", valid: true},
		{schema: " public", table: "orders"},
		{schema: "public ", table: "orders"},
		{schema: "public", table: ""},
		{schema: "public", table: " orders"},
	} {
		err := validateMetadataObject(test.schema, test.table)
		if test.valid {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "invalid schema or table name")
		}
	}
	require.ErrorContains(t, (*MySQLExecutor)(nil).validateMetadataOperation("", "orders"), "closed")
	require.ErrorContains(t, mysqlExecutor.validateMetadataOperation(" bad", "orders"), "read MySQL metadata")
	require.NoError(t, mysqlExecutor.validateMetadataOperation("", "orders"))
}

func TestCoverageExecutorTransactionStateEdges(t *testing.T) {
	now := (wallClock{}).Now()
	require.WithinDuration(t, time.Now(), now, time.Second)
	machine := newTransactionStateMachine(nil)
	require.NotNil(t, machine.clock)
	require.False(t, machine.active())
	machine.touchStatementEnd()
	_, _, _, _, err := machine.snapshot()
	require.NoError(t, err)

	clock := newFakeSessionClock()
	machine = newTransactionStateMachine(clock)
	begin := statementClass{transaction: transactionBegin}
	machine.finishStatement(begin, clock.Now(), 0, true)
	clock.Advance(time.Second)
	machine.touchStatementEnd()
	clock.Advance(250 * time.Millisecond)
	inTransaction, ageMS, idleMS, _, err := machine.snapshot()
	require.NoError(t, err)
	require.True(t, inTransaction)
	require.Equal(t, int64(1250), ageMS)
	require.Equal(t, int64(250), idleMS)

	for _, test := range []struct {
		name  string
		text  string
		start int
		quote byte
		want  int
	}{
		{name: "single quote", text: "'value' tail", quote: '\'', want: 7},
		{name: "backslash escape", text: "'a\\'b' tail", quote: '\'', want: 6},
		{name: "doubled quote", text: "'a''b' tail", quote: '\'', want: 6},
		{name: "double quote", text: `"a""b" tail`, quote: '"', want: 6},
		{name: "backtick", text: "`a``b` tail", quote: '`', want: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, quoteErr := skipSQLQuoted(test.text, test.start, test.quote)
			require.NoError(t, quoteErr)
			require.Equal(t, test.want, next)
		})
	}
	_, err = skipSQLQuoted("'unterminated\\", 0, '\'')
	require.ErrorContains(t, err, "unterminated")
}

func TestCoverageMySQLExecutorWithStandardLibraryDriver(t *testing.T) {
	database := openCoverageSQLDatabase(t, "normal")
	executor := &MySQLExecutor{
		database: database,
		maxConns: 2,
		timeout:  50 * time.Millisecond,
		clock:    newFakeSessionClock(),
		sessions: make(map[string]Session),
	}

	require.NoError(t, executor.Ping(context.Background()))
	pool := executor.poolSnapshot()
	require.Equal(t, 2, pool.MaxOpen)
	require.GreaterOrEqual(t, pool.Idle, 1)

	result, err := executor.Query(context.Background(), "SELECT value FROM t", 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.Equal(t, []string{"id", "value"}, result.Columns)
	require.Equal(t, []string{"1", "alpha"}, result.Rows[0])
	require.True(t, result.Truncated)

	result, err = executor.Execute(context.Background(), "UPDATE t SET value = 'x'")
	require.NoError(t, err)
	require.Equal(t, 2, result.RowCount)

	plan, err := executor.Explain(context.Background(), "SELECT * FROM t")
	require.NoError(t, err)
	require.True(t, plan.SeqScan)
	require.False(t, plan.UsesIndex)
	require.Equal(t, int64(7), plan.EstScanRows)
	require.Contains(t, plan.Raw, "type\tkey\trows")

	hasIndex, err := executor.TableHasIndex("app", "t")
	require.NoError(t, err)
	require.True(t, hasIndex)
	rowCount, err := executor.TableRowCount("app", "t")
	require.NoError(t, err)
	require.Equal(t, int64(12), rowCount)
	state, ok := executor.snapshotServerTransaction()
	require.True(t, ok)
	require.Equal(t, rules.MysqlTransactionState{InTransaction: true, AgeMS: 4, AffectedRows: 3}, state)
	unbound, err := executor.MysqlTransactionState()
	require.NoError(t, err)
	require.Equal(t, rules.MysqlTransactionState{}, unbound)

	for _, id := range []string{"", " leading", "trailing "} {
		_, err = executor.OpenSession(context.Background(), id)
		require.ErrorContains(t, err, "invalid session ID")
	}
	_, err = executor.OpenSession(nil, "session")
	require.ErrorContains(t, err, "unavailable")
	session, err := executor.OpenSession(context.Background(), "session")
	require.NoError(t, err)
	_, err = executor.OpenSession(context.Background(), "session")
	require.ErrorIs(t, err, ErrSessionExists)

	_, err = session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)
	write, err := session.Execute(context.Background(), "UPDATE t SET value = 'session'")
	require.NoError(t, err)
	require.Equal(t, 2, write.RowCount)
	query, err := session.Query(context.Background(), "SELECT value FROM t", 1)
	require.NoError(t, err)
	require.Equal(t, 1, query.RowCount)
	sessionPlan, err := session.Explain(context.Background(), "SELECT * FROM t")
	require.NoError(t, err)
	require.Equal(t, int64(7), sessionPlan.EstScanRows)
	transaction, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, transaction.InTransaction)
	require.Equal(t, int64(2), transaction.AffectedRows)
	_, err = session.TransactionState()
	require.ErrorContains(t, err, "unavailable for MySQL")
	require.NoError(t, session.Close())
	require.NoError(t, session.Close())
	_, err = session.Query(context.Background(), "SELECT 1", 1)
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.Execute(context.Background(), "UPDATE t SET value = 'x'")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.Explain(context.Background(), "SELECT 1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.MysqlTransactionState()
	require.ErrorIs(t, err, ErrSessionClosed)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.Query(canceled, "SELECT 1", 1)
	require.ErrorIs(t, err, ErrQueryTimeout)
	require.NoError(t, executor.Close())
	require.NoError(t, executor.Close())
	_, err = executor.OpenSession(context.Background(), "after-close")
	require.ErrorIs(t, err, ErrSessionClosed)
}

func TestCoverageMySQLErrorAndResourceBranches(t *testing.T) {
	database := openCoverageSQLDatabase(t, "normal")
	executor := &MySQLExecutor{database: database, timeout: time.Second, sessions: make(map[string]Session)}

	_, err := executor.Query(context.Background(), "SELECT 1", 0)
	require.ErrorContains(t, err, "row limit must be positive")
	_, err = executor.Query(nil, "SELECT 1", 1)
	require.ErrorContains(t, err, "context is nil")
	_, err = executor.Query(context.Background(), " ", 1)
	require.ErrorContains(t, err, "SQL is empty")
	_, err = executor.Explain(context.Background(), "FAIL")
	require.ErrorContains(t, err, "database operation failed")
	require.NotContains(t, err.Error(), "driver failure secret")
	_, err = executor.Execute(context.Background(), "FAIL")
	require.ErrorContains(t, err, "database operation failed")

	rows, err := database.QueryContext(context.Background(), "SELECT CLOSE_ERROR")
	require.NoError(t, err)
	cause := errors.New("scan failed")
	err = closeSQLRowsAfterError(rows, cause)
	require.ErrorIs(t, err, cause)
	require.ErrorContains(t, err, "driver rows close failed")
	rows, err = database.QueryContext(context.Background(), "SELECT 1")
	require.NoError(t, err)
	err = closeSQLRowsAfterError(rows, cause)
	require.Same(t, cause, err)

	negativeDB := openCoverageSQLDatabase(t, "negative")
	negative := &MySQLExecutor{database: negativeDB, timeout: time.Second}
	_, err = negative.TableRowCount("app", "t")
	require.ErrorContains(t, err, "negative estimate")
	_, ok := negative.snapshotServerTransaction()
	require.False(t, ok)
	require.NoError(t, negative.Close())

	require.NoError(t, executor.Close())
	require.NoError(t, (*MySQLExecutor)(nil).Close())
	require.NoError(t, (&MySQLExecutor{}).Close())
	require.ErrorIs(t, (*MySQLExecutor)(nil).Ping(context.Background()), ErrDatasourceUnreachable)
	require.ErrorIs(t, (&MySQLExecutor{database: database}).Ping(nil), ErrDatasourceUnreachable)
	_, ok = (*MySQLExecutor)(nil).snapshotServerTransaction()
	require.False(t, ok)

	var nilSession *mysqlSession
	_, err = nilSession.Query(context.Background(), "SELECT 1", 1)
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.Execute(context.Background(), "UPDATE t SET a=1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.Explain(context.Background(), "SELECT 1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.MysqlTransactionState()
	require.ErrorIs(t, err, ErrSessionClosed)
	require.NoError(t, nilSession.Close())
}

func TestPolarDBXExplainUsesPhysicalExecutePlan(t *testing.T) {
	database := openCoverageSQLDatabase(t, "polardbx")
	executor := &MySQLExecutor{database: database, timeout: time.Second, sessions: make(map[string]Session)}

	plan, err := executor.Explain(context.Background(), "SELECT id FROM orders WHERE id = 7")
	require.NoError(t, err)
	require.Equal(t, int64(1), plan.EstScanRows)
	require.True(t, plan.UsesIndex)
	require.False(t, plan.SeqScan)

	invalidDatabase := openCoverageSQLDatabase(t, "polardbx-invalid")
	invalid := &MySQLExecutor{database: invalidDatabase, timeout: time.Second, sessions: make(map[string]Session)}
	_, err = invalid.Explain(context.Background(), "SELECT id FROM orders")
	require.ErrorContains(t, err, "unsupported MySQL-compatible EXPLAIN format")
}

func TestCoveragePostgresRunnerAndSession(t *testing.T) {
	clock := newFakeSessionClock()
	runner := &coveragePostgresRunner{
		rows: [][]any{{int64(1), "alpha"}, {int64(2), []byte("beta")}},
		plan: []byte(`[{"Plan":{"Node Type":"Index Scan","Plan Rows":3,"Total Cost":1.5,"Index Name":"idx_t"}}]`),
	}
	executor := &PostgresExecutor{timeout: time.Second, clock: clock, sessions: make(map[string]Session)}

	result, err := executor.queryWithRunner(context.Background(), runner, "SELECT value FROM t", 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.True(t, result.Truncated)
	write, err := executor.executeWithRunner(context.Background(), runner, "UPDATE t SET value='x'")
	require.NoError(t, err)
	require.Equal(t, 2, write.RowCount)
	plan, err := executor.explainWithRunner(context.Background(), runner, "SELECT * FROM t")
	require.NoError(t, err)
	require.True(t, plan.UsesIndex)
	require.Equal(t, int64(3), plan.EstScanRows)

	session := &postgresSession{
		id:       "postgres-session",
		executor: executor,
		runner:   runner,
		state:    newTransactionStateMachine(clock),
	}
	executor.sessions[session.id] = session
	released := 0
	session.release = func() { released++ }
	session.discard = func() error { return errors.New("discard failure") }

	_, err = session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)
	clock.Advance(time.Second)
	query, err := session.Query(context.Background(), "SELECT value FROM t", 1)
	require.NoError(t, err)
	require.Equal(t, 1, query.RowCount)
	clock.Advance(250 * time.Millisecond)
	plan, err = session.Explain(context.Background(), "SELECT * FROM t")
	require.NoError(t, err)
	require.True(t, plan.UsesIndex)
	state, err := session.TransactionState()
	require.NoError(t, err)
	require.True(t, state.InTransaction)
	require.Equal(t, int64(1250), state.AgeMS)
	require.Zero(t, state.IdleMS)
	_, err = session.MysqlTransactionState()
	require.ErrorContains(t, err, "unavailable for PostgreSQL")
	_, err = session.TableHasIndex("public", "t")
	require.ErrorContains(t, err, "executor is not initialized")
	_, err = session.TableRowCount("public", "t")
	require.ErrorContains(t, err, "executor is not initialized")

	require.NoError(t, session.Close())
	require.Equal(t, 1, released)
	require.Contains(t, runner.statements, "ROLLBACK")
	require.NoError(t, session.Close())
	_, err = session.Query(context.Background(), "SELECT 1", 1)
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.Execute(context.Background(), "UPDATE t SET a=1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.Explain(context.Background(), "SELECT 1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.TransactionState()
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.TableHasIndex("public", "t")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = session.TableRowCount("public", "t")
	require.ErrorIs(t, err, ErrSessionClosed)
}

func TestCoveragePostgresErrorBranches(t *testing.T) {
	runner := &coveragePostgresRunner{queryErr: context.Canceled, execErr: context.DeadlineExceeded, rowErr: errors.New("plan secret")}
	executor := &PostgresExecutor{timeout: time.Second}
	_, err := executor.queryWithRunner(context.Background(), runner, "SELECT 1", 1)
	require.ErrorIs(t, err, ErrQueryTimeout)
	_, err = executor.executeWithRunner(context.Background(), runner, "UPDATE t SET a=1")
	require.ErrorIs(t, err, ErrQueryTimeout)
	_, err = executor.explainWithRunner(context.Background(), runner, "SELECT 1")
	require.ErrorContains(t, err, "database operation failed")
	require.NotContains(t, err.Error(), "plan secret")

	readOnly := &postgresSession{
		executor: &PostgresExecutor{readOnly: true},
		runner:   &coveragePostgresRunner{},
		state:    newTransactionStateMachine(newFakeSessionClock()),
	}
	_, err = readOnly.Execute(context.Background(), "UPDATE t SET a=1")
	require.ErrorIs(t, err, ErrReadOnlyViolated)
	_, err = readOnly.Query(context.Background(), "SELECT 1", 0)
	require.ErrorContains(t, err, "row limit must be positive")
	_, err = readOnly.Query(nil, "SELECT 1", 1)
	require.ErrorContains(t, err, "context is nil")
	readWrite := &postgresSession{
		executor: &PostgresExecutor{},
		runner:   &coveragePostgresRunner{},
		state:    newTransactionStateMachine(newFakeSessionClock()),
	}
	_, err = readWrite.Execute(context.Background(), "/* unterminated")
	require.ErrorContains(t, err, "unterminated SQL block comment")

	var nilSession *postgresSession
	_, err = nilSession.Query(context.Background(), "SELECT 1", 1)
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.Execute(context.Background(), "UPDATE t SET a=1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.Explain(context.Background(), "SELECT 1")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.TransactionState()
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.TableHasIndex("public", "t")
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = nilSession.TableRowCount("public", "t")
	require.ErrorIs(t, err, ErrSessionClosed)
	require.NoError(t, nilSession.Close())

	require.NoError(t, (*PostgresExecutor)(nil).Close())
	require.NoError(t, (&PostgresExecutor{}).Close())
	require.ErrorIs(t, (*PostgresExecutor)(nil).Ping(context.Background()), ErrDatasourceUnreachable)
	_, ok := (*PostgresExecutor)(nil).snapshotServerTransaction()
	require.False(t, ok)
	unbound, err := (&PostgresExecutor{}).TransactionState()
	require.NoError(t, err)
	require.Equal(t, rules.TransactionState{}, unbound)
}

type closeErrorExecutor struct {
	err    error
	closed int
}

func (*closeErrorExecutor) Dialect() string                                      { return "test" }
func (*closeErrorExecutor) Ping(context.Context) error                           { return nil }
func (*closeErrorExecutor) OpenSession(context.Context, string) (Session, error) { return nil, nil }
func (*closeErrorExecutor) BeginWriteTx(context.Context) (WriteTx, error)        { return nil, nil }
func (*closeErrorExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{}, nil
}
func (*closeErrorExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{}, nil
}
func (*closeErrorExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, nil
}
func (executor *closeErrorExecutor) Close() error {
	executor.closed++
	return executor.err
}

type coverageRowSource struct {
	closed   bool
	closeErr error
}

func (*coverageRowSource) Columns() ([]string, error) { return []string{"value"}, nil }
func (*coverageRowSource) Next() bool                 { return false }
func (*coverageRowSource) Values() ([]any, error)     { return nil, nil }
func (*coverageRowSource) Err() error                 { return nil }
func (source *coverageRowSource) Close() error {
	source.closed = true
	return source.closeErr
}

const coverageSQLDriverName = "agentsql_executor_coverage"

var registerCoverageSQLDriver sync.Once

func openCoverageSQLDatabase(t *testing.T, mode string) *sql.DB {
	t.Helper()
	registerCoverageSQLDriver.Do(func() {
		sql.Register(coverageSQLDriverName, &coverageSQLDriver{})
	})
	database, err := sql.Open(coverageSQLDriverName, mode)
	require.NoError(t, err)
	database.SetMaxOpenConns(2)
	database.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

type coverageSQLDriver struct{}

func (*coverageSQLDriver) Open(name string) (driver.Conn, error) {
	return &coverageSQLConn{mode: name}, nil
}

type coverageSQLConn struct {
	mode string
}

func (*coverageSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}

func (*coverageSQLConn) Close() error { return nil }

func (*coverageSQLConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are represented by statements in this test driver")
}

func (*coverageSQLConn) Ping(ctx context.Context) error { return ctx.Err() }

func (*coverageSQLConn) ExecContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.Contains(query, "FAIL") {
		return nil, errors.New("driver failure secret")
	}
	return driver.RowsAffected(2), nil
}

func (connection *coverageSQLConn) QueryContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.Contains(query, "FAIL") {
		return nil, errors.New("driver failure secret")
	}
	rows := &coverageDriverRows{}
	switch {
	case strings.Contains(query, "CLOSE_ERROR"):
		rows.columns = []string{"value"}
		rows.values = [][]driver.Value{{int64(1)}}
		rows.closeErr = errors.New("driver rows close failed")
	case connection.mode == "polardbx-invalid" && strings.HasPrefix(strings.TrimSpace(query), "EXPLAIN EXECUTE"):
		rows.columns = []string{"unknown_plan"}
		rows.values = [][]driver.Value{{"unverified"}}
	case connection.mode == "polardbx" && strings.HasPrefix(strings.TrimSpace(query), "EXPLAIN EXECUTE"):
		rows.columns = []string{"id", "select_type", "table", "type", "possible_keys", "key", "rows"}
		rows.values = [][]driver.Value{{int64(1), "SIMPLE", "orders_0000", "const", "PRIMARY", "PRIMARY", "1"}}
	case strings.HasPrefix(connection.mode, "polardbx") && strings.HasPrefix(strings.TrimSpace(query), "EXPLAIN"):
		rows.columns = []string{"LOGICAL EXECUTIONPLAN"}
		rows.values = [][]driver.Value{{`LogicalView(tables="agentsql.orders_0000", shardCount=1)`}}
	case strings.HasPrefix(strings.TrimSpace(query), "EXPLAIN"):
		rows.columns = []string{"type", "key", "rows"}
		rows.values = [][]driver.Value{{"ALL", nil, "7"}}
	case strings.Contains(query, "information_schema.statistics"):
		rows.columns = []string{"count"}
		rows.values = [][]driver.Value{{int64(1)}}
	case strings.Contains(query, "information_schema.tables"):
		rows.columns = []string{"rows"}
		value := int64(12)
		if connection.mode == "negative" {
			value = -1
		}
		rows.values = [][]driver.Value{{value}}
	case strings.Contains(query, "@@session.autocommit"):
		rows.columns = []string{"in_transaction", "age_ms", "affected_rows"}
		if connection.mode == "negative" {
			rows.values = [][]driver.Value{{true, int64(-1), int64(3)}}
		} else {
			rows.values = [][]driver.Value{{true, int64(4), int64(3)}}
		}
	default:
		rows.columns = []string{"id", "value"}
		rows.values = [][]driver.Value{{int64(1), []byte("alpha")}, {int64(2), "beta"}}
	}
	return rows, nil
}

type coverageDriverRows struct {
	columns  []string
	values   [][]driver.Value
	index    int
	closeErr error
}

func (rows *coverageDriverRows) Columns() []string { return rows.columns }
func (rows *coverageDriverRows) Close() error      { return rows.closeErr }
func (rows *coverageDriverRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

type coveragePostgresRunner struct {
	rows       [][]any
	plan       []byte
	queryErr   error
	execErr    error
	rowErr     error
	statements []string
}

func (runner *coveragePostgresRunner) Exec(
	ctx context.Context,
	sqlText string,
	_ ...any,
) (pgconn.CommandTag, error) {
	runner.statements = append(runner.statements, sqlText)
	if err := ctx.Err(); err != nil {
		return pgconn.CommandTag{}, err
	}
	if runner.execErr != nil {
		return pgconn.CommandTag{}, runner.execErr
	}
	if strings.EqualFold(strings.TrimSpace(sqlText), "BEGIN") ||
		strings.EqualFold(strings.TrimSpace(sqlText), "ROLLBACK") {
		return pgconn.NewCommandTag(strings.ToUpper(strings.TrimSpace(sqlText))), nil
	}
	return pgconn.NewCommandTag("UPDATE 2"), nil
}

func (runner *coveragePostgresRunner) Query(
	ctx context.Context,
	_ string,
	_ ...any,
) (pgx.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runner.queryErr != nil {
		return nil, runner.queryErr
	}
	return &coveragePGXRows{
		fields: []pgconn.FieldDescription{{Name: "id"}, {Name: "value"}},
		rows:   runner.rows,
		index:  -1,
	}, nil
}

func (runner *coveragePostgresRunner) QueryRow(
	ctx context.Context,
	_ string,
	_ ...any,
) pgx.Row {
	if err := ctx.Err(); err != nil {
		return coveragePGXRow{err: err}
	}
	return coveragePGXRow{raw: runner.plan, err: runner.rowErr}
}

type coveragePGXRows struct {
	fields []pgconn.FieldDescription
	rows   [][]any
	index  int
	err    error
	closed bool
}

func (rows *coveragePGXRows) Close()                                       { rows.closed = true }
func (rows *coveragePGXRows) Err() error                                   { return rows.err }
func (*coveragePGXRows) CommandTag() pgconn.CommandTag                     { return pgconn.CommandTag{} }
func (rows *coveragePGXRows) FieldDescriptions() []pgconn.FieldDescription { return rows.fields }
func (rows *coveragePGXRows) Next() bool {
	rows.index++
	if rows.index >= len(rows.rows) {
		rows.closed = true
		return false
	}
	return true
}
func (*coveragePGXRows) Scan(...any) error { return errors.New("scan is unused") }
func (rows *coveragePGXRows) Values() ([]any, error) {
	if rows.index < 0 || rows.index >= len(rows.rows) {
		return nil, errors.New("no current row")
	}
	return rows.rows[rows.index], nil
}
func (*coveragePGXRows) RawValues() [][]byte { return nil }
func (*coveragePGXRows) Conn() *pgx.Conn     { return nil }

type coveragePGXRow struct {
	raw []byte
	err error
}

func (row coveragePGXRow) Scan(destination ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destination) != 1 {
		return fmt.Errorf("expected one destination, got %d", len(destination))
	}
	target, ok := destination[0].(*[]byte)
	if !ok {
		return fmt.Errorf("unsupported destination %T", destination[0])
	}
	*target = append((*target)[:0], row.raw...)
	return nil
}

// newZeroPostgresPool is only used for argument validation paths that never
// dereference the pool. Keeping this helper explicit makes that test contract clear.
func newZeroPostgresPool() *pgxpool.Pool {
	return &pgxpool.Pool{}
}

var (
	_ Executor              = (*closeErrorExecutor)(nil)
	_ rowSource             = (*coverageRowSource)(nil)
	_ pgx.Rows              = (*coveragePGXRows)(nil)
	_ driver.Driver         = (*coverageSQLDriver)(nil)
	_ driver.Pinger         = (*coverageSQLConn)(nil)
	_ driver.ExecerContext  = (*coverageSQLConn)(nil)
	_ driver.QueryerContext = (*coverageSQLConn)(nil)
	_ postgresRunner        = (*coveragePostgresRunner)(nil)
)
