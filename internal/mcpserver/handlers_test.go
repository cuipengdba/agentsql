package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var mcpTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestHandlersListAuthorizedDatasourcesWithoutCredentials(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	response := fixture.handlers.listDatasources(context.Background())
	require.Equal(t, "allow", response.Decision)
	listed := response.Data.([]publicDatasource)
	require.Equal(t, []publicDatasource{{ID: "ds-allowed", Name: "Allowed", DBType: "postgres"}}, listed)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "db.internal")
	require.NotContains(t, string(encoded), "database-password")
	require.NotContains(t, string(encoded), "PasswordEnc")
}

func TestHandlersListSchemaFiltersColumnsAndRejectsIdentifiers(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	response := fixture.handlers.listSchema(context.Background(), "ds-allowed", "public.customers")
	require.Equal(t, "allow", response.Decision)
	columns := response.Data.([]publicSchemaColumn)
	require.Equal(t, []string{"id", "phone"}, []string{columns[0].Column, columns[1].Column})
	require.NotContains(t, columns, publicSchemaColumn{Schema: "public", Table: "customers", Column: "secret"})
	require.Contains(t, fixture.executor.lastQuery(), "table_schema = 'public'")
	require.Contains(t, fixture.executor.lastQuery(), "table_name = 'customers'")

	before := fixture.executor.snapshot()
	response = fixture.handlers.listSchema(context.Background(), "ds-allowed", "customers' OR 1=1--")
	require.Equal(t, "deny", response.Decision)
	require.Equal(t, before, fixture.executor.snapshot())
	response = fixture.handlers.listSchema(context.Background(), "ds-hidden", "customers")
	require.Equal(t, "deny", response.Decision)
}

func TestHandlersToolStatementGates(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	before := fixture.provider.calls()
	response := fixture.handlers.query(context.Background(), "ds-allowed", "UPDATE public.customers SET phone='x' WHERE id=1")
	require.Equal(t, "deny", response.Decision)
	require.Equal(t, before, fixture.provider.calls())
	response = fixture.handlers.executeWrite(context.Background(), "ds-allowed", "SELECT phone FROM public.customers", "read")
	require.Equal(t, "deny", response.Decision)
	require.Equal(t, before, fixture.provider.calls())
	response = fixture.handlers.executeWrite(context.Background(), "ds-allowed", "UPDATE public.customers SET phone='x' WHERE id=1", "")
	require.Equal(t, "error", response.Decision)
	require.Equal(t, before, fixture.provider.calls())
}

func TestHandlersReadonlyWriteIsRejectedByPipeline(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	response := fixture.handlers.executeWrite(
		context.Background(),
		"ds-allowed",
		"UPDATE public.customers SET phone='x' WHERE id=1",
		"correct one phone number",
	)
	require.Equal(t, "deny", response.Decision)
	require.Zero(t, fixture.provider.calls())
	require.Zero(t, fixture.executor.snapshot().execute)
}

func TestHandlersRequestApprovalRaisesWithoutExecuting(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	response := fixture.handlers.requestApproval(
		context.Background(),
		"ds-allowed",
		"SELECT phone FROM public.customers WHERE id=1 LIMIT 1",
		"DBA review requested",
	)
	require.Equal(t, "approve", response.Decision)
	data := response.Data.(pipelineData)
	require.NotEmpty(t, data.ApprovalID)
	require.Equal(t, "pending", data.Status)
	calls := fixture.executor.snapshot()
	require.Equal(t, 1, calls.explain)
	require.Zero(t, calls.query+calls.execute)
	approval, err := fixture.runtime.Store.Approvals().Get(context.Background(), data.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, fixture.agent.ID, *approval.AgentID)
}

