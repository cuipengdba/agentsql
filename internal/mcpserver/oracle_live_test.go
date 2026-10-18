package mcpserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	goora "github.com/sijms/go-ora/v2"
	"github.com/stretchr/testify/require"
)

// TestOracleHTTPMCPRealE2E proves the narrowly governed Oracle path against
// a real FREEPDB1 instance. It is opt-in and uses only synthetic data.
func TestOracleHTTPMCPRealE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_ORACLE_E2E") != "1" {
		t.Skip("set AGENTSQL_ORACLE_E2E=1 and ORACLE_PASSWORD for a real Oracle instance")
	}
	password := os.Getenv("ORACLE_PASSWORD")
	require.NotEmpty(t, password)
	host := os.Getenv("ORACLE_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	clientDir := os.Getenv("AGENTSQL_TEST_ORACLE_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_TEST_ORACLE_AUDIT_DSN")
	auditFaultDir := os.Getenv("AGENTSQL_TEST_ORACLE_AUDIT_FAULT_DIR")
	businessFaultDir := os.Getenv("AGENTSQL_TEST_ORACLE_BUSINESS_FAULT_DIR")
	timeout := 3 * time.Minute
	if clientDir != "" || auditFaultDir != "" || businessFaultDir != "" {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	admin, err := sql.Open("oracle", goora.BuildUrl(host, 1521, "FREEPDB1", "SYSTEM", password, nil))
	require.NoError(t, err)
	require.NoError(t, admin.PingContext(ctx))
	defer admin.Close()

	random := make([]byte, 4)
	_, err = rand.Read(random)
	require.NoError(t, err)
	suffix := strings.ToUpper(hex.EncodeToString(random))
	table := "AGSQL_V22_T_" + suffix
	deniedTable := "AGSQL_V22_D_" + suffix
	reader := "AGSQL_V22_R_" + suffix
	secret := make([]byte, 12)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	readerPassword := "Aa1" + hex.EncodeToString(secret)
	created := make([]string, 0, 2)
	createdReader := false
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cleanup, openErr := sql.Open("oracle", goora.BuildUrl(host, 1521, "FREEPDB1", "SYSTEM", password, nil))
		if openErr != nil || cleanup.PingContext(cleanupCtx) != nil {
			t.Error("reconnect Oracle administrator for cleanup failed")
			return
		}
		defer cleanup.Close()
		if createdReader {
			if _, dropErr := cleanup.ExecContext(cleanupCtx, "DROP USER "+reader+" CASCADE"); dropErr != nil {
				t.Error("remove temporary Oracle reader failed")
			}
		}
		for _, name := range created {
			if _, dropErr := cleanup.ExecContext(cleanupCtx, "DROP TABLE SYSTEM."+name+" PURGE"); dropErr != nil {
				t.Error("remove temporary Oracle table failed")
			}
		}
	})
	for _, name := range []string{table, deniedTable} {
		_, err = admin.ExecContext(ctx, "CREATE TABLE SYSTEM."+name+" (ID NUMBER, PHONE VARCHAR2(32))")
		require.NoError(t, err)
		created = append(created, name)
		_, err = admin.ExecContext(ctx, "INSERT INTO SYSTEM."+name+" (ID, PHONE) VALUES (1, '13800135678')")
		require.NoError(t, err)
	}
	_, err = admin.ExecContext(ctx, "CREATE USER "+reader+" IDENTIFIED BY "+readerPassword)
	require.NoError(t, err)
	createdReader = true
	for _, statement := range []string{
		"GRANT CREATE SESSION TO " + reader,
		"GRANT SELECT ON SYSTEM." + table + " TO " + reader,
	} {
		_, err = admin.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	readerDB, err := sql.Open("oracle", goora.BuildUrl(host, 1521, "FREEPDB1", reader, readerPassword, nil))
	require.NoError(t, err)
	require.NoError(t, readerDB.PingContext(ctx))
	defer readerDB.Close()
	var plain string
	require.NoError(t, readerDB.QueryRowContext(ctx, "SELECT PHONE FROM SYSTEM."+table+" WHERE ID = 1").Scan(&plain))
	require.Equal(t, "13800135678", plain)
	_, err = readerDB.ExecContext(ctx, "INSERT INTO SYSTEM."+table+" (ID, PHONE) VALUES (2, 'x')")
	require.Error(t, err, "Oracle must reject writes from the gateway account")

	opener := func(path string) (*store.Store, error) {
		if auditDSN == "" {
			return store.OpenWithSecret(ctx, path, mcpTestSecret)
		}
		return store.OpenMetadata(ctx, store.MetadataOptions{
			Driver: store.DialectSQLite, SQLitePath: path, AutoMigrate: true,
			Audit: store.AuditOptions{Separate: true, Driver: store.DialectPostgres,
				PostgresDSN: auditDSN, AutoMigrate: true},
		}, mcpTestSecret)
	}
	fixture := newMCPFixtureWithOpener(t, "readonly", opener)
	const datasourceID = "ds-oracle-v22"
	_, err = fixture.runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: datasourceID, Name: "Oracle 26ai Free least-privilege test", DBType: "oracle",
		Host: host, Port: 1521, Database: "FREEPDB1", Username: reader,
		ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 10,
	}, readerPassword)
	require.NoError(t, err)
	policy := model.Policy{ID: "oracle-allowed-table", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table", ObjectName: "SYSTEM." + table, Action: "allow"}
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "SYSTEM", Table: table, Column: "PHONE", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.redactors.redactor = redactor
	gateway := executor.NewGateway(true)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	fixture.runtime.Pipeline, err = pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(fixture.runtime.Store.Agents()),
		Datasources:   fixture.runtime.Store.Datasources(), Policies: fixture.runtime.Store.Policies(),
		Executors: gateway, Approvals: fixture.runtime.Store.Approvals(),
		Audit: audit.NewRecorder(fixture.runtime.Store.AuditLogs()), Redactors: fixture.redactors,
		TaskReadGrants: fixture.runtime.Store.TaskReadGrants(),
	}, mcpTestSecret)
	require.NoError(t, err)
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	httpClient := httpServer.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: fixture.handlers.apiKey}
	client := mcp.NewClient(&mcp.Implementation{Name: "oracle-v22-http-e2e", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + modernMCPPath, HTTPClient: httpClient,
	}, nil)
	require.NoError(t, err)
	defer session.Close()
	call := func(name, query string) string {
		t.Helper()
		arguments := map[string]any{"datasource_id": datasourceID, "sql": query}
		if name == "execute_write" {
			arguments["reason"] = "verify Oracle read-only enforcement"
		}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowSQL := "SELECT PHONE FROM SYSTEM." + table + " WHERE ID = 1"
	allowed := call("query", allowSQL)
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, plain)
	denied := call("query", "SELECT PHONE FROM SYSTEM."+deniedTable+" WHERE ID = 1")
	require.Contains(t, denied, `"decision":"deny"`)
	require.Contains(t, denied, `"RuleID":"R010"`)
	require.NotContains(t, denied, plain)
	unsafe := call("query", "SELECT /* unsupported */ PHONE FROM SYSTEM."+table+" WHERE ID = 1")
	require.Contains(t, unsafe, `"decision":"error"`)
	require.NotContains(t, unsafe, plain)
	write := call("execute_write", "UPDATE SYSTEM."+table+" SET PHONE = 'changed' WHERE ID = 1")
	require.NotContains(t, write, `"decision":"allow"`)
	require.NotContains(t, write, plain)

	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(4), page.Total)
	reopened, err := opener(fixture.databasePath)
	require.NoError(t, err)
	defer reopened.Close()
	durable, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, page.Total, durable.Total)
	require.Equal(t, page.List[0].ID, durable.List[0].ID)

	_, err = admin.ExecContext(ctx, "REVOKE SELECT ON SYSTEM."+table+" FROM "+reader)
	require.NoError(t, err)
	permissionRevoked := call("query", allowSQL)
	require.NotContains(t, permissionRevoked, `"decision":"allow"`)
	require.NotContains(t, permissionRevoked, plain)
	_, err = admin.ExecContext(ctx, "GRANT SELECT ON SYSTEM."+table+" TO "+reader)
	require.NoError(t, err)
	permissionRestored := call("query", allowSQL)
	require.Contains(t, permissionRestored, `"decision":"allow"`)
	require.Contains(t, permissionRestored, "138****5678")

	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
	visibilityDenied := call("query", allowSQL)
	require.Contains(t, visibilityDenied, `"decision":"deny"`)
	require.NotContains(t, visibilityDenied, plain)
	after, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, page.Total+3, after.Total)
	require.Equal(t, "datasource_visibility_deny", *after.List[0].Action)
	require.Nil(t, after.List[0].SQLRaw)
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	signal := func(dir, name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("ready\n"), 0o600))
	}
	waitSignal := func(dir, name string) {
		t.Helper()
		for {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
				return
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for Oracle fault signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if auditFaultDir != "" {
		require.NotEmpty(t, auditDSN, "audit outage requires an independent audit database")
		signal(auditFaultDir, "ready")
		waitSignal(auditFaultDir, "down")
		failedAudit := call("query", allowSQL)
		require.NotContains(t, failedAudit, `"decision":"allow"`)
		require.NotContains(t, failedAudit, plain)
		require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
		failedPreflight := call("query", allowSQL)
		require.Contains(t, failedPreflight, `"decision":"error"`)
		require.NotContains(t, failedPreflight, plain)
		_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
		require.NoError(t, err)
		signal(auditFaultDir, "down_checked")
		waitSignal(auditFaultDir, "up")
		restoredAudit := call("query", allowSQL)
		require.Contains(t, restoredAudit, `"decision":"allow"`)
		require.Contains(t, restoredAudit, "138****5678")
		faultPage, pageErr := reopened.AuditLogs().Page(ctx, 1, 20)
		require.NoError(t, pageErr)
		require.Equal(t, after.Total+1, faultPage.Total)
		after = faultPage
	}
	if businessFaultDir != "" {
		signal(businessFaultDir, "ready")
		waitSignal(businessFaultDir, "down")
		failedBusiness := call("query", allowSQL)
		require.NotContains(t, failedBusiness, `"decision":"allow"`)
		require.NotContains(t, failedBusiness, plain)
		signal(businessFaultDir, "down_checked")
		waitSignal(businessFaultDir, "up")
		restoredBusiness := call("query", allowSQL)
		require.Contains(t, restoredBusiness, `"decision":"allow"`)
		require.Contains(t, restoredBusiness, "138****5678")
		faultPage, pageErr := reopened.AuditLogs().Page(ctx, 1, 20)
		require.NoError(t, pageErr)
		require.Equal(t, after.Total+2, faultPage.Total)
		require.Equal(t, "error", faultPage.List[1].Decision)
		require.Equal(t, "allow", faultPage.List[0].Decision)
		after = faultPage
	}
	if clientDir == "" {
		return
	}
	clientInfo, err := json.Marshal(map[string]string{
		"url": httpServer.URL + "/mcp", "api_key": fixture.handlers.apiKey,
		"datasource_id": datasourceID, "allow_sql": allowSQL,
		"deny_sql":  "SELECT PHONE FROM SYSTEM." + deniedTable + " WHERE ID = 1",
		"deny_rule": "R010",
	})
	require.NoError(t, err)
	infoPath := filepath.Join(clientDir, "client.json")
	require.NoError(t, os.WriteFile(infoPath, clientInfo, 0o600))
	defer os.Remove(infoPath)
	for {
		if _, statErr := os.Stat(filepath.Join(clientDir, "clients_done")); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			require.NoError(t, statErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for Oracle Inspector and Dify MCP calls")
		case <-time.After(250 * time.Millisecond):
		}
	}
	afterClients, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, after.Total+4, afterClients.Total)
}
