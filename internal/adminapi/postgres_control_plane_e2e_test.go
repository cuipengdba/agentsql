package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgres18ControlPlaneEndToEndE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 control-plane HTTP E2E is an integration test")
	}
	ctx := adminDockerTestContext(t)
	const (
		metadataDatabase = "agentsql"
		businessDatabase = "demo"
		username         = "agentsql"
		password         = "agentsql-pg18-password"
		adminPassword    = "postgres-e2e-admin-password"
	)

	container, err := postgrescontainer.Run(
		ctx,
		"postgres:18",
		postgrescontainer.WithDatabase(metadataDatabase),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start postgres:18 (Docker daemon probe already succeeded)")
	}
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	metadataDSN := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username, password, host, port.Port(), metadataDatabase,
	)
	metadataPool, err := pgxpool.New(ctx, metadataDSN)
	require.NoError(t, err)
	t.Cleanup(metadataPool.Close)
	_, err = metadataPool.Exec(ctx, "CREATE DATABASE "+businessDatabase)
	require.NoError(t, err)

	businessDSN := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username, password, host, port.Port(), businessDatabase,
	)
	businessPool, err := pgxpool.New(ctx, businessDSN)
	require.NoError(t, err)
	t.Cleanup(businessPool.Close)
	_, err = businessPool.Exec(ctx, `
CREATE TABLE public.customers (
  id integer NOT NULL,
  email text NOT NULL,
  balance integer NOT NULL
)`)
	require.NoError(t, err)
	_, err = businessPool.Exec(ctx, `
INSERT INTO public.customers (id, email, balance)
VALUES (1, 'alice@example.com', 100), (2, 'bob@example.com', 200)`)
	require.NoError(t, err)

	t.Setenv("AGENTSQL_STORE_METADATA_DSN", metadataDSN)
	cfg := adminTestConfig("")
	cfg.Defaults.QPSPerAgent = 100
	cfg.Store.Metadata = &config.MetadataStoreConfig{
		Driver:          string(store.DialectPostgres),
		DSN:             metadataDSN,
		MaxOpenConns:    8,
		MaxIdleConns:    4,
		ConnMaxLifetime: config.ConfigDuration(time.Minute),
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NoError(t, runtime.Store.Ping(ctx))

	adminHandler, err := NewHandler(Deps{
		Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: adminPassword,
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
	}, zerolog.Nop())
	require.NoError(t, err)
	handler, err := mcpserver.NewHTTPHandler(
		runtime, cfg, zerolog.Nop(), mcpserver.WithAdminAPI(adminHandler),
	)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	loginBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/auth/login", "", map[string]any{"username": "admin", "password": adminPassword})
	var login adminE2EEnvelope[struct {
		Token string `json:"token"`
	}]
	require.NoError(t, json.Unmarshal(loginBody, &login))
	require.Zero(t, login.Code)
	require.NotEmpty(t, login.Data.Token)
	adminAuthorization := "Bearer " + login.Data.Token

	notificationDelivered := make(chan struct{}, 16)
	notificationReceiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case notificationDelivered <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(notificationReceiver.Close)
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPut,
		"/api/v1/integrations/notifications", adminAuthorization, map[string]any{
			"enabled": true, "queue_size": 8,
			"channels": []map[string]any{{
				"id": "pg18-webhook", "enabled": true, "kind": "webhook",
				"decisions": []string{"deny"}, "allow_private_endpoints": true,
				"webhook": map[string]any{"template": "generic", "url": notificationReceiver.URL},
			}},
		})
	runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{ID: 3702, TS: time.Now().UTC(), Decision: "deny"}})
	select {
	case <-notificationDelivered:
	case <-time.After(2 * time.Second):
		t.Fatal("postgres:18 combined notification configuration did not deliver")
	}

	datasourceInput := map[string]any{
		"id": "pg-demo", "name": "PostgreSQL 18 Demo", "db_type": "postgres",
		"host": host, "port": port.Int(), "database": businessDatabase,
		"username": username, "password": password, "conn_limit": 5,
		"stmt_timeout_ms": 5000, "row_limit": 100,
	}
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources", adminAuthorization, datasourceInput)
	storedDatasource, err := runtime.Store.Datasources().Get(ctx, "pg-demo")
	require.NoError(t, err)
	require.NotEqual(t, password, storedDatasource.PasswordEnc)
	decrypted, err := runtime.Store.Datasources().DecryptPassword(storedDatasource.PasswordEnc)
	require.NoError(t, err)
	require.Equal(t, password, decrypted)

	agentBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/agents", adminAuthorization,
		map[string]any{"id": "pg-agent", "name": "PostgreSQL E2E Agent", "level": "dml"})
	var createdAgent adminE2EEnvelope[agentView]
	require.NoError(t, json.Unmarshal(agentBody, &createdAgent))
	require.Zero(t, createdAgent.Code)
	require.True(t, strings.HasPrefix(createdAgent.Data.APIKey, "asql_"))

	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/policies", adminAuthorization, map[string]any{
			"id": "pg-customers", "agent_id": "pg-agent", "datasource_id": "pg-demo",
			"object_type": "table", "object_name": "public.customers", "action": "allow",
		})
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", adminAuthorization, map[string]any{
			"id": "pg-email-mask", "datasource_id": "pg-demo", "table_name": "customers",
			"column_name": "email", "sensitive_type": "email", "algo": "mask",
		})
	ruleBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/rules", adminAuthorization, map[string]any{
			"id": "R105", "db_type": "postgres", "title": "Approve unindexed writes",
			"risk_level": 3, "pattern_type": "ast_match", "definition": `{}`, "enabled": true,
		})
	var createdRule adminE2EEnvelope[ruleView]
	require.NoError(t, json.Unmarshal(ruleBody, &createdRule))
	require.True(t, createdRule.Data.Enabled, "PostgreSQL boolean values must scan through the rule repository")

	queryResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, 1,
		"query", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "SELECT id, email, balance FROM public.customers ORDER BY id LIMIT 10",
		})
	require.Equal(t, "allow", queryResponse.Decision)
	queryData := decodeAdminE2EPipelineData(t, queryResponse.Data)
	require.NotNil(t, queryData.Result)
	require.Equal(t, []string{"id", "email", "balance"}, queryData.Result.Columns)
	require.Equal(t, [][]string{{"1", "a***@example.com", "100"}, {"2", "b***@example.com", "200"}}, queryData.Result.Rows)
	require.Equal(t, 2, queryData.Redact.MaskedCells)
	require.Positive(t, queryData.AuditID)

	before := adminE2EBusinessRows(t, ctx, businessPool)
	deniedAuditIDs := make([]int64, 0, 2)
	for id, write := range []struct {
		sql    string
		reason string
	}{
		{sql: "UPDATE public.customers SET balance = 0", reason: "unsafe update regression"},
		{sql: "DELETE FROM public.customers", reason: "unsafe delete regression"},
	} {
		response := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey,
			id+2, "execute_write", map[string]any{
				"datasource_id": "pg-demo", "sql": write.sql, "reason": write.reason,
			})
		require.Equal(t, "deny", response.Decision)
		data := decodeAdminE2EPipelineData(t, response.Data)
		require.Contains(t, adminE2ERuleIDs(data.Hits), "R002")
		require.Positive(t, data.AuditID)
		deniedAuditIDs = append(deniedAuditIDs, data.AuditID)
		require.Equal(t, before, adminE2EBusinessRows(t, ctx, businessPool), "denied SQL must not reach PostgreSQL")
	}

	approvalResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, 4,
		"execute_write", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "UPDATE public.customers SET balance = balance + 1 WHERE id = 1",
			"reason":        "bounded maintenance write",
		})
	require.Equal(t, "approve", approvalResponse.Decision)
	approvalData := decodeAdminE2EPipelineData(t, approvalResponse.Data)
	require.Contains(t, adminE2ERuleIDs(approvalData.Hits), "R105")
	require.NotEmpty(t, approvalData.ApprovalID)
	require.Equal(t, "pending", approvalData.Status)
	require.Positive(t, approvalData.AuditID)
	require.Equal(t, before, adminE2EBusinessRows(t, ctx, businessPool), "approval-gated SQL must not execute")

	pendingBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodGet,
		"/api/v1/approvals?status=pending", adminAuthorization, nil)
	var pending adminE2EEnvelope[adminE2EPage[approvalView]]
	require.NoError(t, json.Unmarshal(pendingBody, &pending))
	require.EqualValues(t, 1, pending.Data.Total)
	require.Len(t, pending.Data.List, 1)
	require.Equal(t, approvalData.ApprovalID, pending.Data.List[0].ID)
	require.NotNil(t, pending.Data.List[0].AuditID)
	require.Equal(t, approvalData.AuditID, *pending.Data.List[0].AuditID)

	decidedBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/approvals/"+approvalData.ApprovalID+"/decide", adminAuthorization,
		map[string]any{"decision": "approve", "comment": "verified in PostgreSQL E2E"})
	var decided adminE2EEnvelope[approvalView]
	require.NoError(t, json.Unmarshal(decidedBody, &decided))
	require.Equal(t, "approved", decided.Data.Status)
	require.NotNil(t, decided.Data.AuditID)
	require.Equal(t, approvalData.AuditID, *decided.Data.AuditID)
	require.NotNil(t, decided.Data.DecidedAt)
	require.False(t, decided.Data.DecidedAt.IsZero())
	require.NotNil(t, decided.Data.Approver)
	require.Equal(t, "admin", *decided.Data.Approver)

	auditBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodGet,
		"/api/v1/audit?agent_id=pg-agent&page_size=100", adminAuthorization, nil)
	var audits adminE2EEnvelope[adminE2EPage[auditView]]
	require.NoError(t, json.Unmarshal(auditBody, &audits))
	require.EqualValues(t, 4, audits.Data.Total)
	require.Len(t, audits.Data.List, 4)
	wantAudits := map[int64]string{
		queryData.AuditID:    "allow",
		deniedAuditIDs[0]:    "deny",
		deniedAuditIDs[1]:    "deny",
		approvalData.AuditID: "approve",
	}
	for _, auditLog := range audits.Data.List {
		require.Equal(t, wantAudits[auditLog.ID], auditLog.Decision)
		require.False(t, auditLog.TS.IsZero())
		delete(wantAudits, auditLog.ID)
	}
	require.Empty(t, wantAudits)

	dashboardBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodGet,
		"/api/v1/dashboard/summary?days=1", adminAuthorization, nil)
	var dashboard adminE2EEnvelope[store.DashboardSummary]
	require.NoError(t, json.Unmarshal(dashboardBody, &dashboard))
	require.Equal(t, int64(4), dashboard.Data.KPI.TotalRequests)
	require.Equal(t, int64(2), dashboard.Data.KPI.Blocked)
	require.Zero(t, dashboard.Data.KPI.PendingApprovals)
	require.Equal(t, int64(1), dashboard.Data.KPI.ActiveAgents)
	require.Equal(t, int64(1), dashboard.Data.KPI.DatasourcesTotal)
	require.Equal(t, map[string]int64{"allow": 1, "deny": 2, "approve": 1, "warn": 0},
		adminE2EDecisionCounts(dashboard.Data.DecisionDistribution))
	require.Len(t, dashboard.Data.Trend14D, 1)
	require.Equal(t, int64(4), dashboard.Data.Trend14D[0].Total)
}

