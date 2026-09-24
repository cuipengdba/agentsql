package businessdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagerConcurrentReuseAndPoolIsolation(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	datasourceOne := encryptedDatasource(t, secret, "ds_one", "postgres")
	datasourceTwo := encryptedDatasource(t, secret, "ds_two", "mysql")
	var mutex sync.Mutex
	openCounts := make(map[string]int)
	opener := func(datasource model.Datasource, password string, readOnly bool) (Executor, error) {
		if password != "database-password" || !readOnly {
			return nil, fmt.Errorf("unexpected opener inputs")
		}
		mutex.Lock()
		defer mutex.Unlock()
		openCounts[datasource.ID]++
		return &fakeExecutor{dialect: datasource.DBType}, nil
	}
	manager := newManager(true, opener)

	const callers = 32
	results := make(chan Executor, callers)
	errorsChannel := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			executor, err := manager.GetOrOpen(datasourceOne, secret)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- executor
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	var first Executor
	for executor := range results {
		if first == nil {
			first = executor
		}
		require.Same(t, first, executor)
	}
	require.Equal(t, 1, openCounts[datasourceOne.ID])

	second, err := manager.GetOrOpen(datasourceTwo, secret)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, "postgres", first.Dialect())
	require.Equal(t, "mysql", second.Dialect())
	require.Equal(t, 1, openCounts[datasourceTwo.ID])
}

func TestManagerCloseAndCloseAll(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	datasourceOne := encryptedDatasource(t, secret, "ds_close_one", "postgres")
	datasourceTwo := encryptedDatasource(t, secret, "ds_close_two", "mysql")
	opened := make([]*fakeExecutor, 0)
	manager := newManager(false, func(datasource model.Datasource, _ string, _ bool) (Executor, error) {
		executor := &fakeExecutor{dialect: datasource.DBType}
		opened = append(opened, executor)
		return executor, nil
	})

	first, err := manager.GetOrOpen(datasourceOne, secret)
	require.NoError(t, err)
	_, err = manager.GetOrOpen(datasourceTwo, secret)
	require.NoError(t, err)
	require.NoError(t, manager.Close(datasourceOne.ID))
	require.Equal(t, 1, first.(*fakeExecutor).closeCount())

	reopened, err := manager.GetOrOpen(datasourceOne, secret)
	require.NoError(t, err)
	require.NotSame(t, first, reopened)
	require.NoError(t, manager.CloseAll())
	for _, executor := range opened {
		require.Equal(t, 1, executor.closeCount())
	}
	require.NoError(t, manager.Close("missing"))
}

func TestManagerFailsClosedAndRedactsCredentials(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	datasource := encryptedDatasource(t, secret, "ds_failure", "postgres")
	manager := newManager(false, func(model.Datasource, string, bool) (Executor, error) {
		return nil, errors.New("driver failure password=database-password")
	})

	_, err := manager.GetOrOpen(datasource, secret)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDatasourceUnreachable))
	require.NotContains(t, err.Error(), "database-password")
	require.NotContains(t, err.Error(), datasource.PasswordEnc)

	_, err = manager.GetOrOpen(datasource, []byte("short"))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDatasourceUnreachable))

	var typedNil *fakeExecutor
	nilManager := newManager(false, func(model.Datasource, string, bool) (Executor, error) {
		return typedNil, nil
	})
	_, err = nilManager.GetOrOpen(datasource, secret)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDatasourceUnreachable))
}

func TestCollectRowsNPlusOneTruncation(t *testing.T) {
	tests := []struct {
		name      string
		rows      [][]any
		limit     int
		count     int
		truncated bool
	}{
		{name: "under limit", rows: [][]any{{1, "a"}}, limit: 2, count: 1},
		{name: "at limit", rows: [][]any{{1, "a"}, {2, []byte("b")}}, limit: 2, count: 2},
		{name: "over limit", rows: [][]any{{1, "a"}, {2, "b"}, {3, "c"}}, limit: 2, count: 2, truncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeRowSource{columns: []string{"id", "name"}, rows: test.rows, index: -1}
			result, err := collectRows(source, test.limit)
			require.NoError(t, err)
			require.Equal(t, test.count, result.RowCount)
			require.Len(t, result.Rows, test.count)
			require.Equal(t, test.truncated, result.Truncated)
			require.True(t, source.closed)
		})
	}
}

func TestCollectRowsRejectsTypedNilSource(t *testing.T) {
	var source *fakeRowSource
	_, err := collectRows(source, 1)
	require.Error(t, err)
}

func TestReadOnlyExecuteDefense(t *testing.T) {
	_, err := (&PostgresExecutor{readOnly: true}).Execute(context.Background(), "UPDATE t SET a = 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReadOnlyViolated))

	_, err = (&MySQLExecutor{readOnly: true}).Execute(context.Background(), "UPDATE t SET a = 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReadOnlyViolated))

	_, err = (&MySQLExecutor{database: &sql.DB{}, readOnly: true}).Query(
		context.Background(),
		"UPDATE t SET a = 1",
		1,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReadOnlyViolated))
}

func TestExecutorSentinelsSupportErrorsIs(t *testing.T) {
	for _, sentinel := range []error{ErrQueryTimeout, ErrReadOnlyViolated, ErrDatasourceUnreachable} {
		wrapped := fmt.Errorf("outer: %w", safeError("operation", sentinel, errors.New("cause")))
		require.True(t, errors.Is(wrapped, sentinel))
	}
}

func encryptedDatasource(
	t *testing.T,
	secret []byte,
	id string,
	databaseType string,
) model.Datasource {
	t.Helper()
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt("database-password")
	require.NoError(t, err)
	port := 5432
	if databaseType == "mysql" {
		port = 3306
	}
	return model.Datasource{
		ID:            id,
		DBType:        databaseType,
		Host:          "127.0.0.1",
		Port:          port,
		Database:      "app",
		Username:      "agentsql",
		PasswordEnc:   encrypted,
		ConnLimit:     5,
		StmtTimeoutMS: 5000,
	}
}

type fakeExecutor struct {
	mu      sync.Mutex
	dialect string
	closed  int
}

func (executor *fakeExecutor) Dialect() string {
	return executor.dialect
}

func (*fakeExecutor) Ping(context.Context) error {
	return nil
}

func (*fakeExecutor) OpenSession(context.Context, string) (Session, error) {
	return nil, nil
}

func (*fakeExecutor) BeginWriteTx(context.Context) (WriteTx, error) {
	return nil, nil
}

func (*fakeExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{}, nil
}

func (*fakeExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{}, nil
}

func (*fakeExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, nil
}

func (executor *fakeExecutor) Close() error {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.closed++
	return nil
}

func (executor *fakeExecutor) closeCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.closed
}

type fakeRowSource struct {
	columns []string
	rows    [][]any
	index   int
	closed  bool
	err     error
}

func (source *fakeRowSource) Columns() ([]string, error) {
	return source.columns, nil
}

func (source *fakeRowSource) Next() bool {
	source.index++
	return source.index < len(source.rows)
}

func (source *fakeRowSource) Values() ([]any, error) {
	return source.rows[source.index], nil
}

func (source *fakeRowSource) Err() error {
	return source.err
}

func (source *fakeRowSource) Close() error {
	source.closed = true
	return nil
}

var _ Executor = (*fakeExecutor)(nil)
