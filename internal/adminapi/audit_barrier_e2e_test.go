package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestThreePostgresAuditOutageFailClosedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("three-postgres audit outage E2E is an integration test")
	}
	ctx := adminDockerTestContext(t)
	const (
		username      = "agentsql"
		password      = "agentsql-three-pg-password"
		adminPassword = "agentsql-three-pg-admin"
	)
	metaContainer, metaDSN := startAuditBarrierPostgres(t, ctx, "metadata", username, password)
	auditContainer, auditDSN := startAuditBarrierPostgres(t, ctx, "audit", username, password)
	_, businessDSN := startAuditBarrierPostgres(t, ctx, "business", username, password)
	_ = metaContainer

	metaPool, err := pgxpool.New(ctx, metaDSN)
	require.NoError(t, err)
	t.Cleanup(metaPool.Close)
	auditPool, err := pgxpool.New(ctx, auditDSN)
	require.NoError(t, err)
	t.Cleanup(auditPool.Close)
	businessPool, err := pgxpool.New(ctx, businessDSN)
	require.NoError(t, err)
	t.Cleanup(businessPool.Close)
	_, err = businessPool.Exec(ctx, `CREATE TABLE public.accounts (
  id integer PRIMARY KEY,
  balance integer NOT NULL
)`)
	require.NoError(t, err)
	_, err = businessPool.Exec(ctx, "INSERT INTO public.accounts(id,balance) VALUES (1,100),(2,200)")
	require.NoError(t, err)

	cfg := adminTestConfig("")
	cfg.Defaults.QPSPerAgent = 100
	cfg.Store = config.StoreConfig{
		AutoMigrate: true,
		Metadata: &config.MetadataStoreConfig{
			Driver: string(store.DialectPostgres), DSN: metaDSN,
			MaxOpenConns: 8, MaxIdleConns: 4, ConnMaxLifetime: config.ConfigDuration(time.Minute),
		},
		Audit: &config.AuditStoreConfig{
			Separate: true, Driver: string(store.DialectPostgres), DSN: auditDSN,
			MaxOpenConns: 8, MaxIdleConns: 4,
		},
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	adminHandler, err := NewHandler(Deps{
		Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: adminPassword,
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
	}, zerolog.Nop())
	require.NoError(t, err)
	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(), mcpserver.WithAdminAPI(adminHandler))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	loginBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/auth/login", "", map[string]any{"username": "admin", "password": adminPassword})
	var login adminE2EEnvelope[struct {
		Token string `json:"token"`
	}]
	require.NoError(t, json.Unmarshal(loginBody, &login))
	authorization := "Bearer " + login.Data.Token

	businessConfig, err := pgxpool.ParseConfig(businessDSN)
	require.NoError(t, err)
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources", authorization, map[string]any{
			"id": "barrier-business", "name": "Barrier Business", "db_type": "postgres",
			"host": businessConfig.ConnConfig.Host, "port": int(businessConfig.ConnConfig.Port),
			"database": businessConfig.ConnConfig.Database, "username": businessConfig.ConnConfig.User,
			"password": password, "conn_limit": 5, "stmt_timeout_ms": 500, "row_limit": 100,
		})
	dmlKey := createAuditBarrierAgent(t, ctx, server, authorization, "barrier-dml", "dml")
	ddlKey := createAuditBarrierAgent(t, ctx, server, authorization, "barrier-ddl", "ddl")
	dmlSession := adminE2EMCPInitialize(t, ctx, server.Client(), server.URL, dmlKey, 100)
	ddlSession := adminE2EMCPInitialize(t, ctx, server.Client(), server.URL, ddlKey, 100)
	for _, policy := range []map[string]any{
		{"id": "barrier-dml-policy", "agent_id": "barrier-dml", "datasource_id": "barrier-business", "object_type": "table", "object_name": "public.accounts", "action": "allow"},
		{"id": "barrier-ddl-policy", "agent_id": "barrier-ddl", "datasource_id": "barrier-business", "object_type": "table", "object_name": "public.audit_outage_object", "action": "allow"},
	} {
		adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
			"/api/v1/policies", authorization, policy)
	}

	before := auditBarrierBusinessRows(t, ctx, businessPool)
	var approvalsBefore int64
	require.NoError(t, metaPool.QueryRow(ctx, "SELECT COUNT(*) FROM approvals").Scan(&approvalsBefore))
	// Inject an audit-DB outage by freezing the audit container with
	// `docker pause` (SIGSTOP of its processes). The container stays up and the
	// ephemeral host-port mapping is preserved: on Docker Desktop/WSL2 a container
	// stop+start does not reliably re-forward that ephemeral host port, which would
	// block recovery through no fault of the gateway. Pause models a hung database
	// / network partition where in-flight connections stall rather than get
	// refused — the stricter test of the bounded-timeout fail-closed barrier;
	// unpause afterwards proves self-healing over the same DSN.
	dockerProvider, err := testcontainers.NewDockerProvider()
	require.NoError(t, err)
	dockerCli := dockerProvider.Client()
	auditContainerID := auditContainer.GetContainerID()
	t.Cleanup(func() { _ = dockerCli.ContainerUnpause(context.Background(), auditContainerID) })
	require.NoError(t, dockerCli.ContainerPause(ctx, auditContainerID))
	waitForAuditBarrier(t, 20*time.Second, func(attempt context.Context) bool {
		return runtime.Store.Ping(attempt) != nil
	}, "store ping to fail after audit shutdown")
	waitForAuditBarrier(t, 30*time.Second, func(attempt context.Context) bool {
		// The server waits up to 1s on the stalled audit DB before answering 503,
		// so the probe's HTTP client must allow longer than the 500ms poll tick.
		reqCtx, cancelReq := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelReq()
		request, requestErr := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/readyz", nil)
		if requestErr != nil {
			return false
		}
		response, requestErr := server.Client().Do(request)
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusServiceUnavailable
	}, "readiness to become unavailable")

	queryFailure := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 101,
		"query", map[string]any{"datasource_id": "barrier-business", "sql": "SELECT id,balance FROM public.accounts ORDER BY id LIMIT 10"})
	require.Equal(t, "error", queryFailure.Decision)
	require.Empty(t, queryFailure.Data)
	require.NotContains(t, queryFailure.Reason, auditDSN)
	require.NotContains(t, queryFailure.Reason, password)

	writeFailure := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 102,
		"execute_write", map[string]any{
			"datasource_id": "barrier-business", "sql": "UPDATE public.accounts SET balance=balance+10 WHERE id=1", "reason": "audit outage test",
		})
	require.Equal(t, "error", writeFailure.Decision)
	require.Equal(t, before, auditBarrierBusinessRows(t, ctx, businessPool))

	approvalFailure := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 103,
		"request_approval", map[string]any{
			"datasource_id": "barrier-business", "sql": "UPDATE public.accounts SET balance=balance+1 WHERE id=2", "reason": "audit outage approval",
		})
	require.Equal(t, "error", approvalFailure.Decision)
	var approvalsAfter int64
	require.NoError(t, metaPool.QueryRow(ctx, "SELECT COUNT(*) FROM approvals").Scan(&approvalsAfter))
	require.Equal(t, approvalsBefore, approvalsAfter)

	ddlFailure := adminE2EMCPCall(t, ctx, server.Client(), server.URL, ddlKey, ddlSession, 104,
		"execute_write", map[string]any{
			"datasource_id": "barrier-business", "sql": "CREATE TABLE public.audit_outage_object(id integer)", "reason": "audit outage DDL",
		})
	require.Equal(t, "error", ddlFailure.Decision)
	var objectExists bool
	require.NoError(t, businessPool.QueryRow(ctx, "SELECT to_regclass('public.audit_outage_object') IS NOT NULL").Scan(&objectExists))
	require.False(t, objectExists)

	// Unfreeze the audit database; the DSN/port never changed, so the gateway's
	// existing connection pools must self-heal after the stalled connections time out.
	require.NoError(t, dockerCli.ContainerUnpause(ctx, auditContainerID))
	waitForAuditBarrier(t, 30*time.Second, func(attempt context.Context) bool {
		return runtime.Store.Ping(attempt) == nil
	}, "store ping to recover after audit restart")
	waitForAuditBarrier(t, 30*time.Second, func(attempt context.Context) bool {
		reqCtx, cancelReq := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelReq()
		request, requestErr := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/readyz", nil)
		if requestErr != nil {
			return false
		}
		response, requestErr := server.Client().Do(request)
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, "readiness to recover")

	querySuccess := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 105,
		"query", map[string]any{"datasource_id": "barrier-business", "sql": "SELECT id,balance FROM public.accounts ORDER BY id LIMIT 10"})
	require.Equal(t, "allow", querySuccess.Decision)
	writeSuccess := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 106,
		"execute_write", map[string]any{
			"datasource_id": "barrier-business", "sql": "UPDATE public.accounts SET balance=balance+10 WHERE id=1", "reason": "audit recovered",
		})
	require.Equal(t, "allow", writeSuccess.Decision)
	approvalSuccess := adminE2EMCPCall(t, ctx, server.Client(), server.URL, dmlKey, dmlSession, 107,
		"request_approval", map[string]any{
			"datasource_id": "barrier-business", "sql": "UPDATE public.accounts SET balance=balance+1 WHERE id=2", "reason": "audit recovered approval",
		})
	require.Equal(t, "approve", approvalSuccess.Decision)
	approvalData := decodeAdminE2EPipelineData(t, approvalSuccess.Data)
	require.NotEmpty(t, approvalData.ApprovalID)
	require.Positive(t, approvalData.AuditID)
	var linkedAuditID int64
	require.NoError(t, metaPool.QueryRow(ctx, "SELECT audit_id FROM approvals WHERE id=$1", approvalData.ApprovalID).Scan(&linkedAuditID))
	require.Equal(t, approvalData.AuditID, linkedAuditID)
	var auditCount int64
	require.NoError(t, auditPool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&auditCount))
	require.GreaterOrEqual(t, auditCount, int64(3))
}

