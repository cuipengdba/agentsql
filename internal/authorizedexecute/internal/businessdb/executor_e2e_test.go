package businessdb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresExecutorE2E(t *testing.T) {
	for _, version := range []string{"14", "15", "16", "17", "18"} {
		version := version
		t.Run("pg"+version, func(t *testing.T) {
			runPostgresExecutorScenarios(t, "postgres:"+version)
		})
	}
}

func runPostgresExecutorScenarios(t *testing.T, image string) {
	t.Helper()
	ctx := dockerTestContext(t)
	const (
		database = "agentsql"
		username = "agentsql"
		password = "agentsql-password"
	)
	container, err := postgrescontainer.Run(
		ctx,
		image,
		postgrescontainer.WithDatabase(database),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start %s (Docker daemon probe already succeeded)", image)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{
		ID:            "ds_postgres_e2e_" + strings.TrimPrefix(image, "postgres:"),
		DBType:        "postgres",
		Host:          host,
		Port:          port.Int(),
		Database:      database,
		Username:      username,
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
	}
	executor, err := NewPostgresExecutor(ctx, datasource, password, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, executor.Close())
	})

	_, err = executor.Execute(ctx, `CREATE TABLE executor_rows (
  id integer PRIMARY KEY,
  value integer NOT NULL
)`)
	require.NoError(t, err)
	_, err = executor.Execute(ctx, `
INSERT INTO executor_rows (id, value)
SELECT value, value FROM generate_series(1, 5000) AS value`)
	require.NoError(t, err)
	_, err = executor.Execute(ctx, "ANALYZE executor_rows")
	require.NoError(t, err)
	_, err = executor.Execute(ctx, "CREATE TYPE executor_order_state AS ENUM ('paid', 'pending')")
	require.NoError(t, err)
	_, err = executor.Execute(ctx, `CREATE TABLE executor_value_types (
  id integer PRIMARY KEY,
  amount numeric(10, 2) NOT NULL,
  discount decimal(10, 2),
  ratio double precision NOT NULL,
  item_count bigint NOT NULL,
  active boolean NOT NULL,
  event_date date NOT NULL,
  created_at timestamp NOT NULL,
  note text NOT NULL,
  external_id uuid NOT NULL,
  elapsed interval NOT NULL,
  metadata jsonb NOT NULL,
  labels text[] NOT NULL,
  state executor_order_state NOT NULL,
  payload bytea NOT NULL,
  client_inet inet NOT NULL,
  network_cidr cidr NOT NULL
)`)
	require.NoError(t, err)
	_, err = executor.Execute(ctx, `INSERT INTO executor_value_types (
  id, amount, discount, ratio, item_count, active, event_date, created_at, note,
  external_id, elapsed, metadata, labels, state, payload, client_inet, network_cidr
) VALUES (
  1, 22.74, NULL, 1.25, 9000000000, true, DATE '2026-09-17',
  TIMESTAMP '2026-09-17 08:09:10.123456', 'paid order',
  '12345678-1234-5678-90ab-cdef12345678', INTERVAL '1 day 02:03:04.005',
  '{"paid": true}'::jsonb, ARRAY['paid', 'priority'], 'paid', decode('68656c6c6f', 'hex'),
  '192.0.2.10'::inet, '198.51.100.0/24'::cidr
)`)
	require.NoError(t, err)

	t.Run("connection and select", func(t *testing.T) {
		require.NoError(t, executor.Ping(ctx))
		result, err := executor.Query(ctx, "SELECT value FROM executor_rows WHERE id = 1", 1)
		require.NoError(t, err)
		require.Equal(t, 1, result.RowCount)
		require.Equal(t, "1", result.Rows[0][0])
	})

	t.Run("common result values are human readable", func(t *testing.T) {
		result, err := executor.Query(ctx, `SELECT
  id, amount, discount, ratio, item_count, active, event_date, created_at, note
FROM executor_value_types WHERE id = 1`, 1)
		require.NoError(t, err)
		require.Equal(t, []string{
			"1", "22.74", "", "1.25", "9000000000", "true",
			"2026-09-17T00:00:00Z", "2026-09-17T08:09:10.123456Z", "paid order",
		}, result.Rows[0])
		for _, cell := range result.Rows[0] {
			require.NotContains(t, cell, "{")
			require.NotContains(t, cell, "finite")
		}
	})

	t.Run("inet cidr and date preserve driver string shapes", func(t *testing.T) {
		result, err := executor.Query(ctx, `SELECT client_inet, network_cidr, event_date
FROM executor_value_types WHERE id = 1`, 1)
		require.NoError(t, err)
		require.Equal(t, [][]string{{"192.0.2.10/32", "198.51.100.0/24", "2026-09-17T00:00:00Z"}}, result.Rows)
	})

	t.Run("structured result values are human readable", func(t *testing.T) {
		result, err := executor.Query(ctx, `SELECT
  external_id, elapsed, metadata, labels, state, payload
FROM executor_value_types WHERE id = 1`, 1)
		require.NoError(t, err)
		require.Equal(t, []string{
			"12345678-1234-5678-90ab-cdef12345678",
			"1 day 02:03:04.005000",
			`{"paid":true}`,
			`["paid","priority"]`,
			"paid",
			"hello",
		}, result.Rows[0])
		for _, cell := range result.Rows[0] {
			require.NotContains(t, cell, "finite")
			require.NotContains(t, cell, "{2274")
		}
	})

	t.Run("pgx default decoded value types", func(t *testing.T) {
		rows, err := executor.pool.Query(ctx, `SELECT
  amount, active, id, item_count, ratio, external_id, event_date, created_at,
  elapsed, metadata, payload, labels, state
FROM executor_value_types WHERE id = 1`)
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
		values, err := rows.Values()
		require.NoError(t, err)
		require.IsType(t, pgtype.Numeric{}, values[0])
		require.IsType(t, false, values[1])
		require.IsType(t, int32(0), values[2])
		require.IsType(t, int64(0), values[3])
		require.IsType(t, float64(0), values[4])
		require.IsType(t, [16]byte{}, values[5])
		require.IsType(t, time.Time{}, values[6])
		require.IsType(t, time.Time{}, values[7])
		require.IsType(t, pgtype.Interval{}, values[8])
		require.IsType(t, map[string]any{}, values[9])
		require.IsType(t, []byte{}, values[10])
		require.IsType(t, []any{}, values[11])
		require.IsType(t, "", values[12])
		require.False(t, rows.Next())
		require.NoError(t, rows.Err())
	})

	t.Run("N+1 fetch detects row limit truncation", func(t *testing.T) {
		result, err := executor.Query(ctx, "SELECT id FROM executor_rows ORDER BY id", 2)
		require.NoError(t, err)
		require.Equal(t, 2, result.RowCount)
		require.Len(t, result.Rows, 2)
		require.True(t, result.Truncated)
	})

	t.Run("read only rejects write", func(t *testing.T) {
		readOnlyExecutor, err := NewPostgresExecutor(ctx, datasource, password, true)
		require.NoError(t, err)
		defer func() { require.NoError(t, readOnlyExecutor.Close()) }()
		_, err = readOnlyExecutor.Execute(ctx, "UPDATE executor_rows SET value = 0")
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrReadOnlyViolated))
		_, err = readOnlyExecutor.Query(ctx, "UPDATE executor_rows SET value = 0 RETURNING id", 1)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrReadOnlyViolated))
	})

	t.Run("explain and metadata identify index", func(t *testing.T) {
		before, err := executor.Explain(ctx, "SELECT * FROM executor_rows WHERE value = 4999")
		require.NoError(t, err)
		require.True(t, before.SeqScan)
		require.False(t, before.UsesIndex)
		hasIndex, err := executor.TableHasIndex("public", "executor_rows")
		require.NoError(t, err)
		require.True(t, hasIndex, "primary key is a valid index")

		_, err = executor.Execute(ctx, "CREATE INDEX executor_rows_value_idx ON executor_rows(value)")
		require.NoError(t, err)
		_, err = executor.Execute(ctx, "ANALYZE executor_rows")
		require.NoError(t, err)
		after, err := executor.Explain(ctx, "SELECT * FROM executor_rows WHERE value = 4999")
		require.NoError(t, err)
		require.True(t, after.UsesIndex)
		rowCount, err := executor.TableRowCount("public", "executor_rows")
		require.NoError(t, err)
		require.GreaterOrEqual(t, rowCount, int64(5000))
	})

	t.Run("bound session basic read write", func(t *testing.T) {
		session, err := executor.OpenSession(ctx, "pg-basic")
		require.NoError(t, err)
		result, err := session.Query(ctx, "SELECT value FROM executor_rows WHERE id = 4", 1)
		require.NoError(t, err)
		require.Equal(t, "4", result.Rows[0][0])
		updated, err := session.Execute(ctx, "UPDATE executor_rows SET value = value + 1 WHERE id = 4")
		require.NoError(t, err)
		require.Equal(t, 1, updated.RowCount)
		result, err = session.Query(ctx, "SELECT value FROM executor_rows WHERE id = 4", 1)
		require.NoError(t, err)
		require.Equal(t, "5", result.Rows[0][0])
		require.NoError(t, session.Close())
	})

	if image == "postgres:14" || image == "postgres:18" {
		t.Run("timeout interrupts query", func(t *testing.T) {
			shortDatasource := datasource
			shortDatasource.ID += "_timeout"
			shortDatasource.StmtTimeoutMS = 25
			shortExecutor, err := NewPostgresExecutor(ctx, shortDatasource, password, false)
			require.NoError(t, err)
			defer func() { require.NoError(t, shortExecutor.Close()) }()
			_, err = shortExecutor.Query(ctx, "SELECT pg_sleep(1)", 1)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrQueryTimeout))
		})

		t.Run("bound session transaction state", func(t *testing.T) {
			testPostgresBoundSessions(t, ctx, executor)
		})
	}

	if image == "postgres:18" {
		t.Run("write transaction commit rollback and session lock", func(t *testing.T) {
			tx, err := executor.BeginWriteTx(ctx)
			require.NoError(t, err)
			result, err := tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (6001,6001)")
			require.NoError(t, err)
			require.Equal(t, 1, result.RowCount)
			assertPostgresRowCount(t, ctx, executor, 6001, "0")
			require.NoError(t, tx.Commit(ctx))
			require.NoError(t, tx.Commit(ctx))
			require.NoError(t, tx.Rollback(ctx))
			assertPostgresRowCount(t, ctx, executor, 6001, "1")

			tx, err = executor.BeginWriteTx(ctx)
			require.NoError(t, err)
			result, err = tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (6002,6002)")
			require.NoError(t, err)
			require.Equal(t, 1, result.RowCount)
			require.NoError(t, tx.Rollback(ctx))
			require.NoError(t, tx.Rollback(ctx))
			require.NoError(t, tx.Commit(ctx))
			assertPostgresRowCount(t, ctx, executor, 6002, "0")
			tx, err = executor.BeginWriteTx(ctx)
			require.NoError(t, err)
			_, err = tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (1,1)")
			rollbackErr := tx.Rollback(ctx)
			var databaseError *DBError
			require.ErrorAs(t, err, &databaseError)
			require.Equal(t, DBErrorCodeConstraint, databaseError.Code)
			require.Equal(t, DBErrorKindConstraint, databaseError.Kind)
			require.Equal(t, "23505", databaseError.DriverCode)
			require.Contains(t, err.Error(), "约束")
			require.NotContains(t, err.Error(), password)
			require.NotContains(t, err.Error(), "duplicate key")
			require.NotContains(t, err.Error(), "(id)=(1)")
			require.NoError(t, rollbackErr)

			session, err := executor.OpenSession(ctx, "pg-write-tx-lock")
			require.NoError(t, err)
			sessionTx, err := session.BeginWriteTx(ctx)
			require.NoError(t, err)
			_, err = sessionTx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (6003,6003)")
			require.NoError(t, err)
			closed := make(chan error, 1)
			go func() { closed <- session.Close() }()
			select {
			case err := <-closed:
				require.Failf(t, "session close did not wait for write transaction", "close returned %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			require.NoError(t, sessionTx.Rollback(ctx))
			require.NoError(t, <-closed)
			assertPostgresRowCount(t, ctx, executor, 6003, "0")

			session, err = executor.OpenSession(ctx, "pg-write-tx-explicit")
			require.NoError(t, err)
			_, err = session.Execute(ctx, "BEGIN")
			require.NoError(t, err)
			_, err = session.BeginWriteTx(ctx)
			require.ErrorIs(t, err, ErrSessionTransactionActive)
			require.NoError(t, session.Close())
		})

		t.Run("bound session fail closed after cancellation and close", func(t *testing.T) {
			sessionValue, err := executor.OpenSession(ctx, "pg-fail-closed")
			require.NoError(t, err)
			session := sessionValue.(*postgresSession)

			_, err = session.Query(ctx, " ", 1)
			require.ErrorContains(t, err, "SQL is empty")
			_, err = session.Execute(ctx, "")
			require.ErrorContains(t, err, "SQL is empty")
			_, err = session.Explain(ctx, "\t")
			require.ErrorContains(t, err, "SQL is empty")

			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = session.Query(canceled, "SELECT 1", 1)
			require.ErrorIs(t, err, ErrQueryTimeout)
			_, err = session.Execute(canceled, "SELECT 1")
			require.ErrorIs(t, err, ErrQueryTimeout)
			_, err = session.Explain(canceled, "SELECT 1")
			require.ErrorIs(t, err, ErrQueryTimeout)

			hasIndex, err := session.TableHasIndex("public", "executor_rows")
			require.NoError(t, err)
			require.True(t, hasIndex)
			rows, err := session.TableRowCount("public", "executor_rows")
			require.NoError(t, err)
			require.GreaterOrEqual(t, rows, int64(5000))

			require.NoError(t, session.Close())
			_, err = session.Query(ctx, "SELECT 1", 1)
			require.ErrorIs(t, err, ErrSessionClosed)
			_, err = session.Execute(ctx, "SELECT 1")
			require.ErrorIs(t, err, ErrSessionClosed)
			_, err = session.Explain(ctx, "SELECT 1")
			require.ErrorIs(t, err, ErrSessionClosed)
			_, err = session.TransactionState()
			require.ErrorIs(t, err, ErrSessionClosed)
			_, err = session.TableHasIndex("public", "executor_rows")
			require.ErrorIs(t, err, ErrSessionClosed)
			_, err = session.TableRowCount("public", "executor_rows")
			require.ErrorIs(t, err, ErrSessionClosed)
		})

		t.Run("pool limit blocks a second bound connection", func(t *testing.T) {
			limitedDatasource := datasource
			limitedDatasource.ID += "_pool_limit"
			limitedDatasource.ConnLimit = 1
			limited, err := NewPostgresExecutor(ctx, limitedDatasource, password, false)
			require.NoError(t, err)
			defer func() { require.NoError(t, limited.Close()) }()
			first, err := limited.OpenSession(ctx, "first")
			require.NoError(t, err)
			defer func() { require.NoError(t, first.Close()) }()
			acquireCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
			defer cancel()
			_, err = limited.OpenSession(acquireCtx, "second")
			require.ErrorIs(t, err, ErrQueryTimeout)
		})

		t.Run("wrong database ping is redacted", func(t *testing.T) {
			bad := datasource
			bad.ID += "_wrong_database"
			bad.Database = "agentsql_missing_database"
			_, err := NewPostgresExecutor(ctx, bad, password, false)
			require.ErrorIs(t, err, ErrDatasourceUnreachable)
			require.NotContains(t, err.Error(), password)
		})
	}
}

func TestMySQLExecutorE2E(t *testing.T) {
	ctx := dockerTestContext(t)
	const (
		database = "agentsql"
		username = "agentsql"
		password = "agentsql-password"
	)
	container, err := mysqlcontainer.Run(
		ctx,
		"mysql:8",
		mysqlcontainer.WithDatabase(database),
		mysqlcontainer.WithUsername(username),
		mysqlcontainer.WithPassword(password),
		testcontainers.WithWaitStrategyAndDeadline(300*time.Second,
			wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(300*time.Second),
		),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start mysql:8 (Docker daemon probe already succeeded)")
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{
		ID:            "ds_mysql_e2e",
		DBType:        "mysql",
		Host:          host,
		Port:          port.Int(),
		Database:      database,
		Username:      username,
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
	}
	executor, err := NewMySQLExecutor(ctx, datasource, password, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, executor.Close())
	})
	_, err = executor.Execute(ctx, `CREATE TABLE executor_rows (
  id integer PRIMARY KEY,
  value integer NOT NULL
)`)
	require.NoError(t, err)
	var insert strings.Builder
	insert.WriteString("INSERT INTO executor_rows (id, value) VALUES ")
	for value := 1; value <= 2000; value++ {
		if value > 1 {
			insert.WriteByte(',')
		}
		insert.WriteByte('(')
		insert.WriteString(strconv.Itoa(value))
		insert.WriteByte(',')
		insert.WriteString(strconv.Itoa(value))
		insert.WriteByte(')')
	}
	_, err = executor.Execute(ctx, insert.String())
	require.NoError(t, err)
	_, err = executor.Execute(ctx, "ANALYZE TABLE executor_rows")
	require.NoError(t, err)
	_, err = executor.Execute(ctx, `CREATE TABLE executor_date_types (
  id integer PRIMARY KEY,
  event_date date NOT NULL
)`)
	require.NoError(t, err)
	_, err = executor.Execute(ctx, `INSERT INTO executor_date_types (id, event_date) VALUES (1, DATE '2026-09-17')`)
	require.NoError(t, err)

	t.Run("date result uses RFC3339Nano string shape", func(t *testing.T) {
		result, err := executor.Query(ctx, "SELECT event_date FROM executor_date_types WHERE id = 1", 1)
		require.NoError(t, err)
		require.Equal(t, [][]string{{"2026-09-17T00:00:00Z"}}, result.Rows)
	})

	t.Run("decimal result is human readable", func(t *testing.T) {
		result, err := executor.Query(ctx, "SELECT CAST(22.74 AS DECIMAL(10, 2)) AS amount", 1)
		require.NoError(t, err)
		require.Equal(t, [][]string{{"22.74"}}, result.Rows)
	})

	t.Run("timeout interrupts query", func(t *testing.T) {
		shortDatasource := datasource
		shortDatasource.ID = "ds_mysql_timeout"
		shortDatasource.StmtTimeoutMS = 25
		shortExecutor, err := NewMySQLExecutor(ctx, shortDatasource, password, false)
		require.NoError(t, err)
		defer func() { require.NoError(t, shortExecutor.Close()) }()
		// 注意：不能用 SELECT SLEEP(1) 验证 MySQL 超时——MAX_EXECUTION_TIME 打断
		// SLEEP() 时只会让其返回 1、整条查询并不报错（已实测），无法触发 3024。
		// 改用优化器无法用行数乘积化简的三表逐行聚合（2000^3 次真实运算），
		// 稳定在 25ms 触发 3024；即便 hint 失效，无 hint 时耗时远超 275ms，
		// 也会被执行器的 context 超时兜底，两层都能得到 ErrQueryTimeout。
		slowQuery := "SELECT sum(a.value + b.value - c.value) " +
			"FROM executor_rows a, executor_rows b, executor_rows c"
		_, err = shortExecutor.Query(ctx, slowQuery, 1)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrQueryTimeout))
		var databaseError *DBError
		require.ErrorAs(t, err, &databaseError)
		require.Equal(t, DBErrorKindTimeout, databaseError.Kind)
		require.Equal(t, DBErrorCodeTimeout, databaseError.Code)
		require.Contains(t, []DBStage{DBStageQuery, DBStageReadRows}, databaseError.Stage)
		driverCode, _ := databaseError.DriverCodeForLog()
		t.Logf("timeout classification: code=%s stage=%s driver_code=%s", databaseError.Code, databaseError.Stage, driverCode)
	})

	t.Run("client cancellation interrupts an in flight query", func(t *testing.T) {
		queryContext, cancel := context.WithCancel(ctx)
		errChannel := make(chan error, 1)
		go func() {
			_, queryErr := executor.Query(queryContext, "SELECT SLEEP(30)", 1)
			errChannel <- queryErr
		}()
		time.Sleep(250 * time.Millisecond)
		cancel()
		err := <-errChannel
		var databaseError *DBError
		require.ErrorAs(t, err, &databaseError)
		require.Equal(t, DBErrorKindInterrupted, databaseError.Kind)
		require.Equal(t, DBErrorCodeInterrupted, databaseError.Code)
		require.Contains(t, []DBStage{DBStageQuery, DBStageReadRows}, databaseError.Stage)
		driverCode, _ := databaseError.DriverCodeForLog()
		t.Logf("cancellation classification: code=%s stage=%s driver_code=%s", databaseError.Code, databaseError.Stage, driverCode)
		require.NotContains(t, err.Error(), "Query execution was interrupted")
	})

	t.Run("N+1 fetch detects row limit truncation", func(t *testing.T) {
		result, err := executor.Query(ctx, "SELECT id FROM executor_rows ORDER BY id", 2)
		require.NoError(t, err)
		require.Equal(t, 2, result.RowCount)
		require.Len(t, result.Rows, 2)
		require.True(t, result.Truncated)
	})

	t.Run("read only rejects write", func(t *testing.T) {
		readOnlyExecutor, err := NewMySQLExecutor(ctx, datasource, password, true)
		require.NoError(t, err)
		defer func() { require.NoError(t, readOnlyExecutor.Close()) }()
		_, err = readOnlyExecutor.Execute(ctx, "UPDATE executor_rows SET value = 0")
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrReadOnlyViolated))
	})

	t.Run("explain and metadata identify index", func(t *testing.T) {
		before, err := executor.Explain(ctx, "SELECT * FROM executor_rows WHERE value = 1999")
		require.NoError(t, err)
		require.True(t, before.SeqScan)
		require.False(t, before.UsesIndex)

		_, err = executor.Execute(ctx, "CREATE INDEX executor_rows_value_idx ON executor_rows(value)")
		require.NoError(t, err)
		after, err := executor.Explain(ctx, "SELECT * FROM executor_rows WHERE value = 1999")
		require.NoError(t, err)
		require.True(t, after.UsesIndex)
		hasIndex, err := executor.TableHasIndex(database, "executor_rows")
		require.NoError(t, err)
		require.True(t, hasIndex)
		rowCount, err := executor.TableRowCount(database, "executor_rows")
		require.NoError(t, err)
		require.Positive(t, rowCount)
	})

	t.Run("explain write statements accepts MySQL plan shapes", func(t *testing.T) {
		insertInfo, err := executor.Explain(
			ctx,
			"INSERT INTO executor_rows (id, value) VALUES (4001, 4001)",
		)
		require.NoError(t, err)
		require.Zero(t, insertInfo.EstScanRows)
		require.NotEmpty(t, insertInfo.Raw)
		t.Logf("INSERT EXPLAIN: seq_scan=%t uses_index=%t raw=%q", insertInfo.SeqScan, insertInfo.UsesIndex, insertInfo.Raw)

		updateInfo, err := executor.Explain(
			ctx,
			"UPDATE executor_rows SET value = value + 1 WHERE id = 1",
		)
		require.NoError(t, err)
		require.GreaterOrEqual(t, updateInfo.EstScanRows, int64(0))

		deleteInfo, err := executor.Explain(
			ctx,
			"DELETE FROM executor_rows WHERE id = 2000",
		)
		require.NoError(t, err)
		require.GreaterOrEqual(t, deleteInfo.EstScanRows, int64(0))
	})

	t.Run("bound session transaction state", func(t *testing.T) {
		testMySQLBoundSessions(t, ctx, executor)
	})

	t.Run("write transaction commit rollback and session lock", func(t *testing.T) {
		tx, err := executor.BeginWriteTx(ctx)
		require.NoError(t, err)
		result, err := tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (3001,3001)")
		require.NoError(t, err)
		require.Equal(t, 1, result.RowCount)
		assertMySQLRowCount(t, ctx, executor, 3001, "0")
		require.NoError(t, tx.Commit(ctx))
		require.NoError(t, tx.Commit(ctx))
		require.NoError(t, tx.Rollback(ctx))
		assertMySQLRowCount(t, ctx, executor, 3001, "1")

		tx, err = executor.BeginWriteTx(ctx)
		require.NoError(t, err)
		result, err = tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (3002,3002)")
		require.NoError(t, err)
		require.Equal(t, 1, result.RowCount)
		require.NoError(t, tx.Rollback(ctx))
		require.NoError(t, tx.Rollback(ctx))
		require.NoError(t, tx.Commit(ctx))
		assertMySQLRowCount(t, ctx, executor, 3002, "0")
		tx, err = executor.BeginWriteTx(ctx)
		require.NoError(t, err)
		_, err = tx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (1,1)")
		rollbackErr := tx.Rollback(ctx)
		var databaseError *DBError
		require.ErrorAs(t, err, &databaseError)
		require.Equal(t, DBErrorCodeConstraint, databaseError.Code)
		require.Equal(t, DBErrorKindConstraint, databaseError.Kind)
		require.Equal(t, "1062", databaseError.DriverCode)
		require.Contains(t, err.Error(), "约束")
		require.NotContains(t, err.Error(), password)
		require.NotContains(t, err.Error(), "Duplicate entry")
		require.NotContains(t, err.Error(), "'1' for key")
		require.NoError(t, rollbackErr)

		session, err := executor.OpenSession(ctx, "mysql-write-tx-lock")
		require.NoError(t, err)
		sessionTx, err := session.BeginWriteTx(ctx)
		require.NoError(t, err)
		_, err = sessionTx.Execute(ctx, "INSERT INTO executor_rows(id,value) VALUES (3003,3003)")
		require.NoError(t, err)
		closed := make(chan error, 1)
		go func() { closed <- session.Close() }()
		select {
		case err := <-closed:
			require.Failf(t, "session close did not wait for write transaction", "close returned %v", err)
		case <-time.After(25 * time.Millisecond):
		}
		require.NoError(t, sessionTx.Rollback(ctx))
		require.NoError(t, <-closed)
		assertMySQLRowCount(t, ctx, executor, 3003, "0")

		session, err = executor.OpenSession(ctx, "mysql-write-tx-explicit")
		require.NoError(t, err)
		_, err = session.Execute(ctx, "START TRANSACTION")
		require.NoError(t, err)
		_, err = session.BeginWriteTx(ctx)
		require.ErrorIs(t, err, ErrSessionTransactionActive)
		require.NoError(t, session.Close())
	})

	t.Run("select for update lock wait 1205 is retryable at query stage", func(t *testing.T) {
		holder, err := executor.OpenSession(ctx, "mysql-lock-wait-holder")
		require.NoError(t, err)
		holderTx, err := holder.BeginWriteTx(ctx)
		require.NoError(t, err)
		_, err = holderTx.Execute(ctx, "SELECT value FROM executor_rows WHERE id = 1 FOR UPDATE")
		require.NoError(t, err)

		waiter, err := executor.OpenSession(ctx, "mysql-lock-wait-waiter")
		require.NoError(t, err)
		_, err = waiter.Execute(ctx, "SET SESSION innodb_lock_wait_timeout = 1")
		require.NoError(t, err)
		_, err = waiter.Query(ctx, "SELECT value FROM executor_rows WHERE id = 1 FOR UPDATE", 1)
		databaseError := requireDBError(
			t,
			err,
			DBErrorKindRetryable,
			DBErrorCodeRetryable,
			DBStageQuery,
		)
		require.Equal(t, "1205", mustDriverCodeForLog(t, databaseError))
		require.NotContains(t, err.Error(), "Lock wait timeout")

		require.NoError(t, waiter.Close())
		require.NoError(t, holderTx.Rollback(ctx))
		require.NoError(t, holder.Close())
	})

	t.Run("bound session fail closed after cancellation and close", func(t *testing.T) {
		session, err := executor.OpenSession(ctx, "mysql-fail-closed")
		require.NoError(t, err)
		_, err = session.Query(ctx, " ", 1)
		require.ErrorContains(t, err, "SQL is empty")
		_, err = session.Execute(ctx, "")
		require.ErrorContains(t, err, "SQL is empty")
		_, err = session.Explain(ctx, "\t")
		require.ErrorContains(t, err, "SQL is empty")

		_, err = session.Execute(ctx, "SET autocommit = 0")
		require.NoError(t, err)
		state, err := session.MysqlTransactionState()
		require.NoError(t, err)
		require.True(t, state.InTransaction)
		_, err = session.Execute(ctx, "SET autocommit = 1")
		require.NoError(t, err)
		state, err = session.MysqlTransactionState()
		require.NoError(t, err)
		require.False(t, state.InTransaction)

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = session.Query(canceled, "SELECT 1", 1)
		require.ErrorIs(t, err, ErrQueryTimeout)
		_, err = session.Execute(canceled, "SELECT 1")
		require.ErrorIs(t, err, ErrQueryTimeout)
		_, err = session.Explain(canceled, "SELECT 1")
		require.ErrorIs(t, err, ErrQueryTimeout)

		require.NoError(t, session.Close())
		_, err = session.Query(ctx, "SELECT 1", 1)
		require.ErrorIs(t, err, ErrSessionClosed)
		_, err = session.Execute(ctx, "SELECT 1")
		require.ErrorIs(t, err, ErrSessionClosed)
		_, err = session.Explain(ctx, "SELECT 1")
		require.ErrorIs(t, err, ErrSessionClosed)
		_, err = session.MysqlTransactionState()
		require.ErrorIs(t, err, ErrSessionClosed)
	})

	t.Run("pool limit blocks a second bound connection", func(t *testing.T) {
		limitedDatasource := datasource
		limitedDatasource.ID = "ds_mysql_pool_limit"
		limitedDatasource.ConnLimit = 1
		limited, err := NewMySQLExecutor(ctx, limitedDatasource, password, false)
		require.NoError(t, err)
		defer func() { require.NoError(t, limited.Close()) }()
		first, err := limited.OpenSession(ctx, "first")
		require.NoError(t, err)
		defer func() { require.NoError(t, first.Close()) }()
		acquireCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		_, err = limited.OpenSession(acquireCtx, "second")
		require.ErrorIs(t, err, ErrQueryTimeout)
	})

	t.Run("wrong database ping is redacted", func(t *testing.T) {
		bad := datasource
		bad.ID = "ds_mysql_wrong_database"
		bad.Database = "agentsql_missing_database"
		_, err := NewMySQLExecutor(ctx, bad, password, false)
		require.ErrorIs(t, err, ErrDatasourceUnreachable)
		require.NotContains(t, err.Error(), password)
	})
}

