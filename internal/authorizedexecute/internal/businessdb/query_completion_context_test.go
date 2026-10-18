package businessdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Model a driver that returns complete rows and a nil Rows.Err even though the
// caller canceled while those rows were being read. No result may escape.
func TestPostgresQueryRejectsCancellationAfterRows(t *testing.T) {
	for _, test := range []struct {
		name     string
		context  func() (context.Context, context.CancelFunc)
		wantKind DBErrorKind
		wantCode DBErrorCode
	}{
		{
			name: "caller canceled",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantKind: DBErrorKindInterrupted,
			wantCode: DBErrorCodeInterrupted,
		},
		{
			name: "deadline expired",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			wantKind: DBErrorKindTimeout,
			wantCode: DBErrorCodeTimeout,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			runner := &contextCompletionRunner{
				coveragePostgresRunner: &coveragePostgresRunner{rows: [][]any{{int64(1), "sensitive"}}},
				afterValues: func() {
					if test.wantKind == DBErrorKindTimeout {
						<-ctx.Done()
					} else {
						cancel()
					}
				},
			}
			executor := &PostgresExecutor{timeout: time.Second}
			result, err := executor.queryWithRunner(ctx, runner, "SELECT secret FROM t", 10)
			require.Empty(t, result.Rows)
			requireDBError(t, err, test.wantKind, test.wantCode, DBStageReadRows)
		})
	}
}

// The SQL driver may return rows backed by an independent context. The
// operation context must still gate delivery for DM and MySQL paths.
func TestSQLQueryRejectsCancellationAfterRows(t *testing.T) {
	for _, test := range []struct {
		name  string
		query func(context.Context, *detachedCompletionRunner) (error, int)
	}{
		{
			name: "limited DM path",
			query: func(ctx context.Context, runner *detachedCompletionRunner) (error, int) {
				executor := &limitedSQLExecutor{dialect: "dm", timeout: time.Second, classifier: classifyDMError}
				result, err := executor.queryWithRunner(ctx, runner, "SELECT id FROM t", 10)
				return err, len(result.Rows)
			},
		},
		{
			name: "MySQL path",
			query: func(ctx context.Context, runner *detachedCompletionRunner) (error, int) {
				executor := &MySQLExecutor{timeout: time.Second}
				result, err := executor.queryWithRunner(ctx, runner, "SELECT id FROM t", 10)
				return err, len(result.Rows)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openCoverageSQLDatabase(t, "completion-context")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &detachedCompletionRunner{DB: database, cancel: cancel}
			err, deliveredRows := test.query(ctx, runner)
			require.Zero(t, deliveredRows)
			requireDBError(t, err, DBErrorKindInterrupted, DBErrorCodeInterrupted, DBStageReadRows)
		})
	}
}

type detachedCompletionRunner struct {
	*sql.DB
	cancel context.CancelFunc
}

func (runner *detachedCompletionRunner) QueryContext(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := runner.DB.QueryContext(context.Background(), query, args...)
	if err == nil {
		runner.cancel()
	}
	return rows, err
}

type contextCompletionRunner struct {
	*coveragePostgresRunner
	afterValues func()
}

func (runner *contextCompletionRunner) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := runner.coveragePostgresRunner.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &contextCompletionRows{Rows: rows, afterValues: runner.afterValues}, nil
}

type contextCompletionRows struct {
	pgx.Rows
	afterValues func()
}

func (rows *contextCompletionRows) Values() ([]any, error) {
	values, err := rows.Rows.Values()
	if err == nil && rows.afterValues != nil {
		rows.afterValues()
		rows.afterValues = nil
	}
	return values, err
}
