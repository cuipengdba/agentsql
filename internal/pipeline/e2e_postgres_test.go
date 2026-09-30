package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPipelinePostgresE2E(t *testing.T) {
	for _, version := range []string{"14", "15", "16", "17", "18"} {
		version := version
		t.Run("pg"+version, func(t *testing.T) {
			runPostgresPipelineScenarios(t, "postgres:"+version)
		})
	}
}

func runPostgresPipelineScenarios(t *testing.T, image string) {
	t.Helper()
	ctx := pipelineDockerTestContext(t)
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
		ID:            "pipeline-postgres-" + strings.TrimPrefix(image, "postgres:"),
		DBType:        "postgres",
		Host:          host,
		Port:          port.Int(),
		Database:      database,
		Username:      username,
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
		RowLimit:      50,
	}
	databaseExecutor := newPipelineE2EDatabase(t, &datasource, password, false)
	setupPostgresPipelineSchema(t, ctx, databaseExecutor)

	counted := &countingDatabaseExecutor{delegate: databaseExecutor.gateway}
	allowedTables := []string{
		"public.a4_missing_table",
		"public.a4_missing_ddl",
		"public.allowed_rows",
		"public.big_rows",
		"public.customers",
		"public.orders",
		"public.sensitive_rows",
		"public.hash_rows",
		"public.block_rows",
	}

	t.Run("E1 normal select allow", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT value FROM public.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "original", response.Result.Rows[0][0])
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Equal(t, 1, delta.query)
		require.Zero(t, delta.execute)
		require.Equal(t, "allow", ports.audit.last().Decision)
	})

	t.Run("A4 missing object is a business error", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT * FROM public.a4_missing_table LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionError, response.Decision, "%+v", response)
		require.Equal(t, string(executor.DBErrorCodeObjectNotFound), response.ErrorCode)
		require.Equal(t, "表或对象不存在", response.ErrorMessage)
		require.Equal(t, executor.Suggestion(executor.DBErrorCodeObjectNotFound), response.Suggestion)
		require.Nil(t, response.Result)
		require.Empty(t, response.Redact)
		require.Equal(t, "error", ports.audit.last().Decision)
		require.Equal(t, response.ErrorMessage, *ports.audit.last().ErrorMsg)
		require.Equal(t, response.ErrorCode, *ports.audit.last().ErrorCode)
	})

	t.Run("A4 failed DDL persists related intent and error outcome", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "ddl", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"DROP INDEX public.a4_missing_ddl"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionError, response.Decision, "%+v", response)
		require.Equal(t, string(executor.DBErrorCodeObjectNotFound), response.ErrorCode)
		require.Equal(t, string(executor.DBStageExecute), response.ErrorStage)
		require.Nil(t, response.Result)
		logs := ports.audit.snapshot()
		require.Len(t, logs, 2)
		require.Equal(t, "allow", logs[0].Decision)
		require.Equal(t, "error", logs[1].Decision)
		require.Equal(t, response.ErrorCode, requireStringPointer(t, logs[1].ErrorCode))
		require.Nil(t, logs[1].RowsReturned)
		assertAuditPhase(t, logs[0], "intent", 0)
		assertAuditPhase(t, logs[1], "outcome", logs[0].ID)
	})

	t.Run("E4 real explain dynamic gate", func(t *testing.T) {
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id FROM public.big_rows WHERE id > 0 LIMIT 5"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Positive(t, response.Assessment.EstScanRows)
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Equal(t, 1, delta.query)
		require.Zero(t, delta.execute)
	})

	t.Run("E6 sensitive result is redacted", func(t *testing.T) {
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT phone FROM public.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Equal(t, 1, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
	})

	t.Run("four category real driver values are redacted without audit leakage", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipelineWithRules(t, datasource, counted, "dml", allowedTables, sensitiveE2ERules(true), nil)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id_card, legacy_id_card, pan_plain, pan_formatted, client_ip, ip_network, birth_date FROM public.sensitive_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, []string{"110105********002X", "130503******001", "411111******1111", "411111******1111", "192.0.*.*", "198.51.*.*", "2000-**-**"}, response.Result.Rows[0])
		require.Equal(t, 7, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeIDCard, 1: mask.TypeIDCard, 2: mask.TypeBankCard, 3: mask.TypeBankCard, 4: mask.TypeIP, 5: mask.TypeIP, 6: mask.TypeBirthDate}, response.Redact.TouchedColumns)
		assertSensitiveE2ENotPresent(t, response, ports.audit.last(), []string{"11010519491231002X", "130503670401001", "4111111111111111", "4111 1111-1111 1111", "192.0.2.10/32", "198.51.100.0/24", "2000-02-29T00:00:00Z"})

		fallback, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id_card FROM public.sensitive_rows WHERE id = 2 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, mask.RedactedFallback, fallback.Result.Rows[0][0])
		require.NotContains(t, fmt.Sprintf("%v", fallback), "PG_INVALID_ID_SENTINEL_8f31")
	})
	runHashE2EScenarios(t, ctx, "public.hash_rows", datasource, counted, allowedTables)
	runBlockE2EScenarios(t, ctx, "public.block_rows", datasource, counted, allowedTables)

	runMaskScopeScenarios(t, ctx, "postgres", datasource, databaseExecutor, counted, allowedTables)
	runDirectSourceFallbackScenario(t, ctx, "postgres", datasource, counted, allowedTables)

	if image == "postgres:14" || image == "postgres:18" {
		runPostgresStaticAndApprovalScenarios(t, ctx, datasource, databaseExecutor, counted, allowedTables)
		runExpressionLineageNowMaskedScenario(t, ctx, "postgres", datasource, counted, allowedTables)
	}
	if image == "postgres:18" {
		runPostgresSchemaQualifiedFallbackScenario(t, ctx, datasource, counted, allowedTables)
	}
}

