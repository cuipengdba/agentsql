package adminapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

// vendorAuditFaultTarget points only at a seeded, isolated vendor instance.
// Credentials are provided at runtime and must never be committed to the repo.
type vendorAuditFaultTarget struct {
	Name       string `json:"name"`
	DBType     string `json:"db_type"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Database   string `json:"database"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	ObjectName string `json:"object_name"`
	TableName  string `json:"table_name"`
	ColumnName string `json:"column_name"`
	SQL        string `json:"sql"`
	ClearValue string `json:"clear_value"`
	MaskValue  string `json:"mask_value"`
	AdminUser  string `json:"admin_user,omitempty"`
	AdminPass  string `json:"admin_pass,omitempty"`
	LockSQL    string `json:"lock_sql,omitempty"`
}

// TestVendorIndependentAuditFaultE2E tests the *same* MCP read against each
// real vendor kernel before, during and after an independent audit DB outage.
// It is deliberately opt-in because vendor images/licenses are not CI fixtures.
func TestVendorIndependentAuditFaultE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("vendor instances are required")
	}
	raw := os.Getenv("AGENTSQL_VENDOR_AUDIT_FAULT_TARGETS")
	if raw == "" {
		t.Skip("set AGENTSQL_VENDOR_AUDIT_FAULT_TARGETS to an isolated target JSON array")
	}
	var targets []vendorAuditFaultTarget
	require.NoError(t, json.Unmarshal([]byte(raw), &targets))
	require.NotEmpty(t, targets)
	for _, target := range targets {
		if target.Name == "" || target.Host == "" || target.Port <= 0 ||
			target.Database == "" || target.Username == "" || target.Password == "" ||
			target.ObjectName == "" || target.TableName == "" || target.ColumnName == "" ||
			target.SQL == "" || target.ClearValue == "" || target.MaskValue == "" {
			t.Fatal("vendor audit target is incomplete")
		}
		if target.LockSQL != "" && (target.AdminUser == "" || target.AdminPass == "") {
			t.Fatal("vendor lock target is missing administrator credentials")
		}
		switch target.DBType {
		case "dm", "postgres", "vastbase":
		default:
			t.Fatal("unsupported vendor audit target database type")
		}
	}

	ctx := adminDockerTestContext(t, 10*time.Minute)
	const username = "agentsql"
	const password = "agentsql-vendor-fault-fixture"
	metaContainer, metaDSN := startAuditBarrierPostgres(t, ctx, "metadata", username, password)
	auditContainer, auditDSN := startAuditBarrierPostgres(t, ctx, "audit", username, password)
	_ = metaContainer
	auditDB, err := sql.Open("pgx", auditDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditDB.Close()) })

	cfg := adminTestConfig("")
	cfg.Defaults.QPSPerAgent = 100
	cfg.Store = config.StoreConfig{
		AutoMigrate: true,
		Metadata: &config.MetadataStoreConfig{Driver: string(store.DialectPostgres), DSN: metaDSN,
			MaxOpenConns: 8, MaxIdleConns: 4},
		Audit: &config.AuditStoreConfig{Separate: true, Driver: string(store.DialectPostgres), DSN: auditDSN,
			MaxOpenConns: 8, MaxIdleConns: 4},
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	adminHandler, err := NewHandler(Deps{Runtime: runtime, Config: cfg,
		AdminUsername: "admin", AdminPassword: "agentsql-vendor-fault-admin",
		TokenKey: DeriveTokenKey([]byte(adminTestSecret))}, zerolog.Nop())
	require.NoError(t, err)
	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(), mcpserver.WithAdminAPI(adminHandler))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	loginBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "agentsql-vendor-fault-admin"})
	var login adminE2EEnvelope[struct {
		Token string `json:"token"`
	}]
	require.NoError(t, json.Unmarshal(loginBody, &login))
	authorization := "Bearer " + login.Data.Token

	dockerProvider, err := testcontainers.NewDockerProvider()
	require.NoError(t, err)
	dockerClient := dockerProvider.Client()
	auditContainerID := auditContainer.GetContainerID()
	t.Cleanup(func() { _ = dockerClient.ContainerUnpause(context.Background(), auditContainerID) })

	for index, target := range targets {
		t.Run(target.Name, func(t *testing.T) {
			id := "vendor-fault-" + target.Name
			statementTimeoutMS := 5000
			if target.LockSQL != "" {
				statementTimeoutMS = 1200
			}
			adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
				"/api/v1/datasources", authorization, map[string]any{
					"id": id, "name": target.Name, "db_type": target.DBType,
					"host": target.Host, "port": target.Port, "database": target.Database,
					"username": target.Username, "password": target.Password,
					"conn_limit": 2, "stmt_timeout_ms": statementTimeoutMS, "row_limit": 10,
				})
			key := createAuditBarrierAgent(t, ctx, server, authorization, id+"-agent", "readonly")
			adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
				"/api/v1/policies", authorization, map[string]any{
					"id": id + "-policy", "agent_id": id + "-agent", "datasource_id": id,
					"object_type": "table", "object_name": target.ObjectName, "action": "allow",
				})
			adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
				"/api/v1/mask_rules", authorization, map[string]any{
					"id": id + "-mask", "datasource_id": id, "table_name": target.TableName,
					"column_name": target.ColumnName, "sensitive_type": "phone", "algo": "mask",
				})
			session := adminE2EMCPInitialize(t, ctx, server.Client(), server.URL, key, 100+index*10)
			call := func(idNumber int) adminE2EMCPResponse {
				return adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, idNumber,
					"query", map[string]any{"datasource_id": id, "sql": target.SQL})
			}
			baseline := call(101 + index*10)
			require.Equal(t, "allow", baseline.Decision, "baseline reason=%s", baseline.Reason)
			require.Contains(t, string(baseline.Data), target.MaskValue)
			require.NotContains(t, string(baseline.Data), target.ClearValue)
			baselineData := decodeAdminE2EPipelineData(t, baseline.Data)
			require.Positive(t, baselineData.AuditID)
			var before int64
			require.NoError(t, auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&before))

			require.NoError(t, dockerClient.ContainerPause(ctx, auditContainerID))
			t.Cleanup(func() { _ = dockerClient.ContainerUnpause(context.Background(), auditContainerID) })
			waitForAuditBarrier(t, 20*time.Second, func(attempt context.Context) bool {
				return runtime.Store.Ping(attempt) != nil
			}, "independent audit store outage")
			failure := call(102 + index*10)
			require.Equal(t, "error", failure.Decision)
			require.Empty(t, failure.Data)
			require.NotContains(t, failure.Reason, target.ClearValue)
			require.NotContains(t, failure.Reason, target.Password)

			require.NoError(t, dockerClient.ContainerUnpause(ctx, auditContainerID))
			waitForAuditBarrier(t, 30*time.Second, func(attempt context.Context) bool {
				return runtime.Store.Ping(attempt) == nil
			}, "independent audit store recovery")
			recovered := call(103 + index*10)
			require.Equal(t, "allow", recovered.Decision, "recovery reason=%s", recovered.Reason)
			require.Contains(t, string(recovered.Data), target.MaskValue)
			require.NotContains(t, string(recovered.Data), target.ClearValue)
			recoveredData := decodeAdminE2EPipelineData(t, recovered.Data)
			require.Greater(t, recoveredData.AuditID, baselineData.AuditID)
			var decision, datasourceID string
			require.NoError(t, auditDB.QueryRowContext(ctx,
				"SELECT decision, datasource_id FROM audit_logs WHERE id=$1", recoveredData.AuditID).
				Scan(&decision, &datasourceID))
			require.Equal(t, "allow", decision)
			require.Equal(t, id, datasourceID)
			var after int64
			require.NoError(t, auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&after))
			require.GreaterOrEqual(t, after, before+1)
			if target.LockSQL != "" {
				verifyVendorBlockedRead(t, ctx, target, server, authorization, key, session, auditDB)
			}
		})
	}
}

