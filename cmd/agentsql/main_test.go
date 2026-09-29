package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/adminapi"
	"github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const commandConfig = `server:
  http_listen: %q
  console_enabled: true
store:
  sqlite_path: %q
defaults:
  statement_timeout_ms: %d
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`

func TestVersionCommand(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")

	command := newRootCommand(zerologForTest(t))
	_, _, err := command.Find([]string{"version"})
	require.NoError(t, err)

	output := new(strings.Builder)
	exitCode := run([]string{"version"}, output, io.Discard)
	require.Equal(t, 0, exitCode)
	require.Equal(t, version.Version+"\n", output.String())
	output.Reset()
	require.Equal(t, 0, run([]string{"--version"}, output, io.Discard))
	require.Equal(t, version.Version+"\n", output.String())
	command = newRootCommand(zerologForTest(t))
	_, _, err = command.Find([]string{"mcp"})
	require.NoError(t, err)
	serve, _, err := command.Find([]string{"serve"})
	require.NoError(t, err)
	require.Nil(t, serve.Flags().Lookup("api-key"))
}

func TestPrepareStdioConfigDisablesHTTPEventStream(t *testing.T) {
	loaded := config.Config{Server: config.ServerConfig{
		ConsoleEnabled:            true,
		EventStream:               true,
		EventStreamMaxConnections: 100,
	}}
	prepared := prepareStdioConfig(loaded)
	require.False(t, prepared.Server.ConsoleEnabled)
	require.False(t, prepared.Server.EventStream)
	require.True(t, loaded.Server.ConsoleEnabled)
	require.True(t, loaded.Server.EventStream)
}

const mcpInitializeHTTPBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cmd-entrypoint-test","version":"1"}}}`

