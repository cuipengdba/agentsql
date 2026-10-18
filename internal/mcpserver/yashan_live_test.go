//go:build yashan && cgo

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
	"github.com/stretchr/testify/require"
)

// TestYashanHTTPMCPRealE2E joins the native driver, least-privilege account,
// HTTP MCP identity, policy, masking, and reopened durable audit in one path.
func TestYashanHTTPMCPRealE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_YASHAN_E2E") != "1" {
		t.Skip("set AGENTSQL_YASHAN_E2E=1 and YASHAN_PASSWORD for a real YashanDB instance")
	}
	password := os.Getenv("YASHAN_PASSWORD")
	require.NotEmpty(t, password)
	host := os.Getenv("YASHAN_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	clientDir := os.Getenv("AGENTSQL_TEST_YASHAN_CLIENT_DIR")
	auditFaultDir := os.Getenv("AGENTSQL_TEST_YASHAN_AUDIT_FAULT_DIR")
	businessFaultDir := os.Getenv("AGENTSQL_TEST_YASHAN_BUSINESS_FAULT_DIR")
	timeout := 90 * time.Second
	if clientDir != "" || auditFaultDir != "" || businessFaultDir != "" {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	escape := strings.NewReplacer(`\`, `\\`, `/`, `\/`, `@`, `\@`)
	adminDSN := "SYS/" + escape.Replace(password) + "@" + host + ":1688?compat_vector=yashan"
	admin, err := sql.Open("yasdb", adminDSN)
	if err != nil || admin.PingContext(ctx) != nil {
		t.Fatal("open YashanDB SYS connection failed")
	}
	t.Cleanup(func() { _ = admin.Close() })
	nameBytes := make([]byte, 4)
	_, err = rand.Read(nameBytes)
	require.NoError(t, err)
	suffix := strings.ToUpper(hex.EncodeToString(nameBytes))
	table := "AGSQL_V21_T_" + suffix
	deniedTable := "AGSQL_V21_D_" + suffix
	reader := "AGSQL_V21_R_" + suffix
	secretBytes := make([]byte, 12)
	_, err = rand.Read(secretBytes)
	require.NoError(t, err)
	readerPassword := "Aa1" + hex.EncodeToString(secretBytes)
	createdTables := make([]string, 0, 2)
	createdReader := false
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		cleanupDB, openErr := sql.Open("yasdb", adminDSN)
		if openErr != nil || cleanupDB.PingContext(cleanupCtx) != nil {
			t.Error("reconnect YashanDB administrator for cleanup failed")
			return
		}
		defer cleanupDB.Close()
		if createdReader {
			if _, err := cleanupDB.ExecContext(cleanupCtx, "DROP USER "+reader+" CASCADE"); err != nil {
				t.Error("remove temporary YashanDB reader failed")
			}
		}
		for _, name := range createdTables {
			if _, err := cleanupDB.ExecContext(cleanupCtx, "DROP TABLE "+name); err != nil {
				t.Error("remove temporary YashanDB table failed")
			}
		}
	})
	for _, name := range []string{table, deniedTable} {
		_, err = admin.ExecContext(ctx, "CREATE TABLE "+name+" (ID NUMBER, PHONE VARCHAR2(32))")
		if err != nil {
			t.Fatal("create temporary YashanDB table failed")
		}
		createdTables = append(createdTables, name)
		_, err = admin.ExecContext(ctx, "INSERT INTO "+name+" (ID, PHONE) VALUES (1, '13800135678')")
		if err != nil {
			t.Fatal("seed temporary YashanDB table failed")
		}
	}
	_, err = admin.ExecContext(ctx, "CREATE USER "+reader+" IDENTIFIED BY "+readerPassword)
	if err != nil {
		t.Fatal("create temporary YashanDB reader failed")
	}
	createdReader = true
	for _, statement := range []string{
		"GRANT CREATE SESSION TO " + reader,
		"GRANT SELECT ON " + table + " TO " + reader,
		"GRANT SELECT ON " + deniedTable + " TO " + reader,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal("grant temporary YashanDB reader failed")
		}
	}
	readerDB, err := sql.Open("yasdb", reader+"/"+escape.Replace(readerPassword)+"@"+host+":1688?compat_vector=yashan")
	if err != nil || readerDB.PingContext(ctx) != nil {
		t.Fatal("login as temporary YashanDB reader failed")
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	var plain string
	if err := readerDB.QueryRowContext(ctx, "SELECT PHONE FROM SYS."+table+" WHERE ID = ?", 1).Scan(&plain); err != nil || plain != "13800135678" {
		t.Fatal("least-privilege YashanDB reader SELECT failed")
	}
	_, err = readerDB.ExecContext(ctx, "INSERT INTO SYS."+table+" (ID, PHONE) VALUES (2, 'x')")
	require.Error(t, err, "database account must reject writes")

	auditDSN := os.Getenv("AGENTSQL_TEST_YASHAN_AUDIT_DSN")
	openStore := func(path string) (*store.Store, error) {
		if auditDSN == "" {
			return store.OpenWithSecret(ctx, path, mcpTestSecret)
		}
		return store.OpenMetadata(ctx, store.MetadataOptions{
			Driver: store.DialectSQLite, SQLitePath: path, AutoMigrate: true,
			Audit: store.AuditOptions{Separate: true, Driver: store.DialectPostgres,
				PostgresDSN: auditDSN, AutoMigrate: true},
		}, mcpTestSecret)
	}
	fixture := newMCPFixtureWithOpener(t, "readonly", openStore)
	const datasourceID = "ds-yashan-v21"
	_, err = fixture.runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: datasourceID, Name: "YashanDB 23.4 least-privilege test", DBType: "yashan",
		Host: host, Port: 1688, Database: "SYS", Username: reader,
		ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 10,
	}, readerPassword)
	require.NoError(t, err)
	policy := model.Policy{ID: "yashan-allowed-table", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table", ObjectName: "SYS." + table, Action: "allow"}
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "SYS", Table: table, Column: "PHONE", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "yashan-v21-http-e2e", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + modernMCPPath, HTTPClient: httpClient,
	}, nil)
	require.NoError(t, err)
	defer session.Close()
	call := func(name, query string) string {
		t.Helper()
		arguments := map[string]any{"datasource_id": datasourceID, "sql": query}
		if name == "execute_write" {
			arguments["reason"] = "verify read-only enforcement"
		}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowSQL := "SELECT PHONE FROM SYS." + table + " WHERE ID = 1"
	allowed := call("query", allowSQL)
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, plain)
	denied := call("query", "SELECT PHONE FROM SYS."+deniedTable+" WHERE ID = 1")
	require.Contains(t, denied, `"decision":"deny"`)
	require.NotContains(t, denied, plain)
	unsafe := call("query", "SELECT /* unsupported */ PHONE FROM SYS."+table+" WHERE ID = 1")
	require.Contains(t, unsafe, `"decision":"error"`)
	require.NotContains(t, unsafe, plain)
	write := call("execute_write", "UPDATE SYS."+table+" SET PHONE = 'changed' WHERE ID = 1")
	require.NotContains(t, write, `"decision":"allow"`)
	// Keep datasource visibility while revoking the queried table. This forces
	// the request through the SQL policy gate and its durable audit record.
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "yashan-visibility-anchor", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table",
		ObjectName: "SYS.AGSQL_V21_UNQUERIED", Action: "allow",
	})
	require.NoError(t, err)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
	revoked := call("query", allowSQL)
	require.Contains(t, revoked, `"decision":"deny"`)
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	recovered := call("query", allowSQL)
	require.Contains(t, recovered, "138****5678")
	var after string
	require.NoError(t, admin.QueryRowContext(ctx, "SELECT PHONE FROM SYS."+table+" WHERE ID = 1").Scan(&after))
	require.Equal(t, plain, after)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(6), page.Total)
	for index, decision := range []string{"allow", "deny", "error", "error", "deny", "allow"} {
		require.Equal(t, decision, page.List[5-index].Decision)
	}
	reopened, err := openStore(fixture.databasePath)
	require.NoError(t, err)
	defer reopened.Close()
	durable, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, page.Total, durable.Total)
	require.Equal(t, page.List[0].ID, durable.List[0].ID)
	_, err = admin.ExecContext(ctx, "REVOKE SELECT ON "+table+" FROM "+reader)
	if err != nil {
		t.Fatal("revoke temporary YashanDB reader SELECT failed")
	}
	permissionRevoked := call("query", allowSQL)
	require.NotContains(t, permissionRevoked, `"decision":"allow"`)
	require.NotContains(t, permissionRevoked, plain)
	_, err = admin.ExecContext(ctx, "GRANT SELECT ON "+table+" TO "+reader)
	if err != nil {
		t.Fatal("restore temporary YashanDB reader SELECT failed")
	}
	permissionRestored := call("query", allowSQL)
	require.Contains(t, permissionRestored, `"decision":"allow"`)
	require.Contains(t, permissionRestored, "138****5678")
	afterPermission, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, page.Total+2, afterPermission.Total)
	require.Equal(t, "error", afterPermission.List[1].Decision)
	require.Equal(t, "allow", afterPermission.List[0].Decision)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "yashan-visibility-anchor"))
	visibilityDenied := call("query", allowSQL)
	require.Contains(t, visibilityDenied, `"decision":"deny"`)
	require.NotContains(t, visibilityDenied, plain)
	afterVisibility, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, afterPermission.Total+1, afterVisibility.Total)
	require.Equal(t, "datasource_visibility_deny", *afterVisibility.List[0].Action)
	require.Nil(t, afterVisibility.List[0].SQLRaw)
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "yashan-visibility-anchor", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table",
		ObjectName: "SYS.AGSQL_V21_UNQUERIED", Action: "allow",
	})
	require.NoError(t, err)
	afterPermission = afterVisibility
	if auditFaultDir != "" {
		require.NotEmpty(t, auditDSN, "audit outage probe needs an independent audit database")
		signal := func(name string) {
			t.Helper()
			require.NoError(t, os.WriteFile(filepath.Join(auditFaultDir, name), []byte("ready\n"), 0o600))
		}
		waitSignal := func(name string) {
			t.Helper()
			for {
				if _, statErr := os.Stat(filepath.Join(auditFaultDir, name)); statErr == nil {
					return
				} else if !os.IsNotExist(statErr) {
					require.NoError(t, statErr)
				}
				select {
				case <-ctx.Done():
					t.Fatal("timed out waiting for independent audit fault signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		signal("ready")
		waitSignal("down")
		failedAudit := call("query", allowSQL)
		require.NotContains(t, failedAudit, `"decision":"allow"`)
		require.NotContains(t, failedAudit, plain)
		require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
		require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "yashan-visibility-anchor"))
		failedPreflightAudit := call("query", allowSQL)
		require.Contains(t, failedPreflightAudit, `"decision":"error"`)
		require.NotContains(t, failedPreflightAudit, plain)
		_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
		require.NoError(t, err)
		_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
			ID: "yashan-visibility-anchor", AgentID: fixture.agent.ID,
			DatasourceID: datasourceID, ObjectType: "table",
			ObjectName: "SYS.AGSQL_V21_UNQUERIED", Action: "allow",
		})
		require.NoError(t, err)
		signal("down_checked")
		waitSignal("up")
		restoredAudit := call("query", allowSQL)
		require.Contains(t, restoredAudit, `"decision":"allow"`)
		require.Contains(t, restoredAudit, "138****5678")
		require.NotContains(t, restoredAudit, plain)
		afterFault, pageErr := reopened.AuditLogs().Page(ctx, 1, 20)
		require.NoError(t, pageErr)
		require.Equal(t, afterPermission.Total+1, afterFault.Total)
		afterPermission = afterFault
	}
	if businessFaultDir != "" {
		signal := func(name string) {
			t.Helper()
			require.NoError(t, os.WriteFile(filepath.Join(businessFaultDir, name), []byte("ready\n"), 0o600))
		}
		waitSignal := func(name string) {
			t.Helper()
			for {
				if _, statErr := os.Stat(filepath.Join(businessFaultDir, name)); statErr == nil {
					return
				} else if !os.IsNotExist(statErr) {
					require.NoError(t, statErr)
				}
				select {
				case <-ctx.Done():
					t.Fatal("timed out waiting for YashanDB outage signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		signal("ready")
		waitSignal("down")
		failedBusiness := call("query", allowSQL)
		require.NotContains(t, failedBusiness, `"decision":"allow"`)
		require.NotContains(t, failedBusiness, plain)
		signal("down_checked")
		waitSignal("up")
		restoredBusiness := call("query", allowSQL)
		require.Contains(t, restoredBusiness, `"decision":"allow"`)
		require.Contains(t, restoredBusiness, "138****5678")
		require.NotContains(t, restoredBusiness, plain)
		afterFault, pageErr := reopened.AuditLogs().Page(ctx, 1, 20)
		require.NoError(t, pageErr)
		require.Equal(t, afterPermission.Total+2, afterFault.Total)
		require.Equal(t, "error", afterFault.List[1].Decision)
		require.Equal(t, "allow", afterFault.List[0].Decision)
		afterPermission = afterFault
	}
	if clientDir == "" {
		return
	}
	clientInfo, err := json.Marshal(map[string]string{
		"url": httpServer.URL + "/mcp", "api_key": fixture.handlers.apiKey,
		"datasource_id": datasourceID, "allow_sql": allowSQL,
		"deny_sql":  "SELECT PHONE FROM SYS." + deniedTable + " WHERE ID = 1",
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
			t.Fatal("timed out waiting for YashanDB Inspector and Dify MCP calls")
		case <-time.After(250 * time.Millisecond):
		}
	}
	afterClients, err := reopened.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, afterPermission.Total+4, afterClients.Total)
}
