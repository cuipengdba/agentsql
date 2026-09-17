package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	demoROKeyCanary  = "asql_demo-ro-key-canary"
	demoDMLKeyCanary = "asql_demo-dml-key-canary"
	demoDSNCanary    = "postgres://demo:demo-password@database/demo"
)

type fakeDemoRunner struct {
	mu       sync.Mutex
	requests []pipeline.Request
	response pipeline.Response
	err      error
	panicVal any
}

func (runner *fakeDemoRunner) ProcessDemo(
	_ context.Context,
	request pipeline.Request,
) (pipeline.Response, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.requests = append(runner.requests, request)
	if runner.panicVal != nil {
		panic(runner.panicVal)
	}
	return runner.response, runner.err
}

func (runner *fakeDemoRunner) calls() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return len(runner.requests)
}

func (runner *fakeDemoRunner) captured() []pipeline.Request {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]pipeline.Request{}, runner.requests...)
}

func TestPlaygroundRunIsNotRegisteredOutsideDemo(t *testing.T) {
	fixture := newAdminFixture(t)
	before := auditCount(t, fixture)

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusNotFound, status, body)
	assertPlaygroundBodyHasNoSecrets(t, body)

	status, body = fixture.request(http.MethodPost, "/api/v1/playground/run", "",
		`{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusUnauthorized, status, body)
	assertPlaygroundBodyHasNoSecrets(t, body)
	require.Equal(t, before, auditCount(t, fixture))
}

func TestPlaygroundRunRequiresAdminAuthentication(t *testing.T) {
	fixture, runner := newDemoAdminFixture(t)
	before := auditCount(t, fixture)

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", "",
		`{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusUnauthorized, status, body)
	require.Zero(t, runner.calls())
	require.Equal(t, before, auditCount(t, fixture))
	assertPlaygroundBodyHasNoSecrets(t, body)
}

func TestPlaygroundRunRejectsInvalidAndIdentityInputsBeforePipeline(t *testing.T) {
	fixture, runner := newDemoAdminFixture(t)
	before := auditCount(t, fixture)
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{name: "empty sql", body: `{"sql":"  ","datasource_id":"ds-demo-pg","agent_profile":"ro"}`, wantStatus: http.StatusUnprocessableEntity, wantMsg: "sql is required"},
		{name: "invalid profile", body: `{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"readonly"}`, wantStatus: http.StatusUnprocessableEntity, wantMsg: "agent_profile must be ro or dml"},
		{name: "datasource outside allowlist", body: `{"sql":"SELECT 1","datasource_id":"ds-production","agent_profile":"ro"}`, wantStatus: http.StatusForbidden, wantMsg: "datasource is not allowed for demo"},
		{name: "api key", body: `{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro","api_key":"asql_attacker"}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid request body"},
		{name: "agent id", body: `{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro","agent_id":"agent-admin"}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid request body"},
		{name: "agent level", body: `{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro","agent_level":"ddl"}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid request body"},
		{name: "database type", body: `{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro","db_type":"postgres"}`, wantStatus: http.StatusBadRequest, wantMsg: "invalid request body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken, test.body)
			require.Equal(t, test.wantStatus, status, body)
			require.Contains(t, body, test.wantMsg)
			assertPlaygroundBodyHasNoSecrets(t, body)
		})
	}

	require.Zero(t, runner.calls())
	require.Equal(t, before, auditCount(t, fixture))
}

