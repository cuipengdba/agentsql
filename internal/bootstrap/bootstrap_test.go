package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

var bootstrapTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestAssembleWiresRuntimeAndStoreBackedRedactor(t *testing.T) {
	provider := &bootstrapExecutorProvider{executor: &bootstrapExecutor{}}
	runtime, err := assembleWithExecutorProvider(
		context.Background(),
		bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")),
		bootstrapTestSecret,
		provider,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NotNil(t, runtime.Pipeline)
	require.NotNil(t, runtime.Executors)
	require.NotNil(t, runtime.Store)
	require.NotNil(t, runtime.Store.Rules())
	var overrideReader pipeline.RuleOverrideReader = runtime.Store.Rules()
	require.NotNil(t, overrideReader)

	plaintext, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = runtime.Store.Agents().Create(context.Background(), model.Agent{
		ID: "agent-1", Name: "Agent", Status: "active", APIKeyHash: hash, Level: "dml",
	})
	require.NoError(t, err)
	_, err = runtime.Store.Datasources().Create(context.Background(), model.Datasource{
		ID: "ds-1", Name: "DB", DBType: "postgres", Host: "127.0.0.1", Port: 5432,
		Database: "app", Username: "agentsql", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "password")
	require.NoError(t, err)
	_, err = runtime.Store.Policies().Create(context.Background(), model.Policy{
		ID: "policy-1", AgentID: "agent-1", DatasourceID: "ds-1",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(context.Background(), model.MaskRule{
		ID:            "global-phone",
		TableName:     "customers",
		ColumnName:    "phone",
		SensitiveType: string(mask.TypePhone),
		Algo:          string(mask.AlgoMask),
	})
	require.NoError(t, err)
	response, err := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: plaintext, DatasourceID: "ds-1",
		SQL: "SELECT phone FROM public.customers WHERE id=1 LIMIT 1", MCPTool: "query",
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	require.Equal(t, 1, response.Redact.MaskedCells)
	require.Equal(t, 1, provider.calls())
	require.NoError(t, runtime.Close())
}

type bootstrapExecutorProvider struct {
	mu       sync.Mutex
	executor executor.Executor
	count    int
}

func (provider *bootstrapExecutorProvider) GetOrOpen(model.Datasource, []byte) (executor.Executor, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.count++
	return provider.executor, nil
}

func (provider *bootstrapExecutorProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.count
}

type bootstrapExecutor struct{}

func (*bootstrapExecutor) Dialect() string            { return "postgres" }
func (*bootstrapExecutor) Ping(context.Context) error { return nil }
func (*bootstrapExecutor) OpenSession(context.Context, string) (executor.Session, error) {
	return nil, errors.New("session not configured")
}
func (*bootstrapExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{EstScanRows: 1, UsesIndex: true}, nil
}
func (*bootstrapExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1}, nil
}
func (*bootstrapExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{RowCount: 1}, nil
}
func (*bootstrapExecutor) Close() error                                { return nil }
func (*bootstrapExecutor) TableHasIndex(string, string) (bool, error)  { return true, nil }
func (*bootstrapExecutor) TableRowCount(string, string) (int64, error) { return 1, nil }
func (*bootstrapExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}

var (
	_ pipeline.ExecutorProvider         = (*bootstrapExecutorProvider)(nil)
	_ executor.Executor                 = (*bootstrapExecutor)(nil)
	_ rules.TransactionMetadataProvider = (*bootstrapExecutor)(nil)
)

func TestRedactorBuilderFailsClosedForUnsupportedRules(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	_, err = runtime.Store.MaskRules().Create(context.Background(), model.MaskRule{
		ID: "unsupported", TableName: "customers", ColumnName: "identity",
		SensitiveType: string(mask.TypeIDCard), Algo: string(mask.AlgoMask),
	})
	require.NoError(t, err)
	_, err = runtime.redactors.RedactorFor(context.Background(), "ds-1")
	require.ErrorIs(t, err, mask.ErrUnsupportedType)
}

func TestAssembleFailsClosed(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		_, err := Assemble(nil, bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
		require.Error(t, err)
	})
	t.Run("invalid secret", func(t *testing.T) {
		_, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), []byte("short"))
		require.Error(t, err)
	})
	t.Run("invalid config", func(t *testing.T) {
		cfg := bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db"))
		cfg.Store.SQLitePath = ""
		_, err := Assemble(context.Background(), cfg, bootstrapTestSecret)
		require.Error(t, err)
	})
	t.Run("store path is directory", func(t *testing.T) {
		_, err := Assemble(context.Background(), bootstrapTestConfig(t.TempDir()), bootstrapTestSecret)
		require.Error(t, err)
	})
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	_, err = runtime.ExecutorFor(model.Datasource{})
	require.Error(t, err)
	require.False(t, errors.Is(err, context.Canceled))
}

func bootstrapTestConfig(path string) config.Config {
	return config.Config{
		Server: config.ServerConfig{HTTPListen: "127.0.0.1:7780"},
		Store:  config.StoreConfig{SQLitePath: path},
		Defaults: config.DefaultsConfig{
			StatementTimeoutMS:    5_000,
			RowLimit:              1_000,
			MaxConnsPerDatasource: 5,
			QPSPerAgent:           20,
		},
		Theme: config.ThemeConfig{Default: "dark"},
	}
}
