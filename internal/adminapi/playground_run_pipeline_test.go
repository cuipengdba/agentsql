package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPlaygroundRunRealPipelineSelectAndDenyPersistFixedIdentityAudits(t *testing.T) {
	metadata, err := store.OpenWithSecret(
		context.Background(), filepath.Join(t.TempDir(), "playground-run.db"), []byte(adminTestSecret),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metadata.Close()) })

	provider := &playgroundPipelineExecutorProvider{executor: &playgroundPipelineExecutor{}}
	redactor, err := mask.NewRedactor([]mask.Rule{
		{Column: "id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "legacy_id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "pan_plain", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "pan_formatted", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "client_ip", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask},
		{Column: "ip_network", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask},
		{Column: "birth_date", SensitiveType: mask.TypeBirthDate, Algorithm: mask.AlgoMask},
	})
	require.NoError(t, err)
	redactorBuilder := &playgroundPipelineRedactorBuilder{redactor: redactor}
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: &playgroundPipelineAuthenticator{},
		Datasources:   playgroundPipelineDatasourceReader{},
		Policies:      playgroundPipelinePolicyLoader{},
		Executors:     provider,
		Approvals:     playgroundPipelineApprovalWriter{},
		Audit:         audit.NewRecorder(metadata.AuditLogs()),
		Redactors:     redactorBuilder,
	}, []byte(adminTestSecret), pipeline.WithDemoConfig(config.DemoConfig{
		Enabled: true, AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL}, QPSPerAgent: 5,
	}))
	require.NoError(t, err)

	cfg := adminTestConfig(filepath.Join(t.TempDir(), "unused.db"))
	cfg.Demo = config.DemoConfig{
		Enabled: true, AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL}, QPSPerAgent: 5,
	}
	var logs bytes.Buffer
	handler, err := NewHandler(Deps{
		Runtime: &bootstrap.Runtime{Pipeline: flow, Store: metadata}, Config: cfg,
		AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
		demoRunner: flow, demoKeys: demoProfileKeys{ro: demoROKeyCanary, dml: demoDMLKeyCanary},
	}, zerolog.New(&logs))
	require.NoError(t, err)
	fixture := &adminFixture{store: metadata, handler: handler, adminToken: newAdminTestToken(t)}

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT id_card, legacy_id_card, pan_plain, pan_formatted, client_ip, ip_network, birth_date FROM public.customers WHERE id = 1 LIMIT 5","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusOK, status, body)
	var selected struct {
		Data playgroundRunView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &selected))
	require.Equal(t, "allow", selected.Data.Decision)
	require.Equal(t, [][]string{{"110105********002X", "130503******001", "411111******1111", "411111******1111", "192.0.*.*", "198.51.*.*", "2000-**-**"}}, selected.Data.Result.Rows)
	require.True(t, selected.Data.Result.Truncated)
	require.Equal(t, 7, selected.Data.Redact.MaskedCells)
	require.Equal(t, map[int]string{0: string(mask.TypeIDCard), 1: string(mask.TypeIDCard), 2: string(mask.TypeBankCard), 3: string(mask.TypeBankCard), 4: string(mask.TypeIP), 5: string(mask.TypeIP), 6: string(mask.TypeBirthDate)}, selected.Data.Redact.TouchedColumns)
	require.Positive(t, selected.Data.AuditID)
	require.Equal(t, 2, provider.executor.(*playgroundPipelineExecutor).lastRowLimit())
	selectAudit := latestPlaygroundPipelineAudit(t, metadata)
	require.Equal(t, config.DemoAgentRO, requirePlaygroundAuditAgent(t, selectAudit))
	selectAuditJSON, err := json.Marshal(selectAudit)
	require.NoError(t, err)
	for _, raw := range []string{"11010519491231002X", "130503670401001", "4111111111111111", "4111 1111-1111 1111", "192.0.2.10/32", "198.51.100.0/24", "2000-02-29T00:00:00Z"} {
		require.NotContains(t, body, raw)
		require.NotContains(t, string(selectAuditJSON), raw)
		require.NotContains(t, logs.String(), raw)
	}
	assertPlaygroundBodyHasNoSecrets(t, body)

	const hashRaw = "T41_PLAYGROUND_RAW_SENTINEL_b7a3"
	hashRedactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "customer_name", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash,
	}}, mask.WithHashKey([]byte("playground-hash-key-0123456789abcdef")))
	require.NoError(t, err)
	redactorBuilder.redactor = hashRedactor
	provider.executor.(*playgroundPipelineExecutor).setResult(model.QueryResult{
		Columns: []string{"customer_name"}, Rows: [][]string{{hashRaw}}, RowCount: 1,
	})
	status, body = fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT customer_name FROM public.customers WHERE id = 1 LIMIT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusOK, status, body)
	var hashed struct {
		Data playgroundRunView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &hashed))
	expectedHash, _ := hashRedactor.Apply(model.QueryResult{Columns: []string{"customer_name"}, Rows: [][]string{{hashRaw}}})
	require.Equal(t, expectedHash.Rows[0][0], hashed.Data.Result.Rows[0][0])
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, hashed.Data.Result.Rows[0][0])
	require.NotContains(t, body, hashRaw)
	hashAuditJSON, err := json.Marshal(latestPlaygroundPipelineAudit(t, metadata))
	require.NoError(t, err)
	require.NotContains(t, string(hashAuditJSON), hashRaw)
	require.NotContains(t, logs.String(), hashRaw)

	beforeDenyExecutorCalls := provider.calls()
	status, body = fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"UPDATE public.orders SET status = 'cancelled'","datasource_id":"ds-demo-pg","agent_profile":"dml"}`)
	require.Equal(t, http.StatusOK, status, body)
	var denied struct {
		Data playgroundRunView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &denied))
	require.Equal(t, "deny", denied.Data.Decision)
	require.Contains(t, playgroundRunHitIDs(denied.Data.Assessment.Hits), "R002")
	require.Contains(t, playgroundRunHitIDs(denied.Data.Assessment.Hits), "DEMO_NON_SELECT")
	require.Empty(t, denied.Data.Result.Columns)
	require.Empty(t, denied.Data.Result.Rows)
	require.Positive(t, denied.Data.AuditID)
	require.Equal(t, beforeDenyExecutorCalls, provider.calls(), "denied demo SQL must not touch the business executor")
	denyAudit := latestPlaygroundPipelineAudit(t, metadata)
	require.Equal(t, config.DemoAgentDML, requirePlaygroundAuditAgent(t, denyAudit))
	assertPlaygroundBodyHasNoSecrets(t, body)
	assertPlaygroundBodyHasNoSecrets(t, logs.String())
}

func TestPlaygroundRunBlockResponseAndAuditDoNotLeakRawSentinel(t *testing.T) {
	const blockRaw = "T42_PLAYGROUND_BLOCK_RAW_SENTINEL_6d19"
	metadata, err := store.OpenWithSecret(
		context.Background(), filepath.Join(t.TempDir(), "playground-block.db"), []byte(adminTestSecret),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metadata.Close()) })

	provider := &playgroundPipelineExecutorProvider{executor: &playgroundPipelineExecutor{}}
	provider.executor.(*playgroundPipelineExecutor).setResult(model.QueryResult{
		Columns: []string{"secret"}, Rows: [][]string{{blockRaw}}, RowCount: 1,
	})
	blockRedactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock,
	}})
	require.NoError(t, err)
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: &playgroundPipelineAuthenticator{},
		Datasources:   playgroundPipelineDatasourceReader{},
		Policies:      playgroundPipelinePolicyLoader{},
		Executors:     provider,
		Approvals:     playgroundPipelineApprovalWriter{},
		Audit:         audit.NewRecorder(metadata.AuditLogs()),
		Redactors:     &playgroundPipelineRedactorBuilder{redactor: blockRedactor},
	}, []byte(adminTestSecret), pipeline.WithDemoConfig(config.DemoConfig{
		Enabled: true, AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL}, QPSPerAgent: 5,
	}))
	require.NoError(t, err)

	cfg := adminTestConfig(filepath.Join(t.TempDir(), "unused.db"))
	cfg.Demo = config.DemoConfig{
		Enabled: true, AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL}, QPSPerAgent: 5,
	}
	var logs bytes.Buffer
	handler, err := NewHandler(Deps{
		Runtime: &bootstrap.Runtime{Pipeline: flow, Store: metadata}, Config: cfg,
		AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
		demoRunner: flow, demoKeys: demoProfileKeys{ro: demoROKeyCanary, dml: demoDMLKeyCanary},
	}, zerolog.New(&logs))
	require.NoError(t, err)
	fixture := &adminFixture{store: metadata, handler: handler, adminToken: newAdminTestToken(t)}

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT secret FROM public.customers WHERE id = 1 LIMIT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusOK, status, body)
	var blocked struct {
		Data playgroundRunView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &blocked))
	require.Equal(t, [][]string{{mask.BlockPlaceholder}}, blocked.Data.Result.Rows)
	require.Equal(t, map[int]string{0: string(mask.TypeGeneric)}, blocked.Data.Redact.TouchedColumns)
	require.Equal(t, 1, blocked.Data.Redact.MaskedCells)
	require.NotContains(t, body, blockRaw)
	blockAuditJSON, err := json.Marshal(latestPlaygroundPipelineAudit(t, metadata))
	require.NoError(t, err)
	require.NotContains(t, string(blockAuditJSON), blockRaw)
	require.NotContains(t, logs.String(), blockRaw)
}

func newAdminTestToken(t *testing.T) string {
	t.Helper()
	token, _, err := issueAdminToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(), "playground-pipeline")
	require.NoError(t, err)
	return "Bearer " + token
}

type playgroundPipelineAuthenticator struct{}

func (*playgroundPipelineAuthenticator) Authenticate(_ context.Context, key string) (model.Agent, error) {
	switch key {
	case demoROKeyCanary:
		return model.Agent{ID: config.DemoAgentRO, Level: "readonly", Status: "active"}, nil
	case demoDMLKeyCanary:
		return model.Agent{ID: config.DemoAgentDML, Level: "dml", Status: "active"}, nil
	default:
		return model.Agent{}, errors.New("invalid demo credential")
	}
}

type playgroundPipelineDatasourceReader struct{}

func (playgroundPipelineDatasourceReader) Get(_ context.Context, id string) (model.Datasource, error) {
	if id != config.DemoDatasourcePG {
		return model.Datasource{}, errors.New("unknown demo datasource")
	}
	return model.Datasource{
		ID: id, DBType: "postgres", RowLimit: 2, StmtTimeoutMS: 1_500,
	}, nil
}

type playgroundPipelinePolicyLoader struct{}

func (playgroundPipelinePolicyLoader) ListByAgentAndDatasource(
	_ context.Context,
	agentID string,
	datasourceID string,
) ([]model.Policy, error) {
	return []model.Policy{{
		ID: "demo-policy-" + agentID, AgentID: agentID, DatasourceID: datasourceID,
		ObjectType: "table", ObjectName: "*", Action: "allow",
	}}, nil
}

type playgroundPipelineExecutorProvider struct {
	mu       sync.Mutex
	executor executor.Statement
	count    int
}

func (provider *playgroundPipelineExecutorProvider) AuthorizedExecute(context.Context, model.Datasource, []byte, string, string) (executor.Statement, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.count++
	return provider.executor, nil
}

func (provider *playgroundPipelineExecutorProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.count
}

type playgroundPipelineExecutor struct {
	mu       sync.Mutex
	rowLimit int
	result   *model.QueryResult
}

func (*playgroundPipelineExecutor) Dialect() string { return "postgres" }
func (*playgroundPipelineExecutor) Explain(context.Context) (model.ExplainInfo, error) {
	return model.ExplainInfo{EstScanRows: 1, UsesIndex: true}, nil
}
func (*playgroundPipelineExecutor) TableHasIndex(string, string) (bool, error)  { return true, nil }
func (*playgroundPipelineExecutor) TableRowCount(string, string) (int64, error) { return 1, nil }
func (*playgroundPipelineExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}
func (demoExecutor *playgroundPipelineExecutor) Query(
	_ context.Context,
	rowLimit int,
) (model.QueryResult, error) {
	demoExecutor.mu.Lock()
	demoExecutor.rowLimit = rowLimit
	if demoExecutor.result != nil {
		result := *demoExecutor.result
		result.Columns = append([]string(nil), demoExecutor.result.Columns...)
		result.Rows = make([][]string, len(demoExecutor.result.Rows))
		for index := range demoExecutor.result.Rows {
			result.Rows[index] = append([]string(nil), demoExecutor.result.Rows[index]...)
		}
		demoExecutor.mu.Unlock()
		return result, nil
	}
	demoExecutor.mu.Unlock()
	return model.QueryResult{
		Columns: []string{"id_card", "legacy_id_card", "pan_plain", "pan_formatted", "client_ip", "ip_network", "birth_date"},
		Rows: [][]string{{
			"11010519491231002X", "130503670401001", "4111111111111111", "4111 1111-1111 1111",
			"192.0.2.10/32", "198.51.100.0/24", "2000-02-29T00:00:00Z",
		}},
		RowCount: 1, Truncated: true, LatencyMS: 1,
	}, nil
}
func (*playgroundPipelineExecutor) Execute(context.Context) (model.QueryResult, error) {
	return model.QueryResult{}, errors.New("unexpected demo execute")
}
func (*playgroundPipelineExecutor) ExecuteTransactional(context.Context, func(model.QueryResult) error) (model.QueryResult, error) {
	return model.QueryResult{}, errors.New("unexpected demo write transaction")
}
func (*playgroundPipelineExecutor) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, nil
}
func (*playgroundPipelineExecutor) Close() error        { return nil }
func (*playgroundPipelineExecutor) ReleaseReservation() {}
func (demoExecutor *playgroundPipelineExecutor) lastRowLimit() int {
	demoExecutor.mu.Lock()
	defer demoExecutor.mu.Unlock()
	return demoExecutor.rowLimit
}

func (demoExecutor *playgroundPipelineExecutor) setResult(result model.QueryResult) {
	demoExecutor.mu.Lock()
	defer demoExecutor.mu.Unlock()
	demoExecutor.result = &result
}

type playgroundPipelineApprovalWriter struct{}

func (playgroundPipelineApprovalWriter) Create(_ context.Context, approval model.Approval) (model.Approval, error) {
	return approval, nil
}

type playgroundPipelineRedactorBuilder struct{ redactor mask.Redactor }

func (builder *playgroundPipelineRedactorBuilder) RedactorFor(context.Context, string) (mask.Redactor, error) {
	return builder.redactor, nil
}

func latestPlaygroundPipelineAudit(t *testing.T, metadata *store.Store) model.AuditLog {
	t.Helper()
	page, err := metadata.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	return page.List[0]
}

func requirePlaygroundAuditAgent(t *testing.T, log model.AuditLog) string {
	t.Helper()
	require.NotNil(t, log.AgentID)
	return *log.AgentID
}

var _ pipeline.IdentityAuthenticator = (*playgroundPipelineAuthenticator)(nil)
var _ pipeline.DatasourceReader = playgroundPipelineDatasourceReader{}
var _ pipeline.PolicyLoader = playgroundPipelinePolicyLoader{}
var _ pipeline.ExecutorProvider = (*playgroundPipelineExecutorProvider)(nil)
var _ executor.Statement = (*playgroundPipelineExecutor)(nil)
var _ rules.TransactionMetadataProvider = (*playgroundPipelineExecutor)(nil)
var _ pipeline.ApprovalWriter = playgroundPipelineApprovalWriter{}
var _ pipeline.RedactorBuilder = (*playgroundPipelineRedactorBuilder)(nil)
