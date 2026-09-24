package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifySessionStatement(t *testing.T) {
	tests := []struct {
		name        string
		dialect     string
		sql         string
		transaction transactionAction
		write       bool
	}{
		{name: "begin mixed case and comment", dialect: "postgres", sql: "/* guard */ bEgIn WORK", transaction: transactionBegin},
		{name: "start transaction comments", dialect: "mysql", sql: "-- guard\nSTART /* bound */ TRANSACTION", transaction: transactionBegin},
		{name: "commit work and chain", dialect: "postgres", sql: "COMMIT WORK AND CHAIN", transaction: transactionEnd},
		{name: "end work", dialect: "postgres", sql: "END WORK", transaction: transactionEnd},
		{name: "abort", dialect: "postgres", sql: "ABORT", transaction: transactionEnd},
		{name: "rollback", dialect: "mysql", sql: "ROLLBACK WORK", transaction: transactionEnd},
		{name: "rollback to savepoint", dialect: "postgres", sql: "ROLLBACK WORK TO SAVEPOINT before_write"},
		{name: "savepoint", dialect: "postgres", sql: "SAVEPOINT before_write"},
		{name: "release savepoint", dialect: "mysql", sql: "RELEASE SAVEPOINT before_write"},
		{name: "set transaction", dialect: "postgres", sql: "SET TRANSACTION READ ONLY"},
		{name: "mysql autocommit off", dialect: "mysql", sql: "SET autocommit = 0", transaction: transactionBegin},
		{name: "mysql session autocommit off", dialect: "mysql", sql: "SET @@session.autocommit = OFF", transaction: transactionBegin},
		{name: "mysql autocommit on", dialect: "mysql", sql: "SET SESSION autocommit = 1", transaction: transactionEnd},
		{name: "mysql global autocommit ignored", dialect: "mysql", sql: "SET GLOBAL autocommit = 0"},
		{name: "hash comment", dialect: "mysql", sql: "# guard\nBEGIN", transaction: transactionBegin},
		{name: "insert write", dialect: "postgres", sql: "INSERT INTO t VALUES (1)", write: true},
		{name: "update write", dialect: "mysql", sql: "UPDATE t SET a = 1", write: true},
		{name: "delete write", dialect: "mysql", sql: "DELETE FROM t", write: true},
		{name: "merge write", dialect: "postgres", sql: "MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE", write: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification, err := classifySessionStatement(test.sql, test.dialect)
			require.NoError(t, err)
			require.Equal(t, test.transaction, classification.transaction)
			require.Equal(t, test.write, classification.write)
		})
	}
}

func TestClassifySessionStatementRejectsMalformedText(t *testing.T) {
	for _, sqlText := range []string{"", "/* unterminated", "BEGIN 'unterminated"} {
		_, err := classifySessionStatement(sqlText, "postgres")
		require.Error(t, err)
	}
}

func TestBoundSessionTransactionStateMachine(t *testing.T) {
	clock := newFakeSessionClock()
	runner := &spyMySQLRunner{affected: map[string]int64{
		"UPDATE one": 2,
		"UPDATE two": 3,
	}}
	session := newTestMySQLSession(clock, runner)

	initial, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, initial.InTransaction)

	_, err = session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)
	clock.Advance(1200 * time.Millisecond)
	started, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, started.InTransaction)
	require.Equal(t, int64(1200), started.AgeMS)

	_, err = session.Execute(context.Background(), "UPDATE one")
	require.NoError(t, err)
	_, err = session.Execute(context.Background(), "UPDATE two")
	require.NoError(t, err)
	afterWrites, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.Equal(t, int64(5), afterWrites.AffectedRows)

	clock.Advance(800 * time.Millisecond)
	_, err = session.Execute(context.Background(), "/* repeated */ BEGIN")
	require.NoError(t, err)
	repeated, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.Equal(t, int64(2000), repeated.AgeMS)
	require.Equal(t, int64(5), repeated.AffectedRows)

	_, err = session.Execute(context.Background(), "SAVEPOINT before_more")
	require.NoError(t, err)
	afterSavepoint, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, afterSavepoint.InTransaction)
	require.Equal(t, int64(5), afterSavepoint.AffectedRows)

	_, err = session.Execute(context.Background(), "COMMIT AND CHAIN")
	require.NoError(t, err)
	committed, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, committed.InTransaction)
	require.Zero(t, committed.AgeMS)
	require.Zero(t, committed.AffectedRows)

	_, err = session.Execute(context.Background(), "SET @@session.autocommit = 0")
	require.NoError(t, err)
	autocommitOff, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, autocommitOff.InTransaction)
	_, err = session.Execute(context.Background(), "SET autocommit = 1")
	require.NoError(t, err)
	autocommitOn, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, autocommitOn.InTransaction)

	_, err = session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)
	_, err = session.Execute(context.Background(), "ROLLBACK")
	require.NoError(t, err)
	rolledBack, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, rolledBack.InTransaction)
}

