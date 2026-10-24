package mcpserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	_ "github.com/microsoft/go-mssqldb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestSQLServerHTTPMCPRealE2E verifies the bounded SQL Server 2025 path with
// a real certificate, dedicated database and least-privilege login. It uses
// only randomized synthetic data and is never part of the short test suite.
func TestSQLServerHTTPMCPRealE2E(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTSQL_SQLSERVER_2025_MCP_E2E") != "1" {
		t.Skip("set AGENTSQL_SQLSERVER_2025_MCP_E2E=1 and SQL Server test connection variables")
	}
	host := sqlServerE2ERequired(t, "AGENTSQL_SQLSERVER_HOST")
	port, err := strconv.Atoi(sqlServerE2ERequired(t, "AGENTSQL_SQLSERVER_PORT"))
	require.NoError(t, err)
	caFile := sqlServerE2ERequired(t, "AGENTSQL_SQLSERVER_TLS_CA_FILE")
	adminPassword := sqlServerE2ERequired(t, "AGENTSQL_SQLSERVER_ADMIN_PASSWORD")
	auditDSN := os.Getenv("AGENTSQL_TEST_SQLSERVER_AUDIT_DSN")
	auditFaultDir := os.Getenv("AGENTSQL_TEST_SQLSERVER_AUDIT_FAULT_DIR")
	businessFaultDir := os.Getenv("AGENTSQL_TEST_SQLSERVER_BUSINESS_FAULT_DIR")
	clientDir := os.Getenv("AGENTSQL_TEST_SQLSERVER_CLIENT_DIR")
	timeout := 4 * time.Minute
	if auditFaultDir != "" || businessFaultDir != "" || clientDir != "" {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	open := func(database, username, password string) (*sql.DB, error) {
		connection := &url.URL{
			Scheme: "sqlserver", User: url.UserPassword(username, password),
			Host: net.JoinHostPort(host, strconv.Itoa(port)),
		}
		query := connection.Query()
		query.Set("database", database)
		query.Set("encrypt", "strict")
		query.Set("certificate", caFile)
		connection.RawQuery = query.Encode()
		return sql.Open("sqlserver", connection.String())
	}
	admin, err := open("master", "sa", adminPassword)
	require.NoError(t, err)
	require.NoError(t, admin.PingContext(ctx))
	defer admin.Close()

	random := make([]byte, 4)
	_, err = rand.Read(random)
	require.NoError(t, err)
	suffix := strings.ToLower(hex.EncodeToString(random))
	database := "agsql_v23_" + suffix
	table := "agsql_v23_t_" + suffix
	deniedTable := "agsql_v23_d_" + suffix
	reader := "agsql_v23_r_" + suffix
	secret := make([]byte, 16)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	readerPassword := "Aa1!" + hex.EncodeToString(secret)
	createdDatabase, createdLogin := false, false
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 40*time.Second)
		defer stop()
		cleanup, openErr := open("master", "sa", adminPassword)
		if openErr != nil || cleanup.PingContext(cleanupCtx) != nil {
			t.Error("reconnect SQL Server administrator for cleanup failed")
			return
		}
		defer cleanup.Close()
		if createdDatabase {
			_, dropErr := cleanup.ExecContext(cleanupCtx,
				"ALTER DATABASE ["+database+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+database+"]")
			if dropErr != nil {
				var remaining int
				if countErr := cleanup.QueryRowContext(cleanupCtx,
					"SELECT COUNT(*) FROM sys.databases WHERE name = @p1", database).Scan(&remaining); countErr != nil || remaining != 0 {
					t.Error("remove temporary SQL Server database failed")
				}
			}
		}
		if createdLogin {
			if _, dropErr := cleanup.ExecContext(cleanupCtx, "DROP LOGIN ["+reader+"]"); dropErr != nil {
				t.Error("remove temporary SQL Server login failed")
			}
		}
	})
	_, err = admin.ExecContext(ctx, "CREATE DATABASE ["+database+"]")
	require.NoError(t, err)
	createdDatabase = true
	_, err = admin.ExecContext(ctx, "CREATE LOGIN ["+reader+"] WITH PASSWORD = '"+readerPassword+"', CHECK_POLICY = OFF")
	require.NoError(t, err)
	createdLogin = true
	adminDB, err := open(database, "sa", adminPassword)
	require.NoError(t, err)
	require.NoError(t, adminDB.PingContext(ctx))
	defer adminDB.Close()
	for _, statement := range []string{
		"CREATE TABLE [dbo].[" + table + "] ([id] int NOT NULL, [phone] varchar(32) NOT NULL)",
		"CREATE TABLE [dbo].[" + deniedTable + "] ([id] int NOT NULL, [phone] varchar(32) NOT NULL)",
		"INSERT INTO [dbo].[" + table + "] ([id], [phone]) VALUES (1, '13800135678')",
		"INSERT INTO [dbo].[" + deniedTable + "] ([id], [phone]) VALUES (1, '13800135678')",
		"CREATE USER [" + reader + "] FOR LOGIN [" + reader + "]",
		"GRANT SELECT ON OBJECT::[dbo].[" + table + "] TO [" + reader + "]",
		"GRANT SHOWPLAN TO [" + reader + "]",
	} {
		_, err = adminDB.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	readerDB, err := open(database, reader, readerPassword)
	require.NoError(t, err)
	require.NoError(t, readerDB.PingContext(ctx))
	defer readerDB.Close()
	var plain string
	require.NoError(t, readerDB.QueryRowContext(ctx,
		"SELECT [phone] FROM [dbo].["+table+"] WHERE [id] = 1").Scan(&plain))
	require.Equal(t, "13800135678", plain)
	_, err = readerDB.ExecContext(ctx, "INSERT INTO [dbo].["+table+"] ([id], [phone]) VALUES (2, 'x')")
	require.Error(t, err, "the database account must reject writes independently of AgentSQL")
	for _, role := range []string{"db_owner", "db_datareader", "db_datawriter"} {
		var member int
		require.NoError(t, readerDB.QueryRowContext(ctx, "SELECT IS_MEMBER('"+role+"')").Scan(&member))
		require.Zero(t, member, "temporary reader must not have broad database role: "+role)
	}

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
	const datasourceID = "ds-sqlserver-v23"
	_, err = fixture.runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: datasourceID, Name: "SQL Server 2025 least-privilege test", DBType: "sqlserver",
		Host: host, Port: port, Database: database, Username: reader,
		TLSMode: "strict", TLSCAFile: caFile, ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 10,
	}, readerPassword)
	require.NoError(t, err)
	policy := model.Policy{ID: "sqlserver-allowed-table", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table", ObjectName: "dbo." + table, Action: "allow"}
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "dbo", Table: table, Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "sqlserver-v23-http-e2e", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + modernMCPPath, HTTPClient: httpClient,
	}, nil)
	require.NoError(t, err)
	defer session.Close()
	call := func(name, query string) string {
		t.Helper()
		arguments := map[string]any{"datasource_id": datasourceID, "sql": query}
		if name == "execute_write" {
			arguments["reason"] = "verify SQL Server read-only enforcement"
		}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowSQL := "SELECT [phone] FROM [dbo].[" + table + "] WHERE [id] = 1"
	allowed := call("query", allowSQL)
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, plain)
	denied := call("query", "SELECT [phone] FROM [dbo].["+deniedTable+"] WHERE [id] = 1")
	require.Contains(t, denied, `"decision":"deny"`)
	require.Contains(t, denied, `"RuleID":"R010"`)
	require.NotContains(t, denied, plain)
	for _, unsafeSQL := range []string{
		"SELECT /* unsupported */ [phone] FROM [dbo].[" + table + "] WHERE [id] = 1",
		"SELECT [phone] FROM [dbo].[" + table + "] WHERE [id] = 1; SELECT [phone] FROM [dbo].[" + table + "] WHERE [id] = 1",
		"EXEC xp_cmdshell 'whoami'",
		"SELECT [phone] FROM OPENROWSET('SQLNCLI', 'server', 'query')",
	} {
		unsafe := call("query", unsafeSQL)
		require.NotContains(t, unsafe, `"decision":"allow"`)
		require.NotContains(t, unsafe, plain)
	}
	write := call("execute_write", "UPDATE [dbo].["+table+"] SET [phone] = 'changed' WHERE [id] = 1")
	require.NotContains(t, write, `"decision":"allow"`)
	require.NotContains(t, write, plain)

	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	reopened, err := opener(fixture.databasePath)
	require.NoError(t, err)
	defer reopened.Close()
	durable, err := reopened.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.Equal(t, page.Total, durable.Total)
	require.Equal(t, page.List[0].ID, durable.List[0].ID)

	_, err = adminDB.ExecContext(ctx, "REVOKE SELECT ON OBJECT::[dbo].["+table+"] FROM ["+reader+"]")
	require.NoError(t, err)
	permissionRevoked := call("query", allowSQL)
	require.NotContains(t, permissionRevoked, `"decision":"allow"`)
	require.NotContains(t, permissionRevoked, plain)
	_, err = adminDB.ExecContext(ctx, "GRANT SELECT ON OBJECT::[dbo].["+table+"] TO ["+reader+"]")
	require.NoError(t, err)
	_, err = adminDB.ExecContext(ctx, "REVOKE SHOWPLAN FROM ["+reader+"]")
	require.NoError(t, err)
	planRevoked := call("query", allowSQL)
	require.NotContains(t, planRevoked, `"decision":"allow"`)
	require.NotContains(t, planRevoked, plain)
	_, err = adminDB.ExecContext(ctx, "GRANT SHOWPLAN TO ["+reader+"]")
	require.NoError(t, err)
	permissionRestored := call("query", allowSQL)
	require.Contains(t, permissionRestored, `"decision":"allow"`)
	require.Contains(t, permissionRestored, "138****5678")

	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
	visibilityDenied := call("query", allowSQL)
	require.Contains(t, visibilityDenied, `"decision":"deny"`)
	require.NotContains(t, visibilityDenied, plain)
	after, err := reopened.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.Equal(t, "datasource_visibility_deny", *after.List[0].Action)
	require.Nil(t, after.List[0].SQLRaw)
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	if auditFaultDir != "" || businessFaultDir != "" || clientDir != "" {
		runSQLServerMCPExternalChecks(t, ctx, call, readerDB, "SELECT [phone] FROM [dbo].["+table+"] WHERE [id] = 1", opener, fixture.databasePath,
			auditDSN, auditFaultDir, businessFaultDir, clientDir, httpServer.URL,
			fixture.handlers.apiKey, datasourceID, allowSQL,
			"SELECT [phone] FROM [dbo].["+deniedTable+"] WHERE [id] = 1", plain)
	}
}

