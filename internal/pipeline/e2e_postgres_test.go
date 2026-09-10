package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPipelinePostgresE2E(t *testing.T) {
	ctx := pipelineDockerTestContext(t)
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
		skipPipelineDocker(t, err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{
		ID:            "pipeline-postgres",
		DBType:        "postgres",
		Host:          host,
		Port:          port.Int(),
		Database:      database,
		Username:      username,
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
		RowLimit:      50,
	}
	databaseExecutor, err := executor.NewPostgresExecutor(ctx, datasource, password, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, databaseExecutor.Close()) })
	_, err = databaseExecutor.Execute(ctx, `CREATE TABLE allowed_rows (
  id integer PRIMARY KEY,
  value text NOT NULL,
  phone text NOT NULL
)`)
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, `INSERT INTO allowed_rows (id, value, phone)
VALUES (1, 'original', '13812345678')`)
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, "CREATE TABLE secret_rows (id integer PRIMARY KEY)")
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, "INSERT INTO secret_rows (id) VALUES (1)")
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, "CREATE TABLE big_rows (id integer PRIMARY KEY, value integer NOT NULL)")
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, `INSERT INTO big_rows (id, value)
SELECT value, value FROM generate_series(1, 200) AS value`)
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, "ANALYZE allowed_rows")
	require.NoError(t, err)
	_, err = databaseExecutor.Execute(ctx, "ANALYZE big_rows")
	require.NoError(t, err)

	counted := &countingPostgresExecutor{delegate: databaseExecutor}

	t.Run("E1 normal select allow", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(t, datasource, counted, "dml")
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("SELECT value FROM public.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "original", response.Result.Rows[0][0])
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Equal(t, 1, delta.query)
		require.Zero(t, delta.execute)
		require.Equal(t, "allow", ports.audit.last().Decision)
	})

	t.Run("E2 update without where denied and unchanged", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(t, datasource, counted, "dml")
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("UPDATE public.allowed_rows SET value = 'changed'"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		delta := counted.snapshot().minus(before)
		require.Zero(t, delta.explain+delta.query+delta.execute+delta.openSession)
		value, err := databaseExecutor.Query(ctx, "SELECT value FROM allowed_rows WHERE id = 1", 1)
		require.NoError(t, err)
		require.Equal(t, "original", value.Rows[0][0])
		require.Equal(t, "deny", ports.audit.last().Decision)
	})

	t.Run("E3 unauthorized table denied", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(t, datasource, counted, "dml")
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("SELECT id FROM public.secret_rows LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		delta := counted.snapshot().minus(before)
		require.Zero(t, delta.explain+delta.query+delta.execute+delta.openSession)
		require.Equal(t, "deny", ports.audit.last().Decision)
	})

	t.Run("E4 scan threshold creates approval without execution", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(
			t,
			datasource,
			counted,
			"dml",
			WithRuleLayers(engine.RuleLayers{Agent: engine.RuleLayer{
				"R004": {Thresholds: map[string]float64{rules.ThresholdMaxScanRows: 10}},
			}}),
		)
		ports.approvals.audit = ports.audit
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("SELECT id FROM public.big_rows"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionApprove, response.Decision)
		require.NotEmpty(t, response.ApprovalID)
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Zero(t, delta.query+delta.execute)
		require.Equal(t, "approve", ports.audit.last().Decision)
		require.Equal(t, response.AuditID, *ports.approvals.last().AuditID)
	})

	t.Run("E5 readonly write denied", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(t, datasource, counted, "readonly")
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("UPDATE public.allowed_rows SET value = 'changed' WHERE id = 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		delta := counted.snapshot().minus(before)
		require.Zero(t, delta.explain+delta.query+delta.execute+delta.openSession)
		require.Equal(t, "deny", ports.audit.last().Decision)
	})

	t.Run("E6 sensitive result is redacted", func(t *testing.T) {
		flow, ports := newPostgresE2EPipeline(t, datasource, counted, "dml")
		before := counted.snapshot()
		response, err := flow.Process(ctx, postgresE2ERequest("SELECT phone FROM public.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Equal(t, 1, response.Redact.MaskedCells)
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Equal(t, 1, delta.query)
		require.Zero(t, delta.execute)
		require.Equal(t, "allow", ports.audit.last().Decision)
	})
}

type postgresE2EPorts struct {
	audit     *fakeAuditRecorder
	approvals *fakeApprovalWriter
}

func newPostgresE2EPipeline(
	t *testing.T,
	datasource model.Datasource,
	databaseExecutor executor.Executor,
	agentLevel string,
	options ...Option,
) (*Pipeline, postgresE2EPorts) {
	t.Helper()
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Column:        "phone",
		SensitiveType: mask.TypePhone,
		Algorithm:     mask.AlgoMask,
	}})
	require.NoError(t, err)
	agentID := "e2e-" + agentLevel
	auditRecorder := &fakeAuditRecorder{}
	approvalWriter := &fakeApprovalWriter{}
	ports := Ports{
		Authenticator: &fakeAuthenticator{agent: model.Agent{ID: agentID, Status: "active", Level: agentLevel}},
		Datasources:   &fakeDatasourceReader{datasource: datasource},
		Policies: &fakePolicyLoader{policies: []model.Policy{
			{ID: "allow-rows", AgentID: agentID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.allowed_rows", Action: "allow"},
			{ID: "allow-big", AgentID: agentID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.big_rows", Action: "allow"},
		}},
		Executors: &fakeExecutorProvider{executor: databaseExecutor},
		Approvals: approvalWriter,
		Audit:     auditRecorder,
		Redactors: &fakeRedactorBuilder{redactor: redactor},
	}
	flow, err := New(ports, testPipelineSecret, options...)
	require.NoError(t, err)
	return flow, postgresE2EPorts{audit: auditRecorder, approvals: approvalWriter}
}

func postgresE2ERequest(sql string) Request {
	return Request{
		APIKey:       "asql_e2e",
		DatasourceID: "pipeline-postgres",
		SQL:          sql,
		MCPTool:      "query",
	}
}

type countingPostgresExecutor struct {
	mu       sync.Mutex
	delegate *executor.PostgresExecutor
	calls    executorCalls
}

func (*countingPostgresExecutor) Dialect() string { return "postgres" }

func (counted *countingPostgresExecutor) Ping(ctx context.Context) error {
	return counted.delegate.Ping(ctx)
}

func (counted *countingPostgresExecutor) OpenSession(
	ctx context.Context,
	sessionID string,
) (executor.Session, error) {
	counted.mu.Lock()
	counted.calls.openSession++
	counted.mu.Unlock()
	return counted.delegate.OpenSession(ctx, sessionID)
}

func (counted *countingPostgresExecutor) Explain(
	ctx context.Context,
	sql string,
) (model.ExplainInfo, error) {
	counted.mu.Lock()
	counted.calls.explain++
	counted.mu.Unlock()
	return counted.delegate.Explain(ctx, sql)
}

func (counted *countingPostgresExecutor) Query(
	ctx context.Context,
	sql string,
	limit int,
) (model.QueryResult, error) {
	counted.mu.Lock()
	counted.calls.query++
	counted.mu.Unlock()
	return counted.delegate.Query(ctx, sql, limit)
}

func (counted *countingPostgresExecutor) Execute(
	ctx context.Context,
	sql string,
) (model.QueryResult, error) {
	counted.mu.Lock()
	counted.calls.execute++
	counted.mu.Unlock()
	return counted.delegate.Execute(ctx, sql)
}

func (counted *countingPostgresExecutor) Close() error {
	return counted.delegate.Close()
}

func (counted *countingPostgresExecutor) TableHasIndex(schema, table string) (bool, error) {
	return counted.delegate.TableHasIndex(schema, table)
}

func (counted *countingPostgresExecutor) TableRowCount(schema, table string) (int64, error) {
	return counted.delegate.TableRowCount(schema, table)
}

func (counted *countingPostgresExecutor) TransactionState() (rules.TransactionState, error) {
	return counted.delegate.TransactionState()
}

func (counted *countingPostgresExecutor) snapshot() executorCalls {
	counted.mu.Lock()
	defer counted.mu.Unlock()
	return counted.calls
}

func (calls executorCalls) minus(previous executorCalls) executorCalls {
	return executorCalls{
		openSession:    calls.openSession - previous.openSession,
		explain:        calls.explain - previous.explain,
		query:          calls.query - previous.query,
		execute:        calls.execute - previous.execute,
		sessionExplain: calls.sessionExplain - previous.sessionExplain,
		sessionQuery:   calls.sessionQuery - previous.sessionQuery,
		sessionExecute: calls.sessionExecute - previous.sessionExecute,
		sessionClose:   calls.sessionClose - previous.sessionClose,
	}
}

func pipelineDockerTestContext(t *testing.T) context.Context {
	t.Helper()
	if err := probePipelineDocker(); err != nil {
		skipPipelineDocker(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func probePipelineDocker() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic, treat as unavailable: %v", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := testcontainers.NewDockerClient()
	if err != nil {
		return err
	}
	if _, err := client.Ping(ctx); err != nil {
		_ = client.Close()
		return err
	}
	return client.Close()
}

func skipPipelineDocker(t *testing.T, err error) {
	t.Helper()
	t.Logf("docker unavailable: %v", err)
	t.Skip("docker unavailable")
}

var (
	_ executor.Executor                 = (*countingPostgresExecutor)(nil)
	_ rules.MetadataProvider            = (*countingPostgresExecutor)(nil)
	_ rules.TransactionMetadataProvider = (*countingPostgresExecutor)(nil)
)