func assertPostgresRowCount(t *testing.T, ctx context.Context, executor *PostgresExecutor, id int, want string) {
	t.Helper()
	result, err := executor.Query(ctx, "SELECT COUNT(*) FROM executor_rows WHERE id = "+strconv.Itoa(id), 1)
	require.NoError(t, err)
	require.Equal(t, want, result.Rows[0][0])
}

func assertMySQLRowCount(t *testing.T, ctx context.Context, executor *MySQLExecutor, id int, want string) {
	t.Helper()
	result, err := executor.Query(ctx, "SELECT COUNT(*) FROM executor_rows WHERE id = "+strconv.Itoa(id), 1)
	require.NoError(t, err)
	require.Equal(t, want, result.Rows[0][0])
}

func testPostgresBoundSessions(
	t *testing.T,
	ctx context.Context,
	executor *PostgresExecutor,
) {
	t.Helper()
	session, err := executor.OpenSession(ctx, "pg-state")
	require.NoError(t, err)
	_, err = executor.OpenSession(ctx, "pg-state")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSessionExists))

	initial, err := session.TransactionState()
	require.NoError(t, err)
	require.False(t, initial.InTransaction)
	_, err = session.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = session.Query(ctx, "SELECT count(*) FROM executor_rows", 1)
	require.NoError(t, err)
	time.Sleep(25 * time.Millisecond)
	active, err := session.TransactionState()
	require.NoError(t, err)
	require.True(t, active.InTransaction)
	require.GreaterOrEqual(t, active.AgeMS, int64(20))
	update, err := session.Execute(ctx, "UPDATE executor_rows SET value = value + 10 WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, 1, update.RowCount)
	time.Sleep(25 * time.Millisecond)
	idle, err := session.TransactionState()
	require.NoError(t, err)
	require.GreaterOrEqual(t, idle.IdleMS, int64(20))
	_, err = session.Execute(ctx, "COMMIT")
	require.NoError(t, err)
	committed, err := session.TransactionState()
	require.NoError(t, err)
	require.False(t, committed.InTransaction)
	require.NoError(t, session.Close())

	rollbackSession, err := executor.OpenSession(ctx, "pg-state")
	require.NoError(t, err)
	_, err = rollbackSession.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = rollbackSession.Execute(ctx, "UPDATE executor_rows SET value = 999999 WHERE id = 2")
	require.NoError(t, err)
	require.NoError(t, rollbackSession.Close())
	reopened, err := executor.OpenSession(ctx, "pg-state")
	require.NoError(t, err)
	reopenedState, err := reopened.TransactionState()
	require.NoError(t, err)
	require.False(t, reopenedState.InTransaction)
	value, err := reopened.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	require.Equal(t, "2", value.Rows[0][0])
	require.NoError(t, reopened.Close())

	left, err := executor.OpenSession(ctx, "pg-left")
	require.NoError(t, err)
	right, err := executor.OpenSession(ctx, "pg-right")
	require.NoError(t, err)
	leftID, err := left.Query(ctx, "SELECT pg_backend_pid()", 1)
	require.NoError(t, err)
	rightID, err := right.Query(ctx, "SELECT pg_backend_pid()", 1)
	require.NoError(t, err)
	require.NotEqual(t, leftID.Rows[0][0], rightID.Rows[0][0])
	_, err = left.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = right.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	runConcurrentSessionUpdates(
		t,
		ctx,
		left,
		"UPDATE executor_rows SET value = 22002 WHERE id = 2",
		right,
		"UPDATE executor_rows SET value = 33003 WHERE id = 3",
	)
	leftState, err := left.TransactionState()
	require.NoError(t, err)
	rightState, err := right.TransactionState()
	require.NoError(t, err)
	require.True(t, leftState.InTransaction)
	require.True(t, rightState.InTransaction)
	leftValue, err := left.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	rightValue, err := right.Query(ctx, "SELECT value FROM executor_rows WHERE id = 3", 1)
	require.NoError(t, err)
	require.Equal(t, "22002", leftValue.Rows[0][0])
	require.Equal(t, "33003", rightValue.Rows[0][0])
	leftSeesRight, err := left.Query(ctx, "SELECT value FROM executor_rows WHERE id = 3", 1)
	require.NoError(t, err)
	rightSeesLeft, err := right.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	require.Equal(t, "3", leftSeesRight.Rows[0][0])
	require.Equal(t, "2", rightSeesLeft.Rows[0][0])
	_, err = left.Execute(ctx, "ROLLBACK")
	require.NoError(t, err)
	_, err = right.Execute(ctx, "ROLLBACK")
	require.NoError(t, err)
	leftRolledBack, err := left.TransactionState()
	require.NoError(t, err)
	rightRolledBack, err := right.TransactionState()
	require.NoError(t, err)
	require.False(t, leftRolledBack.InTransaction)
	require.False(t, rightRolledBack.InTransaction)
	require.NoError(t, left.Close())
	require.NoError(t, right.Close())
}