func setupPostgresPipelineSchema(t *testing.T, ctx context.Context, databaseExecutor *pipelineE2EDatabase) {
	t.Helper()
	statements := []string{
		`CREATE TABLE allowed_rows (id integer PRIMARY KEY, value text NOT NULL, phone text NOT NULL)`,
		`INSERT INTO allowed_rows (id, value, phone) VALUES (1, 'original', '13812345678')`,
		`CREATE TABLE secret_rows (id integer PRIMARY KEY)`,
		`INSERT INTO secret_rows (id) VALUES (1)`,
		`CREATE TABLE big_rows (id integer PRIMARY KEY, value integer NOT NULL)`,
		`INSERT INTO big_rows (id, value) SELECT value, value FROM generate_series(1, 200) AS value`,
		`CREATE TABLE customers (id integer PRIMARY KEY, phone text NOT NULL, name text NOT NULL)`,
		`INSERT INTO customers (id, phone, name) VALUES
 (1, '13812345678', 'Alice'), (2, '13987654321', 'Bob')`,
		`CREATE TABLE orders (id integer PRIMARY KEY, customer_id integer NOT NULL, note text NOT NULL)`,
		`INSERT INTO orders (id, customer_id, note) VALUES
 (10, 1, 'first note'), (20, 2, 'second note')`,
		`CREATE TABLE sensitive_rows (id integer PRIMARY KEY, id_card text NOT NULL, legacy_id_card text NOT NULL, pan_plain text NOT NULL, pan_formatted text NOT NULL, client_ip inet NOT NULL, ip_network cidr NOT NULL, birth_date date NOT NULL)`,
		`INSERT INTO sensitive_rows VALUES
 (1, '11010519491231002X', '130503670401001', '4111111111111111', '4111 1111-1111 1111', '192.0.2.10', '198.51.100.0/24', DATE '2000-02-29'),
 (2, 'PG_INVALID_ID_SENTINEL_8f31', '130503670401001', '4111111111111111', '4111 1111-1111 1111', '192.0.2.10', '198.51.100.0/24', DATE '2000-02-29')`,
		`CREATE TABLE hash_rows (id integer PRIMARY KEY, secret text NULL)`,
		`INSERT INTO hash_rows VALUES
 (1, 'Ordinary Alice'), (2, 'Ordinary Alice'), (3, ' Ordinary Alice '), (4, ''), (5, NULL), (6, 'T41_E2E_RAW_SENTINEL_b7a3')`,
		`CREATE TABLE block_rows (id integer PRIMARY KEY, phone text NULL, hash_secret text NULL, blocked_secret text NULL)`,
		`INSERT INTO block_rows VALUES
 (1, '13812345678', 'T42_E2E_HASH_RAW_2f64', 'T42_E2E_BLOCK_RAW_6d19'),
 (2, '', '', ''),
 (3, NULL, NULL, NULL)`,
		"ANALYZE allowed_rows",
		"ANALYZE big_rows",
		"ANALYZE customers",
		"ANALYZE orders",
		"ANALYZE sensitive_rows",
		"ANALYZE hash_rows",
		"ANALYZE block_rows",
	}
	for _, statement := range statements {
		_, err := databaseExecutor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func runPostgresStaticAndApprovalScenarios(
	t *testing.T,
	ctx context.Context,
	datasource model.Datasource,
	databaseExecutor *pipelineE2EDatabase,
	counted *countingDatabaseExecutor,
	allowedTables []string,
) {
	t.Helper()
	t.Run("E2 update without where denied and unchanged", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"UPDATE public.allowed_rows SET value = 'changed'"))
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
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id FROM public.secret_rows LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R010")
		require.Contains(t, response.Assessment.Reason, "不在 Agent 的允许范围内")
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Zero(t, delta.query+delta.execute+delta.openSession)
		require.Equal(t, "deny", ports.audit.last().Decision)
	})

	t.Run("E5 large scan creates approval without execution", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(
			t,
			datasource,
			counted,
			"dml",
			allowedTables,
			WithRuleLayers(engine.RuleLayers{Agent: engine.RuleLayer{
				"R004": {Thresholds: map[string]float64{rules.ThresholdMaxScanRows: 10}},
			}}),
		)
		ports.approvals.audit = ports.audit
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id FROM public.big_rows"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionApprove, response.Decision)
		require.NotEmpty(t, response.ApprovalID)
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Zero(t, delta.query+delta.execute)
		require.Equal(t, "approve", ports.audit.last().Decision)
		require.Equal(t, response.AuditID, *ports.approvals.last().AuditID)
	})

	t.Run("readonly write denied", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "readonly", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"UPDATE public.allowed_rows SET value = 'changed' WHERE id = 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		delta := counted.snapshot().minus(before)
		require.Zero(t, delta.explain+delta.query+delta.execute+delta.openSession)
		require.Equal(t, "deny", ports.audit.last().Decision)
	})
}