// mcpHTTPRequest posts a single Streamable HTTP MCP envelope, attaching the
// transport session ID when one is provided.
func mcpHTTPRequest(t *testing.T, handler http.Handler, apiKey string, body string, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// mcpHTTPInitialize performs the initialize handshake and returns the
// Mcp-Session-Id that subsequent stateful requests must present.
func mcpHTTPInitialize(t *testing.T, handler http.Handler, apiKey string) string {
	t.Helper()
	recorder := mcpHTTPRequest(t, handler, apiKey, mcpInitializeHTTPBody, "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	sessionID := recorder.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must return a transport session ID")
	return sessionID
}

func TestB5ProductionEntrypointOptionsRegisterHTTPAndStdio(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, runtime := newDemoStartupRuntime(t, false, secret)
	apiKey := "asql_b5_production_entrypoint"
	_, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "b5-entrypoint-agent", Name: "B5", Status: "active", APIKeyHash: store.HashAPIKey(apiKey), Level: "dml"})
	require.NoError(t, err)
	options := productionB5Options(runtime)
	require.True(t, options.B5Sessions)
	require.True(t, options.B5TxPostgres)
	require.False(t, options.B5TxMySQL)

	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(), mcpserver.WithB5Sessions(options))
	require.NoError(t, err)
	sessionID := mcpHTTPInitialize(t, handler, apiKey)
	recorder := mcpHTTPRequest(t, handler, apiKey, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sessionID)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	for _, name := range []string{"open_session", "begin_transaction", "execute_transaction_statement", "commit_transaction", "rollback_transaction"} {
		require.Contains(t, recorder.Body.String(), `"`+name+`"`)
	}
	_, err = runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "b5-entrypoint-mysql", Name: "MySQL", DBType: "mysql",
		Host: "127.0.0.1", Port: 3306, Database: "app", Username: "agentsql", ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100}, "not-used")
	require.NoError(t, err)
	mysqlCall := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"begin_transaction","arguments":{"session_id":"not-used","owner_epoch":1,"request_id":"mysql","continuation_proof":"not-used","body_digest":"not-used","transaction_id":"not-used","datasource_id":"b5-entrypoint-mysql","dialect":"postgres","server_major":18,"key_revision":1,"datasource_revision":1,"policy_revision":1,"statements":[{"operation_id":"op","sql":"UPDATE items SET value=1","reason":"must reject mysql"}]}}}`
	recorder = mcpHTTPRequest(t, handler, apiKey, mysqlCall, sessionID)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "DIALECT_TRANSACTION_UNSUPPORTED")
	require.Contains(t, recorder.Body.String(), "MySQL")
	require.Contains(t, recorder.Body.String(), "不受支持")
	require.Contains(t, recorder.Body.String(), "未取得可写连接")

	server, err := mcpserver.NewServer(ctx, mcpserver.Options{APIKey: apiKey, Runtime: runtime, Logger: zerolog.Nop(), B5: options})
	require.NoError(t, err)
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	runErrors := make(chan error, 1)
	go func() { runErrors <- server.RunStdioStreams(ctx, serverInput, serverOutput) }()
	err = authorizedexecute.WriteSealedFrame(clientInput, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"b5-entrypoint-test","version":"1"}}}`), authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	_, err = authorizedexecute.ReadBoundedFrame(clientOutput, authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	err = authorizedexecute.WriteSealedFrame(clientInput, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`), authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	listed, err := authorizedexecute.ReadBoundedFrame(clientOutput, authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	for _, name := range []string{"open_session", "begin_transaction", "execute_transaction_statement", "commit_transaction", "rollback_transaction"} {
		require.Contains(t, string(listed), `"`+name+`"`)
	}
	require.NoError(t, clientInput.Close())
	select {
	case err := <-runErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("production stdio entrypoint did not stop after EOF")
	}
}

func TestB5ExplicitOffHidesToolsFromHTTPAndStdio(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, _ := newDemoStartupRuntime(t, false, secret)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "b5-off.db")
	cfg.MCP = config.MCPConfig{
		Sessions: config.MCPSessionsConfig{Enabled: false, IdleTTLMS: 600_000, AbsoluteTTLMS: 3_600_000},
		Transactions: config.MCPTransactionsConfig{Postgres: false, MySQL: false, IdleTimeoutMS: 15_000,
			WallTimeoutMS: 60_000, StatementTimeoutMS: 5_000, ShutdownDrainMS: 5_000},
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(secret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.Nil(t, runtime.B5)
	apiKey := "asql_b5_explicit_off"
	_, err = runtime.Store.Agents().Create(ctx, model.Agent{ID: "b5-off-agent", Name: "B5 off", Status: "active", APIKeyHash: store.HashAPIKey(apiKey), Level: "dml"})
	require.NoError(t, err)

	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	sessionID := mcpHTTPInitialize(t, handler, apiKey)
	recorder := mcpHTTPRequest(t, handler, apiKey, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sessionID)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	for _, name := range []string{"open_session", "close_session", "get_session_status", "begin_transaction", "execute_transaction_statement", "commit_transaction", "rollback_transaction", "get_transaction_status"} {
		require.NotContains(t, recorder.Body.String(), `"`+name+`"`)
	}

	server, err := mcpserver.NewServer(ctx, mcpserver.Options{APIKey: apiKey, Runtime: runtime, Logger: zerolog.Nop(), B5: productionB5Options(runtime)})
	require.NoError(t, err)
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	runErrors := make(chan error, 1)
	go func() { runErrors <- server.RunStdioStreams(ctx, serverInput, serverOutput) }()
	require.NoError(t, authorizedexecute.WriteSealedFrame(clientInput, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"b5-off-test","version":"1"}}}`), authorizedexecute.DefaultLimits.FrameBytes))
	_, err = authorizedexecute.ReadBoundedFrame(clientOutput, authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	require.NoError(t, authorizedexecute.WriteSealedFrame(clientInput, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`), authorizedexecute.DefaultLimits.FrameBytes))
	listed, err := authorizedexecute.ReadBoundedFrame(clientOutput, authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(t, err)
	for _, name := range []string{"open_session", "close_session", "get_session_status", "begin_transaction", "execute_transaction_statement", "commit_transaction", "rollback_transaction", "get_transaction_status"} {
		require.NotContains(t, string(listed), `"`+name+`"`)
	}
	require.NoError(t, clientInput.Close())
	select {
	case err := <-runErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("feature-off stdio entrypoint did not stop after EOF")
	}
}

func TestMCPCommandKeepsStdoutCleanOnStartupFailure(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AGENTSQL_INSECURE", "1")
	t.Setenv("AGENTSQL_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := fmt.Sprintf(
		commandConfig,
		"127.0.0.1:7780",
		filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
		5000,
	)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode := run([]string{"mcp", "--config", path}, &stdout, &stderr)
	require.Equal(t, 1, exitCode)
	require.Empty(t, stdout.String())
	require.NotEmpty(t, stderr.String())
	require.NotContains(t, stderr.String(), "0123456789abcdef0123456789abcdef")
}

func TestServeExitCodes(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	tests := []struct {
		name         string
		httpListen   string
		sqlitePath   string
		timeoutMS    int
		secret       string
		expectedExit int
	}{
		{
			name:         "valid configuration requires secret",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "data", "agentsql.db")),
			timeoutMS:    5000,
			secret:       "",
			expectedExit: 1,
		},
		{
			name:         "missing sqlite path",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   "",
			timeoutMS:    5000,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
		{
			name:         "invalid port",
			httpListen:   "127.0.0.1:70000",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
			timeoutMS:    5000,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
		{
			name:         "negative timeout",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
			timeoutMS:    -1,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_SECRET", test.secret)
			if test.secret == "0123456789abcdef0123456789abcdef" {
				t.Setenv("AGENTSQL_INSECURE", "1")
			} else {
				t.Setenv("AGENTSQL_INSECURE", "")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			contents := fmt.Sprintf(commandConfig, test.httpListen, test.sqlitePath, test.timeoutMS)
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

			exitCode := run([]string{"serve", "--config", path}, io.Discard, io.Discard)
			require.Equal(t, test.expectedExit, exitCode)
		})
	}
}

func TestServeConsoleRequiresAdminPassword(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AGENTSQL_INSECURE", "1")
	t.Setenv("AGENTSQL_ADMIN_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := fmt.Sprintf(commandConfig, "127.0.0.1:7780", filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")), 5000)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	require.Equal(t, 1, run([]string{"serve", "--config", path}, io.Discard, io.Discard))
}

func TestServeStartupSecurityMatrix(t *testing.T) {
	const (
		publicSecret = "0123456789abcdef0123456789abcdef"
		strongSecret = "a7f3c91e5b2d4806af15ce9034d77b21"
		strongPass   = "Str0ng!Passphrase_2026"
	)
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")

	failureTests := []struct {
		name       string
		secret     string
		password   string
		insecure   string
		console    bool
		want       string
		forbidLeak string
	}{
		{name: "missing secret", password: strongPass, console: true, want: "AGENTSQL_SECRET is required"},
		{name: "wrong secret length", secret: "short-secret", password: strongPass, console: true, want: "must be exactly 32 bytes", forbidLeak: "short-secret"},
		{name: "public secret in secure mode", secret: publicSecret, password: strongPass, console: true, want: "publicly known example value", forbidLeak: publicSecret},
		{name: "weak admin password", secret: strongSecret, password: "password", console: true, want: "AGENTSQL_ADMIN_PASSWORD is too weak", forbidLeak: "password"},
		{name: "invalid insecure value", secret: strongSecret, password: strongPass, insecure: "true", console: true, want: `AGENTSQL_INSECURE must be unset or exactly \"1\"`},
		{name: "insecure still rejects missing secret", password: "admin", insecure: "1", console: true, want: "AGENTSQL_SECRET is required"},
		{name: "insecure still rejects wrong length", secret: "short-secret", password: "admin", insecure: "1", console: true, want: "must be exactly 32 bytes", forbidLeak: "short-secret"},
		{name: "insecure still rejects empty password", secret: publicSecret, insecure: "1", console: true, want: "AGENTSQL_ADMIN_PASSWORD is required"},
	}
	for _, test := range failureTests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_SECRET", test.secret)
			t.Setenv("AGENTSQL_ADMIN_USER", "admin")
			t.Setenv("AGENTSQL_ADMIN_PASSWORD", test.password)
			t.Setenv("AGENTSQL_INSECURE", test.insecure)
			databasePath := filepath.Join(t.TempDir(), "metadata", "agentsql.db")
			configPath := writeCommandConfig(t, databasePath, "127.0.0.1:7780", test.console)
			var stderr strings.Builder
			require.Equal(t, 1, run([]string{"serve", "--config", configPath}, io.Discard, &stderr))
			require.Contains(t, stderr.String(), test.want)
			if test.forbidLeak != "" {
				require.NotContains(t, stderr.String(), test.forbidLeak)
			}
			_, err := os.Stat(databasePath)
			require.True(t, os.IsNotExist(err), stderr.String())
		})
	}

	passToAssembleTests := []struct {
		name     string
		secret   string
		password string
		insecure string
		console  bool
	}{
		{name: "strong credentials", secret: strongSecret, password: strongPass, console: true},
		{name: "insecure public and weak credentials", secret: publicSecret, password: "admin", insecure: "1", console: true},
		{name: "console disabled without admin password", secret: strongSecret, console: false},
	}
	for _, test := range passToAssembleTests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_SECRET", test.secret)
			t.Setenv("AGENTSQL_ADMIN_USER", "admin")
			t.Setenv("AGENTSQL_ADMIN_PASSWORD", test.password)
			t.Setenv("AGENTSQL_INSECURE", test.insecure)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			// The occupied listener proves startup reached HTTP assembly. Use an
			// in-memory database so transient Windows file retention cannot make
			// TempDir's one-shot cleanup flaky.
			databasePath := ":memory:"
			configPath := writeCommandConfig(t, databasePath, listener.Addr().String(), test.console)
			var stderr strings.Builder
			require.Equal(t, 1, run([]string{"serve", "--config", configPath}, io.Discard, &stderr))
			require.NotEmpty(t, stderr.String())
			require.NotContains(t, stderr.String(), test.secret)
			if test.password != "" {
				require.NotContains(t, stderr.String(), test.password)
			}
			if test.insecure == "1" {
				require.Contains(t, stderr.String(), "AGENTSQL_INSECURE=1 enabled")
			}
		})
	}
}