type adminE2EEnvelope[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type adminE2EPage[T any] struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	List     []T   `json:"list"`
}

type adminE2EMCPResponse struct {
	Decision string
	Reason   string
	Data     json.RawMessage
}

type adminE2EPipelineData struct {
	Result     *model.QueryResult `json:"result"`
	Hits       []model.RuleHit    `json:"hits"`
	ApprovalID string             `json:"approval_id"`
	AuditID    int64              `json:"audit_id"`
	Redact     mask.RedactReport  `json:"redact"`
	Status     string             `json:"status"`
}

type adminE2EBusinessRow struct {
	ID      int
	Email   string
	Balance int
}

func adminE2ERequest(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	method string,
	path string,
	authorization string,
	body any,
) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	require.NoError(t, err)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(contents))
	return contents
}

func adminE2EMCPCall(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	apiKey string,
	id int,
	tool string,
	arguments map[string]any,
) adminE2EMCPResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", bytes.NewReader(payload))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(contents))
	var decoded struct {
		Result struct {
			StructuredContent struct {
				Decision string          `json:"decision"`
				Reason   string          `json:"reason"`
				Data     json.RawMessage `json:"data"`
			} `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(contents, &decoded))
	require.Nil(t, decoded.Error, string(contents))
	require.NotEmpty(t, decoded.Result.StructuredContent.Decision, string(contents))
	return adminE2EMCPResponse{
		Decision: decoded.Result.StructuredContent.Decision,
		Reason:   decoded.Result.StructuredContent.Reason,
		Data:     decoded.Result.StructuredContent.Data,
	}
}

func decodeAdminE2EPipelineData(t *testing.T, encoded json.RawMessage) adminE2EPipelineData {
	t.Helper()
	var data adminE2EPipelineData
	require.NoError(t, json.Unmarshal(encoded, &data))
	return data
}

func adminE2ERuleIDs(hits []model.RuleHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.RuleID)
	}
	return ids
}

func adminE2EBusinessRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []adminE2EBusinessRow {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT id, email, balance FROM public.customers ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	result := make([]adminE2EBusinessRow, 0, 2)
	for rows.Next() {
		var row adminE2EBusinessRow
		require.NoError(t, rows.Scan(&row.ID, &row.Email, &row.Balance))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

func adminE2EDecisionCounts(items []store.DecisionCount) map[string]int64 {
	counts := make(map[string]int64, len(items))
	for _, item := range items {
		counts[item.Decision] = item.Count
	}
	return counts
}

func adminDockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeAdminDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func probeAdminDockerAvailable() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed
		}
	}()
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dockerClient, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err := dockerClient.Ping(probeContext); err != nil {
		_ = dockerClient.Close()
		return adminDockerDaemonUnavailable(err), err
	}
	return false, dockerClient.Close()
}

func adminDockerDaemonUnavailable(err error) bool {
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