func runPostgresSchemaQualifiedFallbackScenario(
	t *testing.T,
	ctx context.Context,
	datasource model.Datasource,
	counted *countingDatabaseExecutor,
	allowedTables []string,
) {
	t.Helper()
	t.Run("a0 schema qualified direct source fallback", func(t *testing.T) {
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT public.customers.phone AS mobile FROM public.customers ORDER BY id"))
		require.NoError(t, err)
		require.Equal(t, []string{"mobile"}, response.Result.Columns)
		require.Equal(t, [][]string{{"138****5678"}, {"139****4321"}}, response.Result.Rows)
		require.Equal(t, 2, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
	})
}

type pipelineE2EPorts struct {
	audit     *fakeAuditRecorder
	approvals *fakeApprovalWriter
}

func newDatabaseE2EPipeline(
	t *testing.T,
	datasource model.Datasource,
	databaseExecutor ExecutorProvider,
	agentLevel string,
	allowedTables []string,
	options ...Option,
) (*Pipeline, pipelineE2EPorts) {
	t.Helper()
	return newDatabaseE2EPipelineWithRules(t, datasource, databaseExecutor, agentLevel, allowedTables, []mask.Rule{{
		Column:        "phone",
		SensitiveType: mask.TypePhone,
		Algorithm:     mask.AlgoMask,
	}}, nil, options...)
}

func newDatabaseE2EPipelineWithRules(
	t *testing.T,
	datasource model.Datasource,
	databaseExecutor ExecutorProvider,
	agentLevel string,
	allowedTables []string,
	rules []mask.Rule,
	redactorOptions []mask.Option,
	options ...Option,
) (*Pipeline, pipelineE2EPorts) {
	t.Helper()
	redactor, err := mask.NewRedactor(rules, redactorOptions...)
	require.NoError(t, err)
	agentID := "e2e-" + datasource.DBType + "-" + agentLevel
	policies := make([]model.Policy, 0, len(allowedTables))
	for index, table := range allowedTables {
		policies = append(policies, model.Policy{
			ID:           fmt.Sprintf("allow-%d", index),
			AgentID:      agentID,
			DatasourceID: datasource.ID,
			ObjectType:   "table",
			ObjectName:   table,
			Action:       "allow",
		})
	}
	auditRecorder := &fakeAuditRecorder{}
	approvalWriter := &fakeApprovalWriter{}
	ports := Ports{
		Authenticator: &fakeAuthenticator{agent: model.Agent{ID: agentID, Status: "active", Level: agentLevel}},
		Datasources:   &fakeDatasourceReader{datasource: datasource},
		Policies:      &fakePolicyLoader{policies: policies},
		Executors:     databaseExecutor,
		Approvals:     approvalWriter,
		Audit:         auditRecorder,
		Redactors:     &fakeRedactorBuilder{redactor: redactor},
	}
	flow, err := New(ports, testPipelineSecret, options...)
	require.NoError(t, err)
	return flow, pipelineE2EPorts{audit: auditRecorder, approvals: approvalWriter}
}

func runHashE2EScenarios(
	t *testing.T,
	ctx context.Context,
	table string,
	datasource model.Datasource,
	databaseExecutor ExecutorProvider,
	allowedTables []string,
) {
	t.Helper()
	const rawSentinel = "T41_E2E_RAW_SENTINEL_b7a3"
	keyA := []byte("pipeline-hash-key-A-0123456789abcdef")
	keyB := []byte("pipeline-hash-key-B-0123456789abcdef")
	rules := []mask.Rule{{Column: "secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash}}
	flowA, portsA := newDatabaseE2EPipelineWithRules(t, datasource, databaseExecutor, "dml", allowedTables, rules, []mask.Option{mask.WithHashKey(keyA)})
	query := "SELECT secret FROM " + table + " ORDER BY id"
	response, err := flowA.Process(ctx, databaseE2ERequest(datasource.ID, query))
	require.NoError(t, err)
	require.Len(t, response.Result.Rows, 6)
	require.Equal(t, response.Result.Rows[0][0], response.Result.Rows[1][0], "same value across rows must have one deterministic fingerprint")
	require.NotEqual(t, response.Result.Rows[0][0], response.Result.Rows[2][0], "leading/trailing spaces must participate in HMAC")
	require.Empty(t, response.Result.Rows[3][0])
	require.Empty(t, response.Result.Rows[4][0])
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, response.Result.Rows[0][0])
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, response.Result.Rows[5][0])
	require.Equal(t, 4, response.Redact.MaskedCells)
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeGeneric}, response.Redact.TouchedColumns)
	assertSensitiveE2ENotPresent(t, response, portsA.audit.last(), []string{rawSentinel})

	repeated, err := flowA.Process(ctx, databaseE2ERequest(datasource.ID, "SELECT secret FROM "+table+" WHERE id = 1 LIMIT 1"))
	require.NoError(t, err)
	require.Equal(t, response.Result.Rows[0][0], repeated.Result.Rows[0][0], "fingerprint must remain stable across results")

	flowB, _ := newDatabaseE2EPipelineWithRules(t, datasource, databaseExecutor, "dml", allowedTables, rules, []mask.Option{mask.WithHashKey(keyB)})
	differentKey, err := flowB.Process(ctx, databaseE2ERequest(datasource.ID, "SELECT secret FROM "+table+" WHERE id = 1 LIMIT 1"))
	require.NoError(t, err)
	require.NotEqual(t, repeated.Result.Rows[0][0], differentKey.Result.Rows[0][0])

	direct, err := mask.NewRedactor(rules, mask.WithHashKey(keyA))
	require.NoError(t, err)
	expected, _ := direct.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{rawSentinel}}})
	require.Equal(t, expected.Rows[0][0], response.Result.Rows[5][0])

	missingKeyFlow, missingKeyPorts := newDatabaseE2EPipeline(t, datasource, databaseExecutor, "dml", allowedTables)
	missingKeyFlow.ports.Redactors = &fakeRedactorBuilder{err: mask.ErrHashKeyRequired}
	failed, err := missingKeyFlow.Process(ctx, databaseE2ERequest(datasource.ID, "SELECT secret FROM "+table+" WHERE id = 6 LIMIT 1"))
	require.ErrorIs(t, err, mask.ErrHashKeyRequired)
	require.Nil(t, failed.Result, "redactor construction failure must clear the executed result")
	require.Equal(t, model.DecisionDeny, failed.Decision)
	failedAuditJSON, marshalErr := json.Marshal(missingKeyPorts.audit.last())
	require.NoError(t, marshalErr)
	require.Equal(t, "error", missingKeyPorts.audit.last().Decision)
	require.NotContains(t, string(failedAuditJSON), rawSentinel)
}

