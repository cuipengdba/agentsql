package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/eventbus"
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
	events, cancelEvents := runtime.Events.Subscribe()
	defer cancelEvents()
	response, err := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: plaintext, DatasourceID: "ds-1",
		SQL: "SELECT phone FROM public.customers WHERE id=1 LIMIT 1", MCPTool: "query",
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	require.Equal(t, 1, response.Redact.MaskedCells)
	require.Equal(t, 1, provider.calls())
	require.Equal(t, "allow", receiveBootstrapEvent(t, events).Audit.Decision)

	denied, err := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: plaintext, DatasourceID: "ds-1", SQL: "UPDATE public.orders SET total = 1",
		MCPTool: "execute_write", SessionID: "deny-session",
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision)
	require.Equal(t, "deny", receiveBootstrapEvent(t, events).Audit.Decision)

	approved, err := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: plaintext, DatasourceID: "ds-1",
		SQL: "SELECT phone FROM public.customers WHERE id=1 LIMIT 1", MCPTool: "request_approval",
		RequireApproval: true,
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionApprove, approved.Decision)
	require.Equal(t, "approve", receiveBootstrapEvent(t, events).Audit.Decision)
	select {
	case duplicate := <-events:
		t.Fatalf("unexpected duplicate event for audit %d", duplicate.Audit.ID)
	case <-time.After(20 * time.Millisecond):
	}
	require.NoError(t, runtime.Close())
}

func TestAssembleDisablesEventsWithConsoleOrStreamSwitch(t *testing.T) {
	for _, test := range []struct {
		name    string
		console bool
		stream  bool
	}{
		{name: "console disabled", console: false, stream: true},
		{name: "stream disabled", console: true, stream: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db"))
			cfg.Server.ConsoleEnabled = test.console
			cfg.Server.EventStream = test.stream
			runtime, err := Assemble(context.Background(), cfg, bootstrapTestSecret)
			require.NoError(t, err)
			require.Nil(t, runtime.Events)
			require.NoError(t, runtime.Close())
		})
	}
}

func TestAssembleMetadataSQLiteConfiguration(t *testing.T) {
	cfg := bootstrapTestConfig("")
	cfg.Store.Metadata = &config.MetadataStoreConfig{
		Driver:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "agentsql.db"),
	}
	runtime, err := Assemble(context.Background(), cfg, bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NoError(t, runtime.Store.Ping(context.Background()))
}

func TestAssembleDoesNotPublishWhenAuditPersistenceFails(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	events, cancel := runtime.Events.Subscribe()
	defer cancel()
	require.NoError(t, runtime.Store.Close())
	response, processErr := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: "asql_missing", DatasourceID: "missing", SQL: "SELECT 1", MCPTool: "query",
	})
	require.Error(t, processErr)
	require.Equal(t, model.DecisionDeny, response.Decision)
	select {
	case event := <-events:
		t.Fatalf("unexpected event after failed audit persistence: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
	require.NoError(t, runtime.Close())
}

func receiveBootstrapEvent(t *testing.T, events <-chan eventbus.Event) eventbus.Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persisted audit event")
		return eventbus.Event{}
	}
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
		ID: "valid-global", TableName: "customers", ColumnName: "identity",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask),
	})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(context.Background(), model.MaskRule{
		ID: "unsupported", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "customers", ColumnName: "identity",
		SensitiveType: string(mask.TypeIDCard), Algo: string(mask.AlgoMask),
	})
	require.NoError(t, err)
	_, err = runtime.redactors.RedactorFor(context.Background(), "ds-1")
	require.ErrorIs(t, err, mask.ErrUnsupportedType)
}

func TestRedactorBuilderMergesScopeAndLegacyDuplicates(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rules := []model.MaskRule{
		{ID: "global-contact", TableName: "users", ColumnName: "contact", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask)},
		{ID: "bound-contact", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "customers", ColumnName: `"CONTACT"`, SensitiveType: string(mask.TypeEmail), Algo: string(mask.AlgoMask)},
		{ID: "legacy-email", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "users", ColumnName: "legacy", SensitiveType: string(mask.TypeEmail), Algo: string(mask.AlgoMask)},
		{ID: "legacy-phone", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "orders", ColumnName: "LEGACY", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask)},
	}
	for _, rule := range rules {
		_, err := runtime.Store.MaskRules().Create(context.Background(), rule)
		require.NoError(t, err)
	}

	redactor, err := runtime.redactors.RedactorFor(context.Background(), "ds-1")
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"contact", "legacy"},
		Rows:    [][]string{{"user@example.com", "13812345678"}},
	})
	require.Equal(t, []string{"u***@example.com", "138****5678"}, result.Rows[0])
	require.Equal(t, mask.TypeEmail, report.TouchedColumns[0])
	require.Equal(t, mask.TypePhone, report.TouchedColumns[1])
}

func stringPointerBootstrap(value string) *string { return &value }

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
	t.Run("invalid metadata driver", func(t *testing.T) {
		cfg := bootstrapTestConfig("")
		cfg.Store.Metadata = &config.MetadataStoreConfig{Driver: "mysql"}
		_, err := Assemble(context.Background(), cfg, bootstrapTestSecret)
		require.ErrorContains(t, err, "unsupported dialect")
	})
	t.Run("postgres dsn missing", func(t *testing.T) {
		t.Setenv("AGENTSQL_STORE_METADATA_DSN", "")
		cfg := bootstrapTestConfig("")
		cfg.Store.Metadata = &config.MetadataStoreConfig{Driver: "postgres"}
		_, err := Assemble(context.Background(), cfg, bootstrapTestSecret)
		require.ErrorIs(t, err, config.ErrMissingPostgresDSN)
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
		Server: config.ServerConfig{HTTPListen: "127.0.0.1:7780", ConsoleEnabled: true, EventStream: true, EventStreamMaxConnections: 100},
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
