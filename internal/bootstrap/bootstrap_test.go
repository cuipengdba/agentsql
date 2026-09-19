package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
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
		Enabled:       true,
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

func TestAssembleKeepsInternalEventsWithoutSSESubscription(t *testing.T) {
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
			require.NotNil(t, runtime.Events)
			require.Zero(t, runtime.Events.SubscriberCount())
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

func TestAssembleLoadsNotificationsAndDeliversPersistedDeny(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentsql.db")
	received := make(chan notify.Payload, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload notify.Payload
		if json.NewDecoder(request.Body).Decode(&payload) == nil {
			received <- payload
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	seed, err := store.OpenWithSecret(context.Background(), path, bootstrapTestSecret)
	require.NoError(t, err)
	plaintext, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = seed.Agents().Create(context.Background(), model.Agent{
		ID: "notify-agent", Name: "Notification Agent", Status: "active", APIKeyHash: hash, Level: "dml",
	})
	require.NoError(t, err)
	_, err = seed.Datasources().Create(context.Background(), model.Datasource{
		ID: "notify-ds", Name: "Notification Database", DBType: "postgres", Host: "db.internal",
		Port: 5432, Database: "app", Username: "user", ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 100,
	}, "database-password")
	require.NoError(t, err)
	require.NoError(t, seed.Notifications().Replace(context.Background(), notify.Config{
		Enabled: true, QueueSize: 4,
		Channels: []notify.ChannelConfig{{
			ID: "startup", Enabled: true, Kind: notify.ChannelWebhook,
			Decisions: []string{"deny"}, AllowPrivateEndpoints: true,
			Webhook: &notify.WebhookConfig{Template: notify.WebhookGeneric, URL: receiver.URL},
		}},
	}))
	require.NoError(t, seed.Close())

	runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	response, _ := runtime.Pipeline.Process(context.Background(), pipeline.Request{
		APIKey: plaintext, DatasourceID: "notify-ds", SQL: "SELECT secret FROM private_table", MCPTool: "query",
	})
	require.Equal(t, model.DecisionDeny, response.Decision)
	select {
	case payload := <-received:
		require.Equal(t, "deny", payload.Decision)
		require.Equal(t, "notify-agent", payload.Agent.ID)
		require.Equal(t, "Notification Agent", payload.Agent.Name)
		require.Equal(t, "notify-ds", payload.Datasource.ID)
		require.Equal(t, "Notification Database", payload.Datasource.Name)
		require.Nil(t, payload.SQLNorm)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for startup notification")
	}
	require.Eventually(t, func() bool {
		return runtime.Notifications.Status()["startup"].Sent == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	require.Zero(t, runtime.Events.SubscriberCount())
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
func (delegate *bootstrapExecutor) BeginWriteTx(context.Context) (executor.WriteTx, error) {
	return bootstrapWriteTx{delegate: delegate}, nil
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

type bootstrapWriteTx struct{ delegate *bootstrapExecutor }

func (tx bootstrapWriteTx) Execute(ctx context.Context, sql string) (model.QueryResult, error) {
	return tx.delegate.Execute(ctx, sql)
}
func (bootstrapWriteTx) Commit(context.Context) error   { return nil }
func (bootstrapWriteTx) Rollback(context.Context) error { return nil }

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
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true,
	})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(context.Background(), model.MaskRule{
		ID: "unsupported", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "customers", ColumnName: "identity",
		SensitiveType: "future", Algo: string(mask.AlgoMask), Enabled: true,
	})
	require.NoError(t, err)
	_, err = runtime.redactors.RedactorFor(context.Background(), "ds-1")
	require.ErrorIs(t, err, mask.ErrUnsupportedType)
}

type staticMaskRuleReader struct {
	rules []model.MaskRule
}

func (reader staticMaskRuleReader) ListEnabledByDatasource(context.Context, string) ([]model.MaskRule, error) {
	return append([]model.MaskRule(nil), reader.rules...), nil
}

func TestRedactorBuilderRejectsDuplicateColumnsWithinOneScope(t *testing.T) {
	types := []mask.SensitiveType{mask.TypeBirthDate, mask.TypeIP, mask.TypeBankCard, mask.TypeIDCard, mask.TypeEmail, mask.TypePhone}
	stored := make([]model.MaskRule, 0, len(types))
	for index, sensitiveType := range types {
		stored = append(stored, model.MaskRule{ID: fmt.Sprintf("rule-%d", index), ColumnName: "shared", SensitiveType: string(sensitiveType), Algo: string(mask.AlgoMask), Enabled: true})
	}
	_, err := (&redactorBuilder{repository: staticMaskRuleReader{rules: stored}}).RedactorFor(context.Background(), "ds-1")
	require.ErrorIs(t, err, mask.ErrDuplicateMaskColumn)
}

func TestRedactorBuilderPrefersDatasourceScope(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rules := []model.MaskRule{
		{ID: "global-contact", TableName: "users", ColumnName: "contact", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true},
		{ID: "bound-contact", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "customers", ColumnName: `"CONTACT"`, SensitiveType: string(mask.TypeEmail), Algo: string(mask.AlgoMask), Enabled: true},
		{ID: "bound-legacy", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "users", ColumnName: "legacy", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true},
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

func TestRedactorBuilderDatasourceMaskOverridesGlobalHash(t *testing.T) {
	key := []byte("hash-key-0123456789abcdef01234567")
	builder := &redactorBuilder{
		repository: staticMaskRuleReader{rules: []model.MaskRule{
			{ID: "global", ColumnName: "contact", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoHash), Enabled: true},
			{ID: "scoped", DatasourceID: stringPointerBootstrap("ds-1"), ColumnName: "contact", SensitiveType: string(mask.TypeEmail), Algo: string(mask.AlgoMask), Enabled: true},
		}},
		options:       []mask.Option{mask.WithHashKey(key)},
		hashAvailable: true,
	}
	redactor, err := builder.RedactorFor(context.Background(), "ds-1")
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{Columns: []string{"contact"}, Rows: [][]string{{"user@example.com"}}})
	require.Equal(t, "u***@example.com", result.Rows[0][0])
	require.Equal(t, mask.TypeEmail, report.TouchedColumns[0])
}

func TestRedactorBuilderBlockDoesNotRequireHashKey(t *testing.T) {
	t.Run("request compile succeeds without key", func(t *testing.T) {
		builder := &redactorBuilder{repository: staticMaskRuleReader{rules: []model.MaskRule{{
			ID: "block", ColumnName: "secret", SensitiveType: string(mask.TypeGeneric),
			Algo: string(mask.AlgoBlock), Enabled: true,
		}}}}
		redactor, err := builder.RedactorFor(context.Background(), "ds-1")
		require.NoError(t, err)
		result, report := redactor.Apply(model.QueryResult{
			Columns: []string{"secret"}, Rows: [][]string{{"raw-secret"}},
		})
		require.Equal(t, mask.BlockPlaceholder, result.Rows[0][0])
		require.Equal(t, 1, report.MaskedCells)
	})

	t.Run("startup scan succeeds without key", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "block.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{
			ID: "block", ColumnName: "secret", SensitiveType: string(mask.TypeGeneric),
			Algo: string(mask.AlgoBlock), Enabled: true,
		})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.NoError(t, err)
		require.NoError(t, runtime.Close())
	})

	t.Run("enabled hash still requires key when mixed with block", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "block-hash.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{
			ID: "block", ColumnName: "secret", SensitiveType: string(mask.TypeGeneric),
			Algo: string(mask.AlgoBlock), Enabled: true,
		})
		seedBootstrapMaskRule(t, path, model.MaskRule{
			ID: "hash", ColumnName: "name", SensitiveType: string(mask.TypeGeneric),
			Algo: string(mask.AlgoHash), Enabled: true,
		})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.Nil(t, runtime)
		require.ErrorIs(t, err, mask.ErrHashKeyRequired)
	})
}