func TestHandlersExplainOnlyAndQuery(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	explained := fixture.handlers.explainQuery(
		context.Background(),
		"ds-allowed",
		"SELECT phone FROM public.customers WHERE id=1 LIMIT 1",
	)
	require.Equal(t, "allow", explained.Decision)
	explainData := explained.Data.(pipelineData)
	require.Nil(t, explainData.Result)
	require.Equal(t, int64(1), explainData.EstScanRows)
	require.Zero(t, fixture.executor.snapshot().query)

	queried := fixture.handlers.query(
		context.Background(),
		"ds-allowed",
		"SELECT phone FROM public.customers WHERE id=1 LIMIT 1",
	)
	require.Equal(t, "allow", queried.Decision)
	queryData := queried.Data.(pipelineData)
	require.Equal(t, "138****5678", queryData.Result.Rows[0][0])
}

func TestHandlersFourCategoryResponseAuditAndLogsDoNotLeak(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	rawValues := []string{
		"11010519491231002X", "130503670401001", "4111111111111111",
		"4111 1111-1111 1111", "192.0.2.10/32", "198.51.100.0/24", "2000-02-29T00:00:00Z",
	}
	fixture.executor.mu.Lock()
	fixture.executor.queryResult = model.QueryResult{
		Columns: []string{"id_card", "legacy_id_card", "pan_plain", "pan_formatted", "client_ip", "ip_network", "birth_date"},
		Rows:    [][]string{rawValues}, RowCount: 1,
	}
	fixture.executor.mu.Unlock()
	var logs bytes.Buffer
	fixture.handlers.logger = zerolog.New(&logs)
	response := fixture.handlers.query(context.Background(), "ds-allowed",
		"SELECT id_card, legacy_id_card, pan_plain, pan_formatted, client_ip, ip_network, birth_date FROM public.customers WHERE id=1 LIMIT 1")
	require.Equal(t, "allow", response.Decision)
	data := response.Data.(pipelineData)
	require.Equal(t, []string{"110105********002X", "130503******001", "411111******1111", "411111******1111", "192.0.*.*", "198.51.*.*", "2000-**-**"}, data.Result.Rows[0])
	require.Equal(t, 7, data.Redact.MaskedCells)
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeIDCard, 1: mask.TypeIDCard, 2: mask.TypeBankCard, 3: mask.TypeBankCard, 4: mask.TypeIP, 5: mask.TypeIP, 6: mask.TypeBirthDate}, data.Redact.TouchedColumns)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	audits, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 10)
	require.NoError(t, err)
	auditJSON, err := json.Marshal(audits.List)
	require.NoError(t, err)
	for _, raw := range rawValues {
		require.NotContains(t, string(encoded), raw)
		require.NotContains(t, string(auditJSON), raw)
		require.NotContains(t, logs.String(), raw)
	}
}

func TestHandlersApprovalOwnershipAndErrorRedaction(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	owned, err := fixture.runtime.Store.Approvals().Create(context.Background(), model.Approval{
		ID: "owned", AgentID: stringPointerForMCP(fixture.agent.ID), Status: "pending",
	})
	require.NoError(t, err)
	response := fixture.handlers.getApprovalResult(context.Background(), owned.ID)
	require.Equal(t, "allow", response.Decision)
	_, err = fixture.runtime.Store.Approvals().Create(context.Background(), model.Approval{
		ID: "other", AgentID: stringPointerForMCP("other-agent"), Status: "pending",
	})
	require.NoError(t, err)
	response = fixture.handlers.getApprovalResult(context.Background(), "other")
	require.Equal(t, "deny", response.Decision)

	fixture.provider.err = errors.New("postgres://admin:database-password@db.internal app asql_secret")
	response = fixture.handlers.explainQuery(
		context.Background(),
		"ds-allowed",
		"SELECT phone FROM public.customers LIMIT 1",
	)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.Equal(t, "error", response.Decision)
	require.NotContains(t, string(encoded), "database-password")
	require.NotContains(t, string(encoded), "db.internal")
	require.NotContains(t, string(encoded), "asql_secret")
}

func TestHandlersNeverWriteProtocolStdout(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	fixture.handlers.logger = zerolog.New(&stderr)
	response := fixture.handlers.executeWrite(
		context.Background(),
		"ds-allowed",
		"UPDATE public.customers SET phone='x' WHERE id=1",
		"test stderr logging",
	)
	require.Equal(t, "allow", response.Decision)
	require.Empty(t, stdout.String())
	require.NotEmpty(t, stderr.String())
}