func testMySQLBoundSessions(
	t *testing.T,
	ctx context.Context,
	executor *MySQLExecutor,
) {
	t.Helper()
	session, err := executor.OpenSession(ctx, "mysql-state")
	require.NoError(t, err)
	_, err = executor.OpenSession(ctx, "mysql-state")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSessionExists))

	initial, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, initial.InTransaction)
	_, err = session.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = session.Query(ctx, "SELECT count(*) FROM executor_rows", 1)
	require.NoError(t, err)
	time.Sleep(25 * time.Millisecond)
	active, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, active.InTransaction)
	require.GreaterOrEqual(t, active.AgeMS, int64(20))
	update, err := session.Execute(ctx, "UPDATE executor_rows SET value = value + 10 WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, 1, update.RowCount)
	afterUpdate, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.Equal(t, int64(1), afterUpdate.AffectedRows)
	_, err = session.Execute(ctx, "COMMIT")
	require.NoError(t, err)
	committed, err := session.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, committed.InTransaction)
	require.Zero(t, committed.AffectedRows)
	require.NoError(t, session.Close())

	rollbackSession, err := executor.OpenSession(ctx, "mysql-state")
	require.NoError(t, err)
	_, err = rollbackSession.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = rollbackSession.Execute(ctx, "UPDATE executor_rows SET value = 999999 WHERE id = 2")
	require.NoError(t, err)
	require.NoError(t, rollbackSession.Close())
	reopened, err := executor.OpenSession(ctx, "mysql-state")
	require.NoError(t, err)
	reopenedState, err := reopened.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, reopenedState.InTransaction)
	value, err := reopened.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	require.Equal(t, "2", value.Rows[0][0])
	require.NoError(t, reopened.Close())

	left, err := executor.OpenSession(ctx, "mysql-left")
	require.NoError(t, err)
	right, err := executor.OpenSession(ctx, "mysql-right")
	require.NoError(t, err)
	leftID, err := left.Query(ctx, "SELECT CONNECTION_ID()", 1)
	require.NoError(t, err)
	rightID, err := right.Query(ctx, "SELECT CONNECTION_ID()", 1)
	require.NoError(t, err)
	require.NotEqual(t, leftID.Rows[0][0], rightID.Rows[0][0])
	_, err = left.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = right.Execute(ctx, "BEGIN")
	require.NoError(t, err)
	runConcurrentSessionUpdates(
		t,
		ctx,
		left,
		"UPDATE executor_rows SET value = 22002 WHERE id = 2",
		right,
		"UPDATE executor_rows SET value = 33003 WHERE id = 3",
	)
	leftState, err := left.MysqlTransactionState()
	require.NoError(t, err)
	rightState, err := right.MysqlTransactionState()
	require.NoError(t, err)
	require.True(t, leftState.InTransaction)
	require.True(t, rightState.InTransaction)
	require.Equal(t, int64(1), leftState.AffectedRows)
	require.Equal(t, int64(1), rightState.AffectedRows)
	leftValue, err := left.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	rightValue, err := right.Query(ctx, "SELECT value FROM executor_rows WHERE id = 3", 1)
	require.NoError(t, err)
	require.Equal(t, "22002", leftValue.Rows[0][0])
	require.Equal(t, "33003", rightValue.Rows[0][0])
	leftSeesRight, err := left.Query(ctx, "SELECT value FROM executor_rows WHERE id = 3", 1)
	require.NoError(t, err)
	rightSeesLeft, err := right.Query(ctx, "SELECT value FROM executor_rows WHERE id = 2", 1)
	require.NoError(t, err)
	require.Equal(t, "3", leftSeesRight.Rows[0][0])
	require.Equal(t, "2", rightSeesLeft.Rows[0][0])
	_, err = left.Execute(ctx, "ROLLBACK")
	require.NoError(t, err)
	_, err = right.Execute(ctx, "ROLLBACK")
	require.NoError(t, err)
	leftRolledBack, err := left.MysqlTransactionState()
	require.NoError(t, err)
	rightRolledBack, err := right.MysqlTransactionState()
	require.NoError(t, err)
	require.False(t, leftRolledBack.InTransaction)
	require.False(t, rightRolledBack.InTransaction)
	require.Zero(t, leftRolledBack.AffectedRows)
	require.Zero(t, rightRolledBack.AffectedRows)
	require.NoError(t, left.Close())
	require.NoError(t, right.Close())
}