func startAuditBarrierPostgres(t *testing.T, ctx context.Context, database, username, password string) (testcontainers.Container, string) {
	t.Helper()
	container, err := postgrescontainer.Run(ctx, "postgres:18",
		postgrescontainer.WithDatabase(database), postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start postgres:18 %s container after Docker probe", database)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return container, fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), database)
}

func createAuditBarrierAgent(t *testing.T, ctx context.Context, server *httptest.Server, authorization, id, level string) string {
	t.Helper()
	body := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/agents", authorization, map[string]any{"id": id, "name": id, "level": level})
	var created adminE2EEnvelope[agentView]
	require.NoError(t, json.Unmarshal(body, &created))
	require.True(t, strings.HasPrefix(created.Data.APIKey, "asql_"))
	return created.Data.APIKey
}

func auditBarrierBusinessRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[int]int {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT id,balance FROM public.accounts ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	result := make(map[int]int)
	for rows.Next() {
		var id, balance int
		require.NoError(t, rows.Scan(&id, &balance))
		result[id] = balance
	}
	require.NoError(t, rows.Err())
	return result
}

func waitForAuditBarrier(t *testing.T, timeout time.Duration, condition func(context.Context) bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		attempt, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		matched := condition(attempt)
		cancel()
		if matched {
			return
		}
		timer := time.NewTimer(100 * time.Millisecond)
		<-timer.C
	}
	t.Fatalf("timed out waiting for %s", description)
}