func verifyVendorBlockedRead(t *testing.T, ctx context.Context, target vendorAuditFaultTarget,
	server *httptest.Server, authorization, apiKey, session string, auditDB *sql.DB) {
	t.Helper()
	var adminDriver, adminDSN, activeSQL string
	switch target.DBType {
	case "dm":
		adminDriver = "dm"
		adminDSN = "dm://" + target.AdminUser + ":" + target.AdminPass + "@" +
			net.JoinHostPort(target.Host, strconv.Itoa(target.Port)) + "?schema=" + target.Database
		activeSQL = "SELECT COUNT(*) FROM V$SESSIONS WHERE USER_NAME=? AND STATE='ACTIVE'"
	default:
		adminDriver = "pgx"
		address := url.URL{Scheme: "postgres", User: url.UserPassword(target.AdminUser, target.AdminPass),
			Host: net.JoinHostPort(target.Host, strconv.Itoa(target.Port)), Path: "/" + target.Database}
		adminDSN = address.String() + "?sslmode=disable"
		activeSQL = "SELECT count(*) FROM pg_stat_activity WHERE usename=$1 AND state='active'"
	}
	adminDB, err := sql.Open(adminDriver, adminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminDB.Close()) })
	adminDB.SetMaxOpenConns(2)
	require.NoError(t, adminDB.PingContext(ctx))
	lock, err := adminDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Rollback() })
	_, err = lock.ExecContext(ctx, target.LockSQL)
	require.NoError(t, err)
	activeCount := func() int {
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var count int
		if scanErr := adminDB.QueryRowContext(check, activeSQL, target.Username).Scan(&count); scanErr != nil {
			return -1
		}
		return count
	}
	var precedingAuditID int64
	require.NoError(t, auditDB.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(id),0) FROM audit_logs WHERE datasource_id=$1", "vendor-fault-"+target.Name).
		Scan(&precedingAuditID))

	started := time.Now()
	timedOut := adminE2EMCPCall(t, ctx, server.Client(), server.URL, apiKey, session, 500,
		"query", map[string]any{"datasource_id": "vendor-fault-" + target.Name, "sql": target.SQL})
	require.Equal(t, "error", timedOut.Decision, "blocked query reason=%s", timedOut.Reason)
	require.Empty(t, timedOut.Data)
	require.NotContains(t, timedOut.Reason, target.ClearValue)
	require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond,
		"the query should reach the held vendor lock before timing out")
	require.Eventually(t, func() bool { return activeCount() == 0 }, 5*time.Second, 100*time.Millisecond,
		"timed-out vendor query must stop while the lock is still held")
	precedingAuditID = requireVendorFaultAudit(t, ctx, auditDB, "vendor-fault-"+target.Name, precedingAuditID, "DB_QUERY_TIMEOUT")
	// A separately configured datasource proves cancellation before its own
	// five-second timeout, without relying on live executor reconfiguration.
	cancelID := "vendor-fault-cancel-" + target.Name
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources", authorization, map[string]any{
			"id": cancelID, "name": target.Name + " cancel", "db_type": target.DBType,
			"host": target.Host, "port": target.Port, "database": target.Database,
			"username": target.Username, "password": target.Password,
			"conn_limit": 2, "stmt_timeout_ms": 5000, "row_limit": 10,
		})
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/policies", authorization, map[string]any{
			"id": cancelID + "-policy", "agent_id": "vendor-fault-" + target.Name + "-agent",
			"datasource_id": cancelID, "object_type": "table",
			"object_name": target.ObjectName, "action": "allow",
		})
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", authorization, map[string]any{
			"id": cancelID + "-mask", "datasource_id": cancelID,
			"table_name": target.TableName, "column_name": target.ColumnName,
			"sensitive_type": "phone", "algo": "mask",
		})

	callContext, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	cancelStarted := time.Now()
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 501, "method": "tools/call",
		"params": map[string]any{"name": "query", "arguments": map[string]any{
			"datasource_id": cancelID, "sql": target.SQL}, "_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			"io.modelcontextprotocol/clientInfo": map[string]any{
				"name": "agentsql-vendor-fault-test", "version": "1",
			},
		}}})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(callContext, http.MethodPost,
		server.URL+"/mcp/2026-07-28", bytes.NewReader(payload))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "query")
	type callResult struct {
		body []byte
		err  error
	}
	completed := make(chan callResult, 1)
	go func() {
		response, callErr := server.Client().Do(request)
		if callErr != nil {
			completed <- callResult{err: callErr}
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		completed <- callResult{body: body, err: readErr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for activeCount() == 0 {
		select {
		case result := <-completed:
			t.Fatalf("cancellation probe finished before vendor execution: %v", result.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation probe did not reach an active vendor query")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancelCall()
	select {
	case result := <-completed:
		require.NotContains(t, string(result.body), target.ClearValue)
		require.NotContains(t, string(result.body), `"decision":"allow"`)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled MCP request did not terminate")
	}
	require.Eventually(t, func() bool { return activeCount() == 0 }, 5*time.Second, 100*time.Millisecond,
		"canceled vendor query must stop while the lock is still held")
	require.Less(t, time.Since(cancelStarted), 3*time.Second,
		"client cancellation must stop the vendor query before its 5-second statement timeout")
	requireVendorFaultAudit(t, ctx, auditDB, cancelID, 0, "DB_QUERY_INTERRUPTED")
	require.NoError(t, lock.Rollback())
	recovered := adminE2EMCPCall(t, ctx, server.Client(), server.URL, apiKey, session, 502,
		"query", map[string]any{"datasource_id": "vendor-fault-" + target.Name, "sql": target.SQL})
	require.Equal(t, "allow", recovered.Decision, "post-timeout recovery reason=%s", recovered.Reason)
	require.Contains(t, string(recovered.Data), target.MaskValue)
	require.NotContains(t, string(recovered.Data), target.ClearValue)
	var auditCount int64
	require.NoError(t, auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&auditCount))
	require.Positive(t, auditCount)
}

func requireVendorFaultAudit(t *testing.T, ctx context.Context, auditDB *sql.DB,
	datasourceID string, afterID int64, wantCode string) int64 {
	t.Helper()
	var auditID int64
	var errorCode sql.NullString
	require.Eventually(t, func() bool {
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return auditDB.QueryRowContext(check,
			"SELECT id,error_code FROM audit_logs WHERE datasource_id=$1 AND id>$2 AND decision='error' ORDER BY id LIMIT 1",
			datasourceID, afterID).Scan(&auditID, &errorCode) == nil
	}, 5*time.Second, 100*time.Millisecond, "missing persisted error audit after vendor fault")
	require.True(t, errorCode.Valid, "error audit must carry a stable code")
	require.Equal(t, wantCode, errorCode.String)
	return auditID
}