func runConcurrentSessionUpdates(
	t *testing.T,
	ctx context.Context,
	left Session,
	leftSQL string,
	right Session,
	rightSQL string,
) {
	t.Helper()
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 2)
	for _, operation := range []struct {
		session Session
		sql     string
	}{
		{session: left, sql: leftSQL},
		{session: right, sql: rightSQL},
	} {
		wait.Add(1)
		go func(session Session, sqlText string) {
			defer wait.Done()
			result, err := session.Execute(ctx, sqlText)
			if err == nil && result.RowCount != 1 {
				err = fmt.Errorf("unexpected affected rows %d", result.RowCount)
			}
			errorsChannel <- err
		}(operation.session, operation.sql)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
}

func dockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// probeDockerAvailable only classifies client construction failures and an
// unmistakably unreachable daemon/socket as skippable. Once Ping succeeds,
// cleanup and every subsequent testcontainers error are test failures.
func probeDockerAvailable() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed
		}
	}()
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dockerClient, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err := dockerClient.Ping(probeContext); err != nil {
		_ = dockerClient.Close()
		return isDockerDaemonUnavailable(err), err
	}
	return false, dockerClient.Close()
}

func isDockerDaemonUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"docker daemon is not running",
		"connection refused",
		"no such file or directory",
		"the system cannot find the file specified",
		"open //./pipe/docker_engine",
		"open \\\\.\\pipe\\docker_engine",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