func sqlServerE2ERequired(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	require.NotEmpty(t, value, name+" is required")
	return value
}

func runSQLServerMCPExternalChecks(
	t *testing.T, ctx context.Context, call func(string, string) string,
	readerDB *sql.DB, readinessSQL string,
	opener func(string) (*store.Store, error), databasePath, auditDSN,
	auditFaultDir, businessFaultDir, clientDir, serverURL, apiKey, datasourceID,
	allowSQL, denySQL, plain string,
) {
	t.Helper()
	signal := func(dir, name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("ready\n"), 0o600))
	}
	waitSignal := func(dir, name string) {
		t.Helper()
		for {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return
			} else if !os.IsNotExist(err) {
				require.NoError(t, err)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for SQL Server fault/client signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if auditFaultDir != "" {
		require.NotEmpty(t, auditDSN, "audit outage requires independent audit database")
		before, err := opener(databasePath)
		require.NoError(t, err)
		baseline, err := before.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.NoError(t, before.Close())
		signal(auditFaultDir, "ready")
		waitSignal(auditFaultDir, "down")
		failed := call("query", allowSQL)
		require.NotContains(t, failed, `"decision":"allow"`)
		require.NotContains(t, failed, plain)
		signal(auditFaultDir, "down_checked")
		waitSignal(auditFaultDir, "up")
		restored := call("query", allowSQL)
		require.Contains(t, restored, `"decision":"allow"`)
		require.Contains(t, restored, "138****5678")
		after, err := opener(databasePath)
		require.NoError(t, err)
		persisted, err := after.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.NoError(t, after.Close())
		require.Equal(t, baseline.Total+1, persisted.Total)
		require.Equal(t, "allow", persisted.List[0].Decision)
	}
	if businessFaultDir != "" {
		before, err := opener(databasePath)
		require.NoError(t, err)
		baseline, err := before.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.NoError(t, before.Close())
		signal(businessFaultDir, "ready")
		waitSignal(businessFaultDir, "down")
		failed := call("query", allowSQL)
		require.NotContains(t, failed, `"decision":"allow"`)
		require.NotContains(t, failed, plain)
		signal(businessFaultDir, "down_checked")
		waitSignal(businessFaultDir, "up")
		ready := false
		recoveryDeadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(recoveryDeadline) {
			readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			var value string
			err := readerDB.QueryRowContext(readCtx, readinessSQL).Scan(&value)
			cancel()
			if err == nil && value == plain {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("SQL Server test database did not recover")
			case <-time.After(time.Second):
			}
		}
		require.True(t, ready, "SQL Server reader must recover before MCP recovery assertion")
		var restored string
		var recoveryCalls int64
		for attempt := 0; attempt < 8; attempt++ {
			restored = call("query", allowSQL)
			recoveryCalls++
			if strings.Contains(restored, `"decision":"allow"`) {
				break
			}
			require.NotContains(t, restored, plain)
			select {
			case <-ctx.Done():
				t.Fatal("SQL Server MCP reader did not recover")
			case <-time.After(2 * time.Second):
			}
		}
		require.Contains(t, restored, `"decision":"allow"`)
		require.Contains(t, restored, "138****5678")
		after, err := opener(databasePath)
		require.NoError(t, err)
		persisted, err := after.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.NoError(t, after.Close())
		require.Equal(t, baseline.Total+1+recoveryCalls, persisted.Total)
		require.Equal(t, "allow", persisted.List[0].Decision)
		require.Equal(t, "error", persisted.List[int(recoveryCalls)].Decision)
	}
	if clientDir == "" {
		return
	}
	before, err := opener(databasePath)
	require.NoError(t, err)
	page, err := before.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.NoError(t, before.Close())
	info, err := json.Marshal(map[string]string{
		"url": serverURL + "/mcp", "api_key": apiKey, "datasource_id": datasourceID,
		"allow_sql": allowSQL, "deny_sql": denySQL, "deny_rule": "R010",
	})
	require.NoError(t, err)
	infoPath := filepath.Join(clientDir, "client.json")
	require.NoError(t, os.WriteFile(infoPath, info, 0o600))
	defer os.Remove(infoPath)
	waitSignal(clientDir, "clients_done")
	after, err := opener(databasePath)
	require.NoError(t, err)
	defer after.Close()
	latest, err := after.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.Equal(t, page.Total+4, latest.Total)
}