func TestServeDemoCredentialStartupMatrix(t *testing.T) {
	const (
		secret = "0123456789abcdef0123456789abcdef"
		roKey  = "asql_serve-demo-ro-canary"
		dmlKey = "asql_serve-demo-dml-canary"
		badKey = "asql_serve-demo-invalid-canary"
	)
	tests := []struct {
		name     string
		roEnv    string
		dmlEnv   string
		roAgent  model.Agent
		dmlAgent model.Agent
		wantErr  bool
	}{
		{
			name: "missing readonly key", dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "missing dml key", roEnv: roKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "authentication failure", roEnv: badKey, dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "readonly key bound to dml agent", roEnv: dmlKey, dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "readonly level mismatch", roEnv: roKey, dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "dml"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "readonly status not active", roEnv: roKey, dmlEnv: dmlKey,
			roAgent: func() model.Agent {
				agent := validDemoAgent(config.DemoAgentRO, "readonly")
				agent.Status = "disabled"
				return agent
			}(),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "dml key bound to readonly agent", roEnv: roKey, dmlEnv: roKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"), wantErr: true,
		},
		{
			name: "dml level mismatch", roEnv: roKey, dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "readonly"), wantErr: true,
		},
		{
			name: "dml status not active", roEnv: roKey, dmlEnv: dmlKey,
			roAgent: validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: func() model.Agent {
				agent := validDemoAgent(config.DemoAgentDML, "dml")
				agent.Status = "disabled"
				return agent
			}(),
			wantErr: true,
		},
		{
			name: "both identities valid", roEnv: roKey, dmlEnv: dmlKey,
			roAgent:  validDemoAgent(config.DemoAgentRO, "readonly"),
			dmlAgent: validDemoAgent(config.DemoAgentDML, "dml"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_DEMO_RO_KEY", test.roEnv)
			t.Setenv("AGENTSQL_DEMO_DML_KEY", test.dmlEnv)
			cfg, runtime := newDemoStartupRuntime(t, true, secret)
			createDemoStartupAgent(t, runtime, test.roAgent, roKey)
			createDemoStartupAgent(t, runtime, test.dmlAgent, dmlKey)

			prepared, err := adminapi.PrepareDemoDeps(context.Background(), adminapi.Deps{
				Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: "password",
				TokenKey: adminapi.DeriveTokenKey([]byte(secret)),
			})
			if test.wantErr {
				require.Error(t, err)
				for _, forbidden := range []string{roKey, dmlKey, badKey, "asql_", "postgres://", "password@"} {
					require.NotContains(t, err.Error(), forbidden)
				}
				return
			}
			require.NoError(t, err)
			_, err = adminapi.NewHandler(prepared, zerolog.Nop())
			require.NoError(t, err, "successful handler construction proves the private demo keys and runner were attached")
		})
	}
}