func TestHandlersAreRaceSafe(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	const count = 20
	var wait sync.WaitGroup
	responses := make(chan ToolResponse, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- fixture.handlers.listDatasources(context.Background())
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		require.Equal(t, "allow", response.Decision)
	}
}

type mcpFixture struct {
	runtime  *bootstrap.Runtime
	handlers *toolHandlers
	agent    model.Agent
	executor *mcpSpyExecutor
	provider *mcpExecutorProvider
}

func newMCPFixture(t *testing.T, level string) *mcpFixture {
	t.Helper()
	metadataStore, err := store.OpenWithSecret(context.Background(), filepath.Join(t.TempDir(), "agentsql.db"), mcpTestSecret)
	require.NoError(t, err)
	plaintext, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := metadataStore.Agents().Create(context.Background(), model.Agent{
		ID: "agent-1", Name: "Agent", Status: "active", APIKeyHash: hash, Level: level,
	})
	require.NoError(t, err)
	for _, datasource := range []model.Datasource{
		{ID: "ds-allowed", Name: "Allowed", DBType: "postgres", Host: "db.internal", Port: 5432, Database: "app", Username: "admin", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100},
		{ID: "ds-hidden", Name: "Hidden", DBType: "postgres", Host: "hidden.internal", Port: 5432, Database: "secret", Username: "admin", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100},
	} {
		_, err = metadataStore.Datasources().Create(context.Background(), datasource, "database-password")
		require.NoError(t, err)
	}
	columns := "id,phone,id_card,legacy_id_card,pan_plain,pan_formatted,client_ip,ip_network,birth_date"
	for _, stored := range []model.Policy{
		{ID: "table", AgentID: agent.ID, DatasourceID: "ds-allowed", ObjectType: "table", ObjectName: "public.customers", Action: "allow"},
		{ID: "columns", AgentID: agent.ID, DatasourceID: "ds-allowed", ObjectType: "column", ObjectName: "public.customers", Columns: &columns, Action: "allow"},
	} {
		_, err = metadataStore.Policies().Create(context.Background(), stored)
		require.NoError(t, err)
	}
	redactor, err := mask.NewRedactor([]mask.Rule{
		{Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Column: "id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "legacy_id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "pan_plain", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "pan_formatted", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "client_ip", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask},
		{Column: "ip_network", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask},
		{Column: "birth_date", SensitiveType: mask.TypeBirthDate, Algorithm: mask.AlgoMask},
	})
	require.NoError(t, err)
	spy := &mcpSpyExecutor{
		queryResult: model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1},
		schemaResult: model.QueryResult{
			Columns: []string{"table_schema", "table_name", "column_name"},
			Rows: [][]string{
				{"public", "customers", "id"},
				{"public", "customers", "phone"},
				{"public", "customers", "secret"},
			},
			RowCount: 3,
		},
	}
	provider := &mcpExecutorProvider{executor: spy}
	metricsHub := metrics.New(func() []metrics.PoolStat {
		return []metrics.PoolStat{
			{DatasourceID: "ds-allowed", Dialect: "postgres", MaxOpen: 5, InUse: 2, Idle: 3},
			{DatasourceID: "ds-hidden", Dialect: "postgres", MaxOpen: 4, InUse: 1, Idle: 3},
		}
	})
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(metadataStore.Agents()),
		Datasources:   metadataStore.Datasources(),
		Policies:      metadataStore.Policies(),
		Executors:     provider,
		Approvals:     metadataStore.Approvals(),
		Audit:         audit.NewRecorder(metadataStore.AuditLogs()),
		Redactors:     staticRedactorBuilder{redactor: redactor},
	}, mcpTestSecret, pipeline.WithObserver(metricsHub))
	require.NoError(t, err)
	runtime := &bootstrap.Runtime{
		Pipeline: flow, Executors: executor.NewManager(false), Store: metadataStore, Metrics: metricsHub,
	}
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	handlers := &toolHandlers{
		runtime:     runtime,
		agent:       agent,
		apiKey:      plaintext,
		logger:      zerolog.Nop(),
		executorFor: func(model.Datasource) (executor.Executor, error) { return spy, nil },
	}
	return &mcpFixture{runtime: runtime, handlers: handlers, agent: agent, executor: spy, provider: provider}
}