func TestPlaygroundRunMapsProfilesAndReturnsOnlyWhitelistedDTO(t *testing.T) {
	fixture, runner := newDemoAdminFixture(t)
	runner.response = pipeline.Response{
		Decision: model.DecisionAllow,
		Assessment: model.Assessment{
			Decision: model.DecisionAllow,
			Risk:     model.RiskInfo,
			StmtType: "SELECT",
			Hits: []model.RuleHit{{
				RuleID: "R005", Risk: model.RiskWarn, Decision: model.DecisionWarn,
				Message: "large scan", Suggestion: "add a limit",
			}},
			EstScanRows:  42,
			Reason:       "allowed",
			Suggestion:   "",
			Normalized:   "SELECT id, phone FROM customers LIMIT ?",
			Objects:      []model.ObjectRef{{Schema: "public", Table: "customers", Alias: "c"}},
			StageLatency: map[string]int64{pipeline.StageAuth: 1, pipeline.StageAudit: 2},
		},
		Result: &model.QueryResult{
			Columns: []string{"id", "phone"}, Rows: [][]string{{"1", "138****8000"}},
			RowCount: 1, Truncated: false, LatencyMS: 3,
		},
		Redact:  mask.RedactReport{TouchedColumns: map[int]mask.SensitiveType{1: mask.TypePhone}, MaskedCells: 1},
		AuditID: 91,
	}

	for _, profile := range []string{"ro", "dml"} {
		status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
			`{"sql":"SELECT id, phone FROM customers LIMIT 1","datasource_id":"ds-demo-pg","agent_profile":"`+profile+`"}`)
		require.Equal(t, http.StatusOK, status, body)
		assertPlaygroundBodyHasNoSecrets(t, body)
		assertPlaygroundRunJSONWhitelist(t, body)

		var envelope struct {
			Data playgroundRunView `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &envelope))
		require.Equal(t, "allow", envelope.Data.Decision)
		require.Equal(t, int64(91), envelope.Data.AuditID)
		require.Equal(t, profile, envelope.Data.AgentProfile)
		require.Equal(t, []string{"id", "phone"}, envelope.Data.Result.Columns)
		require.Equal(t, [][]string{{"1", "138****8000"}}, envelope.Data.Result.Rows)
		require.Equal(t, map[int]string{1: "phone"}, envelope.Data.Redact.TouchedColumns)
	}

	requests := runner.captured()
	require.Len(t, requests, 2)
	require.Equal(t, demoROKeyCanary, requests[0].APIKey)
	require.Equal(t, demoDMLKeyCanary, requests[1].APIKey)
	for _, request := range requests {
		require.Equal(t, "query", request.MCPTool)
		require.Equal(t, config.DemoDatasourcePG, request.DatasourceID)
		require.Equal(t, "SELECT id, phone FROM customers LIMIT 1", request.SQL)
	}
}

func TestPlaygroundRunDenyAndInternalErrorAreSafe(t *testing.T) {
	fixture, runner := newDemoAdminFixture(t)
	runner.response = pipeline.Response{
		Decision: model.DecisionDeny,
		Assessment: model.Assessment{
			Decision: model.DecisionDeny,
			Risk:     model.RiskDeny,
			StmtType: "UPDATE",
			Hits: []model.RuleHit{
				{RuleID: "R002", Risk: model.RiskDeny, Decision: model.DecisionDeny, Message: "missing WHERE"},
				{RuleID: "DEMO_NON_SELECT", Risk: model.RiskDeny, Decision: model.DecisionDeny, Message: "demo is read-only"},
			},
		},
		AuditID: 92,
	}

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"UPDATE orders SET status='cancelled'","datasource_id":"ds-demo-mysql","agent_profile":"dml"}`)
	require.Equal(t, http.StatusOK, status, body)
	assertPlaygroundBodyHasNoSecrets(t, body)
	var envelope struct {
		Data playgroundRunView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	require.Equal(t, "deny", envelope.Data.Decision)
	require.ElementsMatch(t, []string{"R002", "DEMO_NON_SELECT"}, playgroundRunHitIDs(envelope.Data.Assessment.Hits))
	require.Equal(t, int64(92), envelope.Data.AuditID)
	require.Empty(t, envelope.Data.Result.Columns)
	require.Empty(t, envelope.Data.Result.Rows)
	require.Zero(t, envelope.Data.Result.RowCount)

	runner.err = errors.New("backend failed: " + demoROKeyCanary + " " + demoDSNCanary)
	status, body = fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusInternalServerError, status, body)
	require.Contains(t, body, "internal error")
	assertPlaygroundBodyHasNoSecrets(t, body)
}

