package executor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgresExecutorE2E(t *testing.T) {
	ctx := dockerTestContext(t)
	const (
		database = "agentsql"
		username = "agentsql"
		password = "agentsql-password"
	)
	container, err := postgrescontainer.Run(
		ctx,
		"postgres:16",
		postgrescontainer.WithDatabase(database),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		skipDockerUnavailable(t, err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{
		ID:            "ds_postgres_e2e",
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

	t.Run("timeout interrupts query", func(t *testing.T) {
		shortDatasource := datasource
		shortDatasource.ID = "ds_postgres_timeout"
		shortDatasource.StmtTimeoutMS = 25
		shortExecutor, err := NewPostgresExecutor(ctx, shortDatasource, password, false)
		require.NoError(t, err)
		defer func() { require.NoError(t, shortExecutor.Close()) }()
		_, err = shortExecutor.Query(ctx, "SELECT pg_sleep(1)", 1)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrQueryTimeout))
	})

	t.Run("row limit truncates", func(t *testing.T) {
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
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		skipDockerUnavailable(t, err)
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
	})

	t.Run("row limit truncates", func(t *testing.T) {
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
}

func dockerTestContext(t *testing.T) context.Context {
	t.Helper()
	// testcontainers v0.37 在 Windows 上探测不到 Docker（或 DOCKER_HOST 指向
	// rootless/unix socket）时，NewDockerClient 内部可能直接 panic 而非返回 error，
	// 这里统一兜底：panic 与 error 都按"Docker 不可用"Skip，绝不让测试进程崩溃。
	if err := probeDockerAvailable(); err != nil {
		skipDockerUnavailable(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// probeDockerAvailable 探测本机 Docker 是否可用（NewDockerClient + Ping）。
// 把 testcontainers 内部 panic（Windows 无 Docker / rootless 不支持等）也转成
// 普通 error，供上层决定 t.Skip；client 具体类型在内部用短变量推断，不外泄。
func probeDockerAvailable() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic, treat as unavailable: %v", recovered)
		}
	}()
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dockerClient, err := testcontainers.NewDockerClient()
	if err != nil {
		return err
	}
	if _, err := dockerClient.Ping(probeContext); err != nil {
		_ = dockerClient.Close()
		return err
	}
	return dockerClient.Close()
}

func skipDockerUnavailable(t *testing.T, err error) {
	t.Helper()
	t.Logf("docker unavailable: %v", err)
	t.Skip("docker unavailable")
}
