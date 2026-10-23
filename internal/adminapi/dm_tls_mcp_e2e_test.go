package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	_ "github.com/godoes/gorm-dameng/dm8"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestDMVerifiedTLSMCPE2E uses a licensed, isolated DM8 instance. It proves
// the authenticated transport and the MCP authorization/masking/audit chain.
func TestDMVerifiedTLSMCPE2E(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTSQL_DM_TLS_E2E") != "1" {
		t.Skip("set AGENTSQL_DM_TLS_E2E=1 for the isolated licensed DM8 instance")
	}
	password := os.Getenv("DM_PASSWORD")
	if password == "" {
		password = os.Getenv("SYSDBA_PWD")
	}
	ca, name := os.Getenv("DM_TLS_CA_FILE"), os.Getenv("DM_TLS_SERVER_NAME")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	require.NotEmpty(t, name)
	host := os.Getenv("DM_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := fmt.Sprintf("dm://SYSDBA:%s@%s?schema=SYSDBA&connectTimeout=5000&sslCaPath=%s&sslServerName=%s",
		password, net.JoinHostPort(host, "5236"), ca, name)
	admin, err := sql.Open("dm", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	require.NoError(t, admin.PingContext(ctx))
	proxy, err := newDME2EFaultProxy(net.JoinHostPort(host, "5236"))
	require.NoError(t, err)
	t.Cleanup(proxy.Close)

	suffix := fmt.Sprintf("%X", time.Now().UnixNano())
	table, reader := "AGSQL_V330_"+suffix, "AGSQL_V330_R_"+suffix
	readerPassword := "Aa1!V330" + suffix
	_, err = admin.ExecContext(ctx, "CREATE TABLE SYSDBA."+table+" (ID INT PRIMARY KEY, PHONE VARCHAR(20))")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if _, dropErr := admin.ExecContext(cleanup, "DROP USER "+reader+" CASCADE"); dropErr != nil {
			t.Errorf("drop DM fixture reader: %v", dropErr)
		}
		if _, dropErr := admin.ExecContext(cleanup, "DROP TABLE SYSDBA."+table); dropErr != nil {
			t.Errorf("drop DM fixture table: %v", dropErr)
		}
	})
	_, err = admin.ExecContext(ctx, "INSERT INTO SYSDBA."+table+" (ID,PHONE) VALUES (1,'13800135678')")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "CREATE USER "+reader+` IDENTIFIED BY "`+readerPassword+`"`)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "GRANT CREATE SESSION TO "+reader)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "GRANT SELECT ON SYSDBA."+table+" TO "+reader)
	require.NoError(t, err)

	cfg := adminTestConfig(filepath.Join(t.TempDir(), "metadata.db"))
	cfg.Defaults.QPSPerAgent = 100
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	adminHandler, err := NewHandler(Deps{Runtime: runtime, Config: cfg,
		AdminUsername: "admin", AdminPassword: "dm-v330-e2e-admin",
		TokenKey: DeriveTokenKey([]byte(adminTestSecret))}, zerolog.Nop())
	require.NoError(t, err)
	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(), mcpserver.WithAdminAPI(adminHandler))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	loginBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "dm-v330-e2e-admin"})
	var login adminE2EEnvelope[struct {
		Token string `json:"token"`
	}]
	require.NoError(t, json.Unmarshal(loginBody, &login))
	authorization := "Bearer " + login.Data.Token
	const datasourceID = "dm-v330-tls-e2e"
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources", authorization, map[string]any{
			"id": datasourceID, "name": "DM v3.3 TLS E2E", "db_type": "dm", "host": host,
			"port": proxy.Port(), "database": "SYSDBA", "username": reader, "password": readerPassword,
			"tls_mode": "verify-full", "tls_ca_file": ca, "tls_server_name": name,
			"conn_limit": 2, "stmt_timeout_ms": 20000, "row_limit": 10,
		})
	key := createAuditBarrierAgent(t, ctx, server, authorization, "dm-v330-agent", "readonly")
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/policies", authorization, map[string]any{
			"id": "dm-v330-policy", "agent_id": "dm-v330-agent", "datasource_id": datasourceID,
			"object_type": "table", "object_name": "SYSDBA." + table, "action": "allow",
		})
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", authorization, map[string]any{
			"id": "dm-v330-mask", "datasource_id": datasourceID,
			"table_name": table, "column_name": "PHONE", "sensitive_type": "phone", "algo": "mask",
		})
	session := adminE2EMCPInitialize(t, ctx, server.Client(), server.URL, key, 100)
	allowed := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 101,
		"query", map[string]any{"datasource_id": datasourceID,
			"sql": "SELECT PHONE FROM SYSDBA." + table + " WHERE ID=1 LIMIT 1"})
	if allowed.Decision != "allow" {
		t.Logf("DM MCP diagnostic: code=%s stage=%s", allowed.ErrorCode, allowed.ErrorStage)
		page, pageErr := runtime.Store.AuditLogs().FilteredPage(ctx,
			model.AuditFilter{DatasourceID: stringPointerAdmin(datasourceID)}, 1, 20)
		if pageErr == nil {
			for _, item := range page.List {
				code := ""
				if item.ErrorCode != nil {
					code = *item.ErrorCode
				}
				t.Logf("DM MCP diagnostic: audit=%d decision=%s code=%s", item.ID, item.Decision, code)
			}
		}
	}
	require.Equal(t, "allow", allowed.Decision, allowed.Reason)
	require.Contains(t, string(allowed.Data), "138****5678")
	require.NotContains(t, string(allowed.Data), "13800135678")
	allowAudit := decodeAdminE2EPipelineData(t, allowed.Data).AuditID
	require.Positive(t, allowAudit)

	denied := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 102,
		"query", map[string]any{"datasource_id": datasourceID,
			"sql": "DELETE FROM SYSDBA." + table + " WHERE ID=1"})
	require.NotEqual(t, "allow", denied.Decision)
	require.NotContains(t, string(denied.Data), "13800135678")
	page, err := runtime.Store.AuditLogs().FilteredPage(ctx,
		model.AuditFilter{DatasourceID: stringPointerAdmin(datasourceID)}, 1, 20)
	require.NoError(t, err)
	var foundAllow, foundDenied bool
	for _, item := range page.List {
		if item.ID == allowAudit {
			require.Equal(t, "allow", item.Decision)
			foundAllow = true
		}
		if item.ID != allowAudit && item.Decision == denied.Decision {
			foundDenied = true
		}
	}
	require.True(t, foundAllow && foundDenied, "allow and deny must be durably audited")

	outsidePolicy := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 103,
		"query", map[string]any{"datasource_id": datasourceID, "sql": "SELECT USER FROM SYS.DUAL"})
	require.NotEqual(t, "allow", outsidePolicy.Decision, "an ungranted table must fail closed")

	// Exercise the exact MCP path with concurrent requests at the datasource's
	// two-connection pool limit. Every accepted response must remain masked and
	// carry a durable audit ID; latencies are emitted for the acceptance record.
	var latencies []time.Duration
	var latencyMu sync.Mutex
	t.Run("bounded_load", func(t *testing.T) {
		semaphore := make(chan struct{}, 2)
		for index := range 12 {
			t.Run(fmt.Sprintf("query_%02d", index), func(t *testing.T) {
				t.Parallel()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				started := time.Now()
				result := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 200+index,
					"query", map[string]any{"datasource_id": datasourceID,
						"sql": "SELECT PHONE FROM SYSDBA." + table + " WHERE ID=1 LIMIT 1"})
				elapsed := time.Since(started)
				require.Equal(t, "allow", result.Decision, result.Reason)
				require.Contains(t, string(result.Data), "138****5678")
				require.Positive(t, decodeAdminE2EPipelineData(t, result.Data).AuditID)
				latencyMu.Lock()
				latencies = append(latencies, elapsed)
				latencyMu.Unlock()
			})
		}
	})
	require.Len(t, latencies, 12)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("DM MCP bounded load: requests=%d concurrency=2 pool_limit=2 errors=0 p50=%s p95=%s p99=%s",
		len(latencies), latencies[5], latencies[11], latencies[11])

	beforeFailure, err := runtime.Store.AuditLogs().FilteredPage(ctx,
		model.AuditFilter{DatasourceID: stringPointerAdmin(datasourceID)}, 1, 1)
	require.NoError(t, err)
	require.NotEmpty(t, beforeFailure.List)
	previousAuditID := beforeFailure.List[0].ID
	proxy.Block(true)
	unavailable := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 300,
		"query", map[string]any{"datasource_id": datasourceID,
			"sql": "SELECT PHONE FROM SYSDBA." + table + " WHERE ID=1 LIMIT 1"})
	require.NotEqual(t, "allow", unavailable.Decision, "transport outage must fail closed")
	require.NotContains(t, string(unavailable.Data), "13800135678")
	afterFailure, err := runtime.Store.AuditLogs().FilteredPage(ctx,
		model.AuditFilter{DatasourceID: stringPointerAdmin(datasourceID)}, 1, 5)
	require.NoError(t, err)
	var failureAudited bool
	for _, item := range afterFailure.List {
		if item.ID > previousAuditID && item.Decision == unavailable.Decision {
			failureAudited = true
		}
	}
	require.True(t, failureAudited, "transport failure must be durably audited")
	proxy.Block(false)
	recovered := adminE2EMCPCall(t, ctx, server.Client(), server.URL, key, session, 301,
		"query", map[string]any{"datasource_id": datasourceID,
			"sql": "SELECT PHONE FROM SYSDBA." + table + " WHERE ID=1 LIMIT 1"})
	require.Equal(t, "allow", recovered.Decision, recovered.Reason)
	require.Contains(t, string(recovered.Data), "138****5678")
	require.Positive(t, decodeAdminE2EPipelineData(t, recovered.Data).AuditID)
	t.Log("DM MCP network outage failed closed; the recovered verified connection was masked and audited")
}