func TestServeDemoDisabledDoesNotRequireDemoKeys(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	t.Setenv("AGENTSQL_DEMO_RO_KEY", "")
	t.Setenv("AGENTSQL_DEMO_DML_KEY", "")
	cfg, runtime := newDemoStartupRuntime(t, false, secret)
	prepared, err := adminapi.PrepareDemoDeps(context.Background(), adminapi.Deps{
		Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: "password",
		TokenKey: adminapi.DeriveTokenKey([]byte(secret)),
	})
	require.NoError(t, err)
	_, err = adminapi.NewHandler(prepared, zerolog.Nop())
	require.NoError(t, err)
}

func newDemoStartupRuntime(
	t *testing.T,
	enabled bool,
	secret string,
) (config.Config, *bootstrap.Runtime) {
	t.Helper()
	cfg := config.Config{
		Server: config.ServerConfig{
			HTTPListen: "127.0.0.1:7780", ConsoleEnabled: true,
			EventStream: false, EventStreamMaxConnections: 100,
		},
		Store: config.StoreConfig{SQLitePath: filepath.Join(t.TempDir(), "startup.db")},
		Defaults: config.DefaultsConfig{
			StatementTimeoutMS: 5000, RowLimit: 1000, MaxConnsPerDatasource: 5, QPSPerAgent: 20,
		},
		Theme: config.ThemeConfig{Default: "dark"},
		Demo: config.DemoConfig{
			Enabled: enabled,
			AllowedDatasourceIDs: []string{
				config.DemoDatasourcePG,
				config.DemoDatasourceMySQL,
			},
		},
	}
	runtime, err := bootstrap.Assemble(context.Background(), cfg, []byte(secret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	return cfg, runtime
}

func createDemoStartupAgent(
	t *testing.T,
	runtime *bootstrap.Runtime,
	agent model.Agent,
	rawKey string,
) {
	t.Helper()
	agent.Name = agent.ID
	agent.APIKeyHash = store.HashAPIKey(rawKey)
	_, err := runtime.Store.Agents().Create(context.Background(), agent)
	require.NoError(t, err)
}

func validDemoAgent(id, level string) model.Agent {
	return model.Agent{ID: id, Level: level, Status: "active"}
}

func writeCommandConfig(t *testing.T, databasePath, listen string, console bool) string {
	t.Helper()
	contents := fmt.Sprintf(commandConfig, listen, filepath.ToSlash(databasePath), 5000)
	contents = strings.Replace(contents, "console_enabled: true", fmt.Sprintf("console_enabled: %t", console), 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func zerologForTest(t *testing.T) zerolog.Logger {
	t.Helper()
	logger, err := newLogger(io.Discard)
	require.NoError(t, err)
	return logger
}