func TestAssembleRedactionStartupMatrixSQLite(t *testing.T) {
	t.Run("enabled hash without key fails and closes store", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "enabled.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{ID: "hash", ColumnName: "phone", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoHash), Enabled: true})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.Nil(t, runtime)
		require.ErrorIs(t, err, mask.ErrHashKeyRequired)
		require.ErrorContains(t, err, "set AGENTSQL_REDACTION_HASH_KEY or redaction.hash_key")
		renamed := path + ".closed"
		require.NoError(t, os.Rename(path, renamed), "failed assembly must close the opened SQLite store")
	})

	t.Run("disabled hash without key starts", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "disabled.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{ID: "hash", ColumnName: "phone", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoHash), Enabled: false})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.NoError(t, err)
		require.NoError(t, runtime.Close())
	})

	t.Run("same normalized column in different datasource scopes starts", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "scopes.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{ID: "ds-1-phone", DatasourceID: stringPointerBootstrap("ds-1"), ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
		seedBootstrapMaskRule(t, path, model.MaskRule{ID: "ds-2-phone", DatasourceID: stringPointerBootstrap("ds-2"), ColumnName: "PHONE", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.NoError(t, err)
		require.NoError(t, runtime.Close())
	})

	t.Run("short key fails without hash rules", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "tiny-secret-T41")
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "short.db")), bootstrapTestSecret)
		require.Nil(t, runtime)
		require.ErrorIs(t, err, config.ErrInvalidRedactionHashKey)
		require.NotContains(t, err.Error(), "tiny-secret-T41")
	})

	t.Run("valid key starts and hashes query result", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "hash-key-0123456789abcdef01234567")
		path := filepath.Join(t.TempDir(), "valid.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{ID: "hash", ColumnName: "phone", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoHash), Enabled: true})
		provider := &bootstrapExecutorProvider{executor: &bootstrapExecutor{}}
		runtime, err := assembleWithExecutorProvider(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret, provider)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, runtime.Close()) })
		seedBootstrapPipelineIdentity(t, runtime)
		response, err := runtime.Pipeline.Process(context.Background(), pipeline.Request{APIKey: bootstrapPipelineAPIKey, DatasourceID: "ds-1", SQL: "SELECT phone FROM public.customers WHERE id=1 LIMIT 1", MCPTool: "query"})
		require.NoError(t, err)
		require.Regexp(t, regexp.MustCompile(`^h\.[0-9a-f]{32}$`), response.Result.Rows[0][0])
		require.NotEqual(t, "13812345678", response.Result.Rows[0][0])
	})
}