func runBlockE2EScenarios(
	t *testing.T,
	ctx context.Context,
	table string,
	datasource model.Datasource,
	databaseExecutor ExecutorProvider,
	allowedTables []string,
) {
	t.Helper()
	const (
		hashRaw  = "T42_E2E_HASH_RAW_2f64"
		blockRaw = "T42_E2E_BLOCK_RAW_6d19"
	)
	rules := []mask.Rule{
		{Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Column: "hash_secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash},
		{Column: "blocked_secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
	}
	flow, ports := newDatabaseE2EPipelineWithRules(
		t, datasource, databaseExecutor, "dml", allowedTables, rules,
		[]mask.Option{mask.WithHashKey([]byte("pipeline-block-e2e-key-0123456789ab"))},
	)
	response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
		"SELECT phone, hash_secret, blocked_secret FROM "+table+" ORDER BY id"))
	require.NoError(t, err)
	require.Len(t, response.Result.Rows, 3)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, response.Result.Rows[0][1])
	require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][2])
	require.Equal(t, []string{"", "", ""}, response.Result.Rows[1])
	require.Equal(t, []string{"", "", ""}, response.Result.Rows[2])
	require.Equal(t, 3, response.Redact.MaskedCells)
	require.Equal(t, map[int]mask.SensitiveType{
		0: mask.TypePhone, 1: mask.TypeGeneric, 2: mask.TypeGeneric,
	}, response.Redact.TouchedColumns)
	assertSensitiveE2ENotPresent(t, response, ports.audit.last(), []string{hashRaw, blockRaw, "13812345678"})
}