func TestTransactionStateMachinePostgresIdleTime(t *testing.T) {
	clock := newFakeSessionClock()
	machine := newTransactionStateMachine(clock)
	begin, err := classifySessionStatement("BEGIN", "postgres")
	require.NoError(t, err)
	machine.finishStatement(begin, clock.Now(), 0, true)
	clock.Advance(1500 * time.Millisecond)

	inTransaction, ageMS, idleMS, affectedRows, err := machine.snapshot()
	require.NoError(t, err)
	require.True(t, inTransaction)
	require.Equal(t, int64(1500), ageMS)
	require.Equal(t, int64(1500), idleMS)
	require.Zero(t, affectedRows)
}

func TestBoundSessionCloseRollsBackAndReleases(t *testing.T) {
	clock := newFakeSessionClock()
	runner := &spyMySQLRunner{affected: make(map[string]int64)}
	session := newTestMySQLSession(clock, runner)
	_, err := session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)
	_, err = session.Execute(context.Background(), "UPDATE t SET a = 1")
	require.NoError(t, err)

	require.NoError(t, session.Close())
	require.Equal(t, 1, sessionReleaseCount(session))
	require.Equal(t, 0, sessionDiscardCount(session))
	require.Equal(t, "ROLLBACK", runner.lastStatement())
	_, err = session.MysqlTransactionState()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSessionClosed))
}

func TestBoundSessionRollbackFailureDiscardsConnection(t *testing.T) {
	clock := newFakeSessionClock()
	runner := &spyMySQLRunner{
		affected:    make(map[string]int64),
		rollbackErr: errors.New("rollback failed"),
	}
	session := newTestMySQLSession(clock, runner)
	_, err := session.Execute(context.Background(), "BEGIN")
	require.NoError(t, err)

	err = session.Close()
	require.Error(t, err)
	require.Equal(t, 0, sessionReleaseCount(session))
	require.Equal(t, 1, sessionDiscardCount(session))
}

func newTestMySQLSession(clock sessionClock, runner *spyMySQLRunner) *mysqlSession {
	executor := &MySQLExecutor{
		timeout:  time.Second,
		clock:    clock,
		sessions: make(map[string]Session),
	}
	session := &mysqlSession{
		id:       "session-test",
		executor: executor,
		runner:   runner,
		state:    newTransactionStateMachine(clock),
	}
	executor.sessions[session.id] = session
	session.release = func() error {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		runner.released++
		return nil
	}
	session.discard = func() error {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		runner.discarded++
		return nil
	}
	return session
}

func sessionReleaseCount(session *mysqlSession) int {
	runner := session.runner.(*spyMySQLRunner)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.released
}

func sessionDiscardCount(session *mysqlSession) int {
	runner := session.runner.(*spyMySQLRunner)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.discarded
}

type fakeSessionClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeSessionClock() *fakeSessionClock {
	return &fakeSessionClock{now: time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)}
}

func (clock *fakeSessionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeSessionClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

type spyMySQLRunner struct {
	mu          sync.Mutex
	statements  []string
	affected    map[string]int64
	rollbackErr error
	released    int
	discarded   int
}

func (runner *spyMySQLRunner) ExecContext(
	_ context.Context,
	query string,
	_ ...any,
) (sql.Result, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.statements = append(runner.statements, query)
	if strings.EqualFold(strings.TrimSpace(query), "ROLLBACK") && runner.rollbackErr != nil {
		return nil, runner.rollbackErr
	}
	return fakeSQLResult(runner.affected[query]), nil
}

func (*spyMySQLRunner) QueryContext(
	context.Context,
	string,
	...any,
) (*sql.Rows, error) {
	return nil, errors.New("query not configured")
}

func (*spyMySQLRunner) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (runner *spyMySQLRunner) lastStatement() string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.statements) == 0 {
		return ""
	}
	return runner.statements[len(runner.statements)-1]
}

type fakeSQLResult int64

func (result fakeSQLResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (result fakeSQLResult) RowsAffected() (int64, error) {
	return int64(result), nil
}

var _ mysqlRunner = (*spyMySQLRunner)(nil)