func TestPlaygroundRunPanicRecoveryDoesNotLeakCredentialsOrDatasourceSecrets(t *testing.T) {
	var logs bytes.Buffer
	fixture, runner := newDemoAdminFixtureWithLogger(t, zerolog.New(&logs))
	runner.panicVal = demoROKeyCanary + " " + demoDMLKeyCanary + " " + demoDSNCanary

	status, body := fixture.request(http.MethodPost, "/api/v1/playground/run", fixture.adminToken,
		`{"sql":"SELECT 1","datasource_id":"ds-demo-pg","agent_profile":"ro"}`)
	require.Equal(t, http.StatusInternalServerError, status, body)
	require.Contains(t, body, "internal error")
	assertPlaygroundBodyHasNoSecrets(t, body)
	assertPlaygroundBodyHasNoSecrets(t, logs.String())
}

func newDemoAdminFixture(t *testing.T) (*adminFixture, *fakeDemoRunner) {
	return newDemoAdminFixtureWithLogger(t, zerolog.Nop())
}

func newDemoAdminFixtureWithLogger(t *testing.T, logger zerolog.Logger) (*adminFixture, *fakeDemoRunner) {
	t.Helper()
	fixture := newAdminFixture(t)
	runner := &fakeDemoRunner{}
	cfg := adminTestConfig("unused-demo.db")
	cfg.Demo = config.DemoConfig{
		Enabled: true,
		AllowedDatasourceIDs: []string{
			config.DemoDatasourcePG,
			config.DemoDatasourceMySQL,
		},
	}
	deps := Deps{
		Runtime:       &bootstrap.Runtime{Store: fixture.store},
		Config:        cfg,
		AdminUsername: "admin",
		AdminPassword: "password",
		TokenKey:      DeriveTokenKey([]byte(adminTestSecret)),
		demoRunner:    runner,
		demoKeys:      demoProfileKeys{ro: demoROKeyCanary, dml: demoDMLKeyCanary},
	}
	handler, err := NewHandler(deps, logger)
	require.NoError(t, err)
	fixture.handler = handler
	return fixture, runner
}

func playgroundRunHitIDs(hits []playgroundRunHitView) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.RuleID)
	}
	return ids
}

func auditCount(t *testing.T, fixture *adminFixture) int64 {
	t.Helper()
	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	return page.Total
}

func assertPlaygroundBodyHasNoSecrets(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{
		demoROKeyCanary,
		demoDMLKeyCanary,
		"asql_",
		demoDSNCanary,
		"demo-password",
		"database-password",
	} {
		require.NotContains(t, body, secret)
	}
}

func assertPlaygroundRunJSONWhitelist(t *testing.T, body string) {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	require.ElementsMatch(t, []string{
		"decision", "assessment", "result", "redact", "audit_id", "datasource_id", "agent_profile",
	}, mapKeys(envelope.Data))
	require.NotContains(t, body, "approval_id")
	require.NotContains(t, body, "api_key")
	require.NotContains(t, body, "agent_id")
	require.NotContains(t, body, "db_type")

	var assessment map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data["assessment"], &assessment))
	require.ElementsMatch(t, []string{
		"decision", "risk", "stmt_type", "hits", "est_scan_rows", "reason", "suggestion",
		"normalized", "objects", "stage_latency",
	}, mapKeys(assessment))
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data["result"], &result))
	require.ElementsMatch(t, []string{
		"columns", "rows", "row_count", "truncated", "latency_ms",
	}, mapKeys(result))
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

var _ DemoRunner = (*fakeDemoRunner)(nil)