const bootstrapPipelineAPIKey = "asql_bootstrap_hash"

func seedBootstrapMaskRule(t *testing.T, path string, rule model.MaskRule) {
	t.Helper()
	opened, err := store.OpenWithSecret(context.Background(), path, bootstrapTestSecret)
	require.NoError(t, err)
	_, err = opened.MaskRules().Create(context.Background(), rule)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
}

func seedBootstrapPipelineIdentity(t *testing.T, runtime *Runtime) {
	t.Helper()
	hash := store.HashAPIKey(bootstrapPipelineAPIKey)
	_, err := runtime.Store.Agents().Create(context.Background(), model.Agent{ID: "agent-hash", Name: "Agent", Status: "active", APIKeyHash: hash, Level: "dml"})
	require.NoError(t, err)
	_, err = runtime.Store.Datasources().Create(context.Background(), model.Datasource{ID: "ds-1", Name: "DB", DBType: "postgres", Host: "127.0.0.1", Port: 5432, Database: "app", Username: "agentsql", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100}, "password")
	require.NoError(t, err)
	_, err = runtime.Store.Policies().Create(context.Background(), model.Policy{ID: "policy-hash", AgentID: "agent-hash", DatasourceID: "ds-1", ObjectType: "table", ObjectName: "public.customers", Action: "allow"})
	require.NoError(t, err)
}

func TestRedactorBuilderIgnoresDisabledDraftsButManagementListsThem(t *testing.T) {
	runtime, err := Assemble(context.Background(), bootstrapTestConfig(filepath.Join(t.TempDir(), "agentsql.db")), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	for _, rule := range []model.MaskRule{
		{ID: "enabled-phone", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "users", ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true},
		{ID: "disabled-email", DatasourceID: stringPointerBootstrap("ds-1"), TableName: "users", ColumnName: "email", SensitiveType: string(mask.TypeEmail), Algo: string(mask.AlgoMask), Enabled: false},
	} {
		_, err := runtime.Store.MaskRules().Create(context.Background(), rule)
		require.NoError(t, err)
	}

	listed, err := runtime.Store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, 1, countDisabledMaskRules(listed))

	redactor, err := runtime.redactors.RedactorFor(context.Background(), "ds-1")
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"phone", "email"},
		Rows:    [][]string{{"13812345678", "user@example.com"}},
	})
	require.Equal(t, []string{"138****5678", "user@example.com"}, result.Rows[0])
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, report.TouchedColumns)
}

func countDisabledMaskRules(rules []model.MaskRule) int {
	count := 0
	for _, rule := range rules {
		if !rule.Enabled {
			count++
		}
	}
	return count
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