type staticRedactorBuilder struct{ redactor mask.Redactor }

func (builder staticRedactorBuilder) RedactorFor(context.Context, string) (mask.Redactor, error) {
	return builder.redactor, nil
}

type mcpExecutorProvider struct {
	mu                    sync.Mutex
	executor              executor.Executor
	executorsByDatasource map[string]executor.Executor
	err                   error
	count                 int
	callsByDatasource     map[string]int
}

func (provider *mcpExecutorProvider) GetOrOpen(datasource model.Datasource, _ []byte) (executor.Executor, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.count++
	if provider.callsByDatasource == nil {
		provider.callsByDatasource = make(map[string]int)
	}
	provider.callsByDatasource[datasource.ID]++
	if provider.executorsByDatasource != nil {
		selected, exists := provider.executorsByDatasource[datasource.ID]
		if !exists {
			return nil, errors.New("test executor not configured for datasource")
		}
		return selected, provider.err
	}
	return provider.executor, provider.err
}

func (provider *mcpExecutorProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.count
}

func (provider *mcpExecutorProvider) datasourceCalls(datasourceID string) int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.callsByDatasource[datasourceID]
}

type mcpExecutorCalls struct {
	explain int
	query   int
	execute int
}

type mcpSpyExecutor struct {
	mu           sync.Mutex
	calls        mcpExecutorCalls
	queryResult  model.QueryResult
	schemaResult model.QueryResult
	lastSQL      string
}

func (*mcpSpyExecutor) Dialect() string            { return "postgres" }
func (*mcpSpyExecutor) Ping(context.Context) error { return nil }
func (*mcpSpyExecutor) OpenSession(context.Context, string) (executor.Session, error) {
	return nil, errors.New("sessions are not used by MCP tests")
}
func (delegate *mcpSpyExecutor) BeginWriteTx(context.Context) (executor.WriteTx, error) {
	return mcpSpyWriteTx{delegate: delegate}, nil
}
func (executor *mcpSpyExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls.explain++
	return model.ExplainInfo{EstScanRows: 1, UsesIndex: true}, nil
}
func (executor *mcpSpyExecutor) Query(_ context.Context, sql string, _ int) (model.QueryResult, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls.query++
	executor.lastSQL = sql
	if strings.Contains(sql, "information_schema.columns") {
		return executor.schemaResult, nil
	}
	return executor.queryResult, nil
}
func (executor *mcpSpyExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls.execute++
	return model.QueryResult{RowCount: 1}, nil
}
func (*mcpSpyExecutor) Close() error                                { return nil }
func (*mcpSpyExecutor) TableHasIndex(string, string) (bool, error)  { return true, nil }
func (*mcpSpyExecutor) TableRowCount(string, string) (int64, error) { return 1, nil }
func (*mcpSpyExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}
func (*mcpSpyExecutor) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, nil
}
func (executor *mcpSpyExecutor) snapshot() mcpExecutorCalls {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}
func (executor *mcpSpyExecutor) lastQuery() string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.lastSQL
}

type mcpSpyWriteTx struct{ delegate *mcpSpyExecutor }

func (tx mcpSpyWriteTx) Execute(ctx context.Context, sql string) (model.QueryResult, error) {
	return tx.delegate.Execute(ctx, sql)
}
func (mcpSpyWriteTx) Commit(context.Context) error   { return nil }
func (mcpSpyWriteTx) Rollback(context.Context) error { return nil }

func stringPointerForMCP(value string) *string { return &value }

var (
	_ pipeline.ExecutorProvider              = (*mcpExecutorProvider)(nil)
	_ pipeline.RedactorBuilder               = staticRedactorBuilder{}
	_ executor.Executor                      = (*mcpSpyExecutor)(nil)
	_ rules.TransactionMetadataProvider      = (*mcpSpyExecutor)(nil)
	_ rules.MysqlTransactionMetadataProvider = (*mcpSpyExecutor)(nil)
)
