package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const adminTestSecret = "0123456789abcdef0123456789abcdef"

func TestAdminLoginAndAuthenticationIsolation(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/auth/login", "", `{"username":"admin","password":"password"}`)
	require.Equal(t, http.StatusOK, status)
	var login struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &login))
	require.Equal(t, 0, login.Code)
	require.NotEmpty(t, login.Data.Token)
	status, _ = fixture.request(http.MethodGet, "/api/v1/auth/me", "Bearer "+login.Data.Token, "")
	require.Equal(t, http.StatusOK, status)
	status, body = fixture.request(http.MethodGet, "/api/v1/auth/me", "Bearer "+fixture.agentKey, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Contains(t, body, "unauthorized")
	status, _ = fixture.request(http.MethodPost, "/api/v1/auth/login", "", `{"username":"admin","password":"wrong"}`)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = fixture.request(http.MethodGet, "/api/v1/auth/me", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/auth/logout", "Bearer "+login.Data.Token, "")
	require.Equal(t, http.StatusOK, status)
}

func TestAdminTokenExpiryAndStrictBearer(t *testing.T) {
	key := DeriveTokenKey([]byte(adminTestSecret))
	token, _, err := issueAdminToken(key, time.Unix(1_000, 0), "jti")
	require.NoError(t, err)
	require.NoError(t, validateAdminToken(key, token, time.Unix(1_000+1, 0)))
	require.ErrorIs(t, validateAdminToken(key, token, time.Unix(1_000+12*60*60, 0)), ErrInvalidAdminToken)
	for _, header := range []string{"", "Basic abc", "Bearer  " + token, "Bearer " + token + " ", "Bearer " + token + " extra"} {
		_, ok := adminBearerToken(header)
		if header == "Bearer "+token {
			require.True(t, ok)
		} else {
			require.False(t, ok)
		}
	}
}

func TestAdminAgentCRUDAndKeyRedaction(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/agents", fixture.adminToken, `{"id":"agent-new","name":"New","level":"dml"}`)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "api_key")
	require.NotContains(t, body, "api_key_hash")
	var created struct {
		Data agentView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.NotEmpty(t, created.Data.APIKey)
	status, body = fixture.request(http.MethodGet, "/api/v1/agents/agent-new", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, body, created.Data.APIKey)
	require.NotContains(t, body, "api_key_hash")
	status, body = fixture.request(http.MethodPost, "/api/v1/agents/agent-new/rotate-key", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "api_key")
	status, _ = fixture.request(http.MethodPost, "/api/v1/agents", fixture.adminToken, `{"id":"bad","name":"Bad","level":"root"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	_, err := fixture.store.Policies().Create(context.Background(), model.Policy{ID: "agent-policy", AgentID: "agent-new", DatasourceID: fixture.datasource.ID, ObjectType: "table", ObjectName: "public.t", Action: "allow"})
	require.NoError(t, err)
	status, body = fixture.request(http.MethodDelete, "/api/v1/agents/agent-new", fixture.adminToken, "")
	require.Equal(t, http.StatusConflict, status)
	require.Contains(t, body, "policies")
}

func TestAdminAgentUpdateExpiresAtThreeStates(t *testing.T) {
	fixture := newAdminFixture(t)
	original := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	agent := fixture.agent
	agent.ExpiresAt = &original
	_, err := fixture.store.Agents().Update(context.Background(), agent)
	require.NoError(t, err)

	status, body := fixture.request(http.MethodPut, "/api/v1/agents/agent-1", fixture.adminToken, `{}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.Agents().Get(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ExpiresAt)
	require.True(t, original.Equal(*stored.ExpiresAt))

	status, body = fixture.request(http.MethodPut, "/api/v1/agents/agent-1", fixture.adminToken, `{"expires_at":null}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err = fixture.store.Agents().Get(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Nil(t, stored.ExpiresAt)

	updated := time.Date(2031, time.February, 3, 4, 5, 6, 0, time.FixedZone("UTC+8", 8*60*60))
	status, body = fixture.request(http.MethodPut, "/api/v1/agents/agent-1", fixture.adminToken, `{"expires_at":"`+updated.Format(time.RFC3339)+`"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err = fixture.store.Agents().Get(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ExpiresAt)
	require.True(t, updated.Equal(*stored.ExpiresAt))

	status, body = fixture.request(http.MethodPut, "/api/v1/agents/agent-1", fixture.adminToken, `{"expires_at":"not-a-time"}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	afterInvalid, err := fixture.store.Agents().Get(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, afterInvalid.ExpiresAt)
	require.True(t, stored.ExpiresAt.Equal(*afterInvalid.ExpiresAt))
}

func TestAdminDatasourcesAndPing(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodGet, "/api/v1/datasources", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "has_password")
	require.NotContains(t, body, "password_enc")
	require.NotContains(t, body, "database-password")
	status, _ = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/ping", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, fixture.pinger.calls)
	fixture.pinger.err = errors.New("connection refused")
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/ping", fixture.adminToken, "")
	require.Equal(t, http.StatusInternalServerError, status)
	require.NotContains(t, body, "connection refused")
}

func TestAdminValidationPoliciesRulesMasks(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken, `{"id":"p-new","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow"}`)
	require.Equal(t, http.StatusOK, status)
	status, _ = fixture.request(http.MethodGet, "/api/v1/policies?agent_id=agent-1&page_size=1", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken, `{"id":"p-bad","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"*.*","action":"allow"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	_, err := fixture.store.Rules().Create(context.Background(), model.Rule{ID: "builtin", DBType: "all", Title: "Built in", RiskLevel: 1, PatternType: "ast", Definition: "{}", Enabled: true, Builtin: true})
	require.NoError(t, err)
	status, _ = fixture.request(http.MethodDelete, "/api/v1/rules/builtin", fixture.adminToken, "")
	require.Equal(t, http.StatusForbidden, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken, `{"id":"mask","table_name":"customers","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken, `{"id":"bad-mask","table_name":"customers","column_name":"id","sensitive_type":"idcard","algo":"mask"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

func TestAdminMaskRuleCanonicalScopeAndConflict(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(
		http.MethodPost,
		"/api/v1/mask_rules",
		fixture.adminToken,
		`{"id":"global-phone","datasource_id":"   ","column_name":" \"PHONE\" ","sensitive_type":"phone","algo":"mask"}`,
	)
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.MaskRules().Get(context.Background(), "global-phone")
	require.NoError(t, err)
	require.Nil(t, stored.DatasourceID)
	require.Empty(t, stored.TableName)
	require.Equal(t, "phone", stored.ColumnName)

	status, body = fixture.request(
		http.MethodPost,
		"/api/v1/mask_rules",
		fixture.adminToken,
		`{"id":"duplicate-global","table_name":"other","column_name":"[Phone]","sensitive_type":"email","algo":"mask"}`,
	)
	require.Equal(t, http.StatusConflict, status, body)
	require.Contains(t, body, "同列仅支持一条规则")

	status, body = fixture.request(
		http.MethodPost,
		"/api/v1/mask_rules",
		fixture.adminToken,
		`{"id":"bound-phone","datasource_id":"ds-1","column_name":"phone","sensitive_type":"email","algo":"mask"}`,
	)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(
		http.MethodPut,
		"/api/v1/mask_rules/bound-phone",
		fixture.adminToken,
		`{"datasource_id":"ds-1","column_name":" PHONE ","sensitive_type":"email","algo":"mask"}`,
	)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(
		http.MethodPost,
		"/api/v1/mask_rules",
		fixture.adminToken,
		`{"id":"bound-other","datasource_id":"ds-1","column_name":"email","sensitive_type":"email","algo":"mask"}`,
	)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(
		http.MethodPut,
		"/api/v1/mask_rules/bound-other",
		fixture.adminToken,
		`{"datasource_id":"ds-1","column_name":"phone","sensitive_type":"email","algo":"mask"}`,
	)
	require.Equal(t, http.StatusConflict, status, body)
	require.Contains(t, body, "同列仅支持一条规则")

	status, _ = fixture.request(
		http.MethodPost,
		"/api/v1/mask_rules",
		fixture.adminToken,
		`{"id":"invalid-mask","column_name":"phone2","sensitive_type":"phone","algo":"hash"}`,
	)
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

func TestAdminApprovalAuditExportAndDashboard(t *testing.T) {
	fixture := newAdminFixture(t)
	approval, err := fixture.store.Approvals().Create(context.Background(), model.Approval{ID: "approval", AgentID: stringPointerAdmin("agent-1"), Status: "pending"})
	require.NoError(t, err)
	status, _ := fixture.request(http.MethodPost, "/api/v1/approvals/"+approval.ID+"/decide", fixture.adminToken, `{"decision":"approve","comment":"ok"}`)
	require.Equal(t, http.StatusOK, status)
	status, body := fixture.request(http.MethodPost, "/api/v1/approvals/"+approval.ID+"/decide", fixture.adminToken, `{"decision":"reject"}`)
	require.Equal(t, http.StatusConflict, status)
	require.Contains(t, body, "pending")
	for _, decision := range []string{"allow", "deny", "warn", "approve"} {
		_, err = fixture.store.AuditLogs().Insert(context.Background(), model.AuditLog{AgentID: stringPointerAdmin("agent-1"), Decision: decision, SQLRaw: stringPointerAdmin("SELECT 1"), RuleHits: stringPointerAdmin("[]"), RiskLevel: intPointerAdmin(4), EstRows: int64PointerAdmin(2)})
		require.NoError(t, err)
	}
	status, body = fixture.request(http.MethodGet, "/api/v1/audit?decisions=allow,deny&page_size=2", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "page_size")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/audit/export", nil)
	request.Header.Set("Authorization", fixture.adminToken)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/x-ndjson", recorder.Header().Get("Content-Type"))
	require.NotEmpty(t, strings.TrimSpace(recorder.Body.String()))
	status, body = fixture.request(http.MethodGet, "/api/v1/dashboard/summary?days=14", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "trend_14d")
	require.Contains(t, body, "decision_distribution")
}

func TestAdminApprovalConcurrentDecideReturnsOneSuccessOneConflict(t *testing.T) {
	fixture := newAdminFixture(t)
	_, err := fixture.store.Approvals().Create(context.Background(), model.Approval{
		ID: "approval-concurrent", Status: "pending",
	})
	require.NoError(t, err)

	type result struct {
		status int
		body   string
	}
	ready := sync.WaitGroup{}
	ready.Add(2)
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, body := range []string{
		`{"decision":"approve","comment":"admin-a"}`,
		`{"decision":"reject","comment":"admin-b"}`,
	} {
		body := body
		go func() {
			ready.Done()
			<-start
			status, responseBody := fixture.request(
				http.MethodPost,
				"/api/v1/approvals/approval-concurrent/decide",
				fixture.adminToken,
				body,
			)
			results <- result{status: status, body: responseBody}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	statuses := []int{first.status, second.status}
	require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, statuses)
	for _, outcome := range []result{first, second} {
		if outcome.status == http.StatusConflict {
			require.Contains(t, outcome.body, "approval is no longer pending")
		}
	}
	stored, err := fixture.store.Approvals().Get(context.Background(), "approval-concurrent")
	require.NoError(t, err)
	require.Contains(t, []string{"approved", "rejected"}, stored.Status)
	require.Contains(t, []string{"admin-a", "admin-b"}, *stored.Reason)

	status, _ := fixture.request(
		http.MethodPost,
		"/api/v1/approvals/missing/decide",
		fixture.adminToken,
		`{"decision":"approve"}`,
	)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = fixture.request(
		http.MethodPost,
		"/api/v1/approvals/approval-concurrent/decide",
		fixture.adminToken,
		`{"decision":"invalid"}`,
	)
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

func TestAdminJSONBodyAndPaginationGuards(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodGet, "/api/v1/agents?page_size=101", fixture.adminToken, "")
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/agents", fixture.adminToken, `{"id":"x","name":"x","level":"dml","unknown":1}`)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/agents", fixture.adminToken, `{"id":"x","name":"x","level":"dml"} {}`)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/agents", fixture.adminToken, strings.Repeat("x", maxJSONBodyBytes+1))
	require.Equal(t, http.StatusBadRequest, status)
}

func TestAdminPlaygroundStaticAssessmentIsAuthenticatedAndDoesNotAudit(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(
		http.MethodPost,
		"/api/v1/playground/assess",
		"",
		`{"sql":"SELECT 1","db_type":"postgres"}`,
	)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Contains(t, body, "unauthorized")

	status, _ = fixture.request(
		http.MethodPost,
		"/api/v1/playground/assess",
		fixture.adminToken,
		`{"sql":`,
	)
	require.Equal(t, http.StatusUnprocessableEntity, status)

	before, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	status, body = fixture.request(
		http.MethodPost,
		"/api/v1/playground/assess",
		fixture.adminToken,
		`{"sql":"SELECT id FROM public.customers WHERE id = 42 LIMIT 10","db_type":"postgres","agent_level":"readonly"}`,
	)
	require.Equal(t, http.StatusOK, status, body)
	var response struct {
		Code int                  `json:"code"`
		Data playgroundAssessView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Zero(t, response.Code)
	require.True(t, response.Data.StaticOnly)
	require.Equal(t, "allow", response.Data.Decision)
	require.Equal(t, "postgres", response.Data.DBType)
	require.Equal(t, "readonly", response.Data.AgentLevel)
	after, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Equal(t, before.Total, after.Total)
}

type adminFixture struct {
	store      *store.Store
	handler    http.Handler
	adminToken string
	agentKey   string
	agent      model.Agent
	datasource model.Datasource
	pinger     *fakePinger
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	opened, err := store.OpenWithSecret(context.Background(), filepath.Join(t.TempDir(), "admin.db"), []byte(adminTestSecret))
	require.NoError(t, err)
	plaintext, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := opened.Agents().Create(context.Background(), model.Agent{ID: "agent-1", Name: "Agent", Status: "active", APIKeyHash: hash, Level: "dml"})
	require.NoError(t, err)
	datasource, err := opened.Datasources().Create(context.Background(), model.Datasource{ID: "ds-1", Name: "DB", DBType: "postgres", Host: "db", Port: 5432, Database: "app", Username: "user", ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 1000}, "database-password")
	require.NoError(t, err)
	_, err = opened.Policies().Create(context.Background(), model.Policy{ID: "policy-1", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.customers", Action: "allow"})
	require.NoError(t, err)
	runtime := &bootstrap.Runtime{Store: opened}
	pinger := &fakePinger{}
	cfg := adminTestConfig(filepath.Join(t.TempDir(), "unused.db"))
	handler, err := NewHandler(Deps{Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)), DatasourcePinger: pinger}, zerolog.Nop())
	require.NoError(t, err)
	token, _, err := issueAdminToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(), "test-jti")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	return &adminFixture{store: opened, handler: handler, adminToken: "Bearer " + token, agentKey: plaintext, agent: agent, datasource: datasource, pinger: pinger}
}

func (fixture *adminFixture) request(method, path, authorization, body string) (int, string) {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

type fakePinger struct {
	calls int
	err   error
}

func (pinger *fakePinger) Ping(context.Context, model.Datasource) (PingResult, error) {
	pinger.calls++
	if pinger.err != nil {
		return PingResult{}, pinger.err
	}
	return PingResult{OK: true, LatencyMS: 1}, nil
}

func adminTestConfig(path string) config.Config {
	return config.Config{Server: config.ServerConfig{HTTPListen: "127.0.0.1:7780", ConsoleEnabled: true, EventStream: false, EventStreamMaxConnections: 100}, Store: config.StoreConfig{SQLitePath: path}, Defaults: config.DefaultsConfig{StatementTimeoutMS: 5000, RowLimit: 1000, MaxConnsPerDatasource: 5, QPSPerAgent: 20}, Theme: config.ThemeConfig{Default: "dark"}}
}
func stringPointerAdmin(value string) *string { return &value }
func intPointerAdmin(value int) *int          { return &value }
func int64PointerAdmin(value int64) *int64    { return &value }