// dmE2EFaultProxy relays raw bytes so the DM driver's real certificate check
// remains intact. Block closes active sockets and refuses new ones.
type dmE2EFaultProxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	blocked  bool
	active   map[net.Conn]struct{}
}

func newDME2EFaultProxy(target string) (*dmE2EFaultProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &dmE2EFaultProxy{listener: listener, target: target, active: make(map[net.Conn]struct{})}
	go proxy.accept()
	return proxy, nil
}

func (proxy *dmE2EFaultProxy) Port() int {
	_, port, _ := net.SplitHostPort(proxy.listener.Addr().String())
	value, _ := strconv.Atoi(port)
	return value
}

func (proxy *dmE2EFaultProxy) accept() {
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		go proxy.relay(client)
	}
}

func (proxy *dmE2EFaultProxy) relay(client net.Conn) {
	upstream, err := net.DialTimeout("tcp", proxy.target, 5*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}
	proxy.mu.Lock()
	if proxy.blocked {
		proxy.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	proxy.active[client] = struct{}{}
	proxy.active[upstream] = struct{}{}
	proxy.mu.Unlock()
	var once sync.Once
	closePair := func() {
		once.Do(func() {
			_ = client.Close()
			_ = upstream.Close()
			proxy.mu.Lock()
			delete(proxy.active, client)
			delete(proxy.active, upstream)
			proxy.mu.Unlock()
		})
	}
	go func() { _, _ = io.Copy(upstream, client); closePair() }()
	_, _ = io.Copy(client, upstream)
	closePair()
}

func (proxy *dmE2EFaultProxy) Block(blocked bool) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.blocked = blocked
	if blocked {
		for connection := range proxy.active {
			_ = connection.Close()
		}
	}
}

func (proxy *dmE2EFaultProxy) Close() {
	_ = proxy.listener.Close()
	proxy.Block(true)
}