func sensitiveE2ERules(includePGCIDR bool) []mask.Rule {
	rules := []mask.Rule{
		{Column: "id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "legacy_id_card", SensitiveType: mask.TypeIDCard, Algorithm: mask.AlgoMask},
		{Column: "pan_plain", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "pan_formatted", SensitiveType: mask.TypeBankCard, Algorithm: mask.AlgoMask},
		{Column: "client_ip", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask},
		{Column: "birth_date", SensitiveType: mask.TypeBirthDate, Algorithm: mask.AlgoMask},
	}
	if includePGCIDR {
		rules = append(rules, mask.Rule{Column: "ip_network", SensitiveType: mask.TypeIP, Algorithm: mask.AlgoMask})
	}
	return rules
}

func assertSensitiveE2ENotPresent(t *testing.T, response Response, auditLog model.AuditLog, rawValues []string) {
	t.Helper()
	responseJSON, err := json.Marshal(response)
	require.NoError(t, err)
	auditJSON, err := json.Marshal(auditLog)
	require.NoError(t, err)
	for _, raw := range rawValues {
		require.NotContains(t, string(responseJSON), raw)
		require.NotContains(t, string(auditJSON), raw)
	}
}

func databaseE2ERequest(datasourceID, sql string) Request {
	return Request{
		APIKey:       "asql_e2e",
		DatasourceID: datasourceID,
		SQL:          sql,
		MCPTool:      "query",
	}
}

type pipelineE2EDatabase struct {
	gateway    *executor.Gateway
	datasource model.Datasource
}

func newPipelineE2EDatabase(t *testing.T, datasource *model.Datasource, password string, readOnly bool) *pipelineE2EDatabase {
	t.Helper()
	cipher, err := store.NewPasswordCipher(testPipelineSecret)
	require.NoError(t, err)
	datasource.PasswordEnc, err = cipher.Encrypt(password)
	require.NoError(t, err)
	database := &pipelineE2EDatabase{gateway: executor.NewGateway(readOnly), datasource: *datasource}
	t.Cleanup(func() { require.NoError(t, database.gateway.CloseAll()) })
	return database
}

func (database *pipelineE2EDatabase) statement(ctx context.Context, sqlText, sessionID string) (executor.Statement, error) {
	return database.gateway.AuthorizedExecute(ctx, database.datasource, testPipelineSecret, sqlText, sessionID)
}

func (database *pipelineE2EDatabase) Execute(ctx context.Context, sqlText string) (model.QueryResult, error) {
	statement, err := database.statement(ctx, sqlText, "")
	if err != nil {
		return model.QueryResult{}, err
	}
	defer statement.Close()
	return statement.Execute(ctx)
}

func (database *pipelineE2EDatabase) Query(ctx context.Context, sqlText string, limit int) (model.QueryResult, error) {
	statement, err := database.statement(ctx, sqlText, "")
	if err != nil {
		return model.QueryResult{}, err
	}
	defer statement.Close()
	return statement.Query(ctx, limit)
}

type countingDatabaseExecutor struct {
	mu       sync.Mutex
	delegate ExecutorProvider
	calls    executorCalls
}

func (counted *countingDatabaseExecutor) AuthorizedExecute(ctx context.Context, datasource model.Datasource, secret []byte, sqlText, sessionID string) (executor.Statement, error) {
	statement, err := counted.delegate.AuthorizedExecute(ctx, datasource, secret, sqlText, sessionID)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		counted.mu.Lock()
		counted.calls.openSession++
		counted.mu.Unlock()
	}
	return &countingStatement{parent: counted, delegate: statement, session: sessionID != ""}, nil
}

type countingStatement struct {
	parent   *countingDatabaseExecutor
	delegate executor.Statement
	session  bool
}

func (statement *countingStatement) Dialect() string { return statement.delegate.Dialect() }
func (statement *countingStatement) Explain(ctx context.Context) (model.ExplainInfo, error) {
	statement.parent.mu.Lock()
	if statement.session {
		statement.parent.calls.sessionExplain++
	} else {
		statement.parent.calls.explain++
	}
	statement.parent.mu.Unlock()
	return statement.delegate.Explain(ctx)
}
func (statement *countingStatement) Query(ctx context.Context, limit int) (model.QueryResult, error) {
	statement.parent.mu.Lock()
	if statement.session {
		statement.parent.calls.sessionQuery++
	} else {
		statement.parent.calls.query++
	}
	statement.parent.mu.Unlock()
	return statement.delegate.Query(ctx, limit)
}
func (statement *countingStatement) Execute(ctx context.Context) (model.QueryResult, error) {
	statement.parent.mu.Lock()
	if statement.session {
		statement.parent.calls.sessionExecute++
	} else {
		statement.parent.calls.execute++
	}
	statement.parent.mu.Unlock()
	return statement.delegate.Execute(ctx)
}
func (statement *countingStatement) ExecuteTransactional(ctx context.Context, before func(model.QueryResult) error) (model.QueryResult, error) {
	statement.parent.mu.Lock()
	statement.parent.calls.execute++
	statement.parent.mu.Unlock()
	return statement.delegate.ExecuteTransactional(ctx, before)
}
func (statement *countingStatement) TableHasIndex(schema, table string) (bool, error) {
	return statement.delegate.TableHasIndex(schema, table)
}
func (statement *countingStatement) TableRowCount(schema, table string) (int64, error) {
	return statement.delegate.TableRowCount(schema, table)
}
func (statement *countingStatement) TransactionState() (rules.TransactionState, error) {
	return statement.delegate.TransactionState()
}
func (statement *countingStatement) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return statement.delegate.MysqlTransactionState()
}
func (statement *countingStatement) Close() error {
	if statement.session {
		statement.parent.mu.Lock()
		statement.parent.calls.sessionClose++
		statement.parent.mu.Unlock()
	}
	return statement.delegate.Close()
}
func (statement *countingStatement) ReleaseReservation() {
	statement.delegate.ReleaseReservation()
}

func (counted *countingDatabaseExecutor) snapshot() executorCalls {
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
	unavailable, err := probePipelineDocker()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func probePipelineDocker() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err := client.Ping(ctx); err != nil {
		_ = client.Close()
		return isPipelineDockerDaemonUnavailable(err), err
	}
	return false, client.Close()
}

func isPipelineDockerDaemonUnavailable(err error) bool {
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

var (
	_ ExecutorProvider   = (*countingDatabaseExecutor)(nil)
	_ executor.Statement = (*countingStatement)(nil)
)
