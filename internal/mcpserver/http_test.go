package mcpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	initializeRequest  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	listToolsRequest   = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	listSourcesRequest = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_datasources","arguments":{}}}`
	b2FeatureOffJSON   = `"b2":{"state":"feature-off","reason":"B2_FEATURE_OFF","protocol":2}`
)

func TestT241HealthAndReadinessBypassAgentAuthentication(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	webCalls := 0
	webConsole := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		webCalls++
		writer.WriteHeader(http.StatusTeapot)
	})
	handler, err := NewHTTPHandler(
		fixture.runtime,
		httpTestConfig(100),
		zerolog.Nop(),
		WithWebConsole(webConsole),
	)
	require.NoError(t, err)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.Equal(t, "application/json", health.Header().Get("Content-Type"))
	require.Equal(t, "no-store", health.Header().Get("Cache-Control"))
	require.JSONEq(t, fmt.Sprintf(`{"status":"ok","version":%q,%s}`, version.Version, b2FeatureOffJSON), health.Body.String())
	require.Zero(t, webCalls)

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, ready.Code)
	require.JSONEq(t, `{"status":"ready",`+b2FeatureOffJSON+`}`, ready.Body.String())
	require.Zero(t, webCalls)

	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	handler.ServeHTTP(unauthorized, request)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.JSONEq(t, `{"error":"unauthorized"}`, unauthorized.Body.String())
	require.Zero(t, webCalls)
}

func TestHealthDemoProjectionIsMinimalAndDoesNotLeak(t *testing.T) {
	const (
		secretCanary   = "health-secret-canary-32-bytes!!!"
		dsnCanary      = "health-dsn-password-canary"
		passwordCanary = "health-datasource-password-canary"
		keyCanary      = "asql_health-demo-key-canary"
		hostCanary     = "health-private-host-canary.invalid"
		usernameCanary = "health-admin-username-canary"
	)
	keyHashCanary := strings.Repeat("ab", 32)
	t.Setenv("AGENTSQL_SECRET", secretCanary)
	t.Setenv("AGENTSQL_DEMO_RO_KEY", keyCanary)
	t.Setenv("AGENTSQL_ADMIN_USER", usernameCanary)

	fixture := newMCPFixture(t, "dml")
	storedAgent, err := fixture.runtime.Store.Agents().Get(context.Background(), fixture.agent.ID)
	require.NoError(t, err)
	storedAgent.APIKeyHash = keyHashCanary
	_, err = fixture.runtime.Store.Agents().Update(context.Background(), storedAgent)
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(context.Background(), model.Datasource{
		ID: "ds-health-canary", Name: "Health Canary", DBType: "postgres",
		Host: hostCanary, Port: 5432, Database: "health", Username: usernameCanary,
		ConnLimit: 2, StmtTimeoutMS: 1_500, RowLimit: 20,
	}, passwordCanary)
	require.NoError(t, err)

	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = ""
	cfg.Store.Metadata = &config.MetadataStoreConfig{
		Driver: "postgres",
		DSN:    "postgres://demo:" + dsnCanary + "@" + hostCanary + "/agentsql",
	}
	cfg.Demo = config.DemoConfig{
		Enabled:              true,
		Banner:               config.DemoDefaultBanner,
		AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL},
		QPSPerAgent:          config.DemoDefaultQPSPerAgent,
	}
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.Equal(t, "no-store", health.Header().Get("Cache-Control"))
	require.JSONEq(t, fmt.Sprintf(
		`{"status":"ok","version":%q,"demo":{"enabled":true,"banner":%q},%s}`,
		version.Version,
		config.DemoDefaultBanner,
		b2FeatureOffJSON,
	), health.Body.String())

	var projection map[string]any
	require.NoError(t, json.Unmarshal(health.Body.Bytes(), &projection))
	require.ElementsMatch(t, []string{"status", "version", "demo", "b2"}, mapKeys(projection))
	demoProjection, ok := projection["demo"].(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"enabled", "banner"}, mapKeys(demoProjection))

	responseText := health.Body.String() + fmt.Sprint(health.Header())
	for _, canary := range []string{
		secretCanary,
		dsnCanary,
		passwordCanary,
		keyCanary,
		keyHashCanary,
		hostCanary,
		usernameCanary,
		"password_enc",
		config.DemoDatasourcePG,
		config.DemoDatasourceMySQL,
	} {
		require.NotContains(t, responseText, canary)
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, ready.Code)
	require.JSONEq(t, `{"status":"ready",`+b2FeatureOffJSON+`}`, ready.Body.String())
	require.Empty(t, ready.Header().Get("Cache-Control"))
}

func TestT241ReadinessFailsClosedWithoutStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	readinessHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"status":"not ready","b2":{"state":"degraded","reason":"B2_METADATA_UNAVAILABLE","protocol":0}}`, recorder.Body.String())
}

func TestB5StatusLinksHealthAndReadinessOnlyWhenExplicitlyInstalled(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	fixture.runtime.SetB5StatusProvider(func(context.Context) (bootstrap.B5Status, error) {
		return bootstrap.B5Status{Enabled: true, State: "DEGRADED", Reason: "B5_QUARANTINE_BUDGET", Ready: false}, nil
	})
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.Contains(t, health.Body.String(), `"b5":{"enabled":true,"state":"DEGRADED","reason":"B5_QUARANTINE_BUDGET","ready":false}`)

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	require.Contains(t, ready.Body.String(), `"status":"not ready"`)
	require.Contains(t, ready.Body.String(), `"reason":"B5_QUARANTINE_BUDGET"`)
}

func TestReadinessRemainsReadyWhenHMACChainKeyMissing(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	good := readinessChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
	opened, _ := openReadinessChainStore(t, good)
	missing := readinessChainManifest{mode: "hmac", version: 1}
	monitor := bootstrap.NewChainMonitor(
		opened,
		readinessChainProvider{manifest: missing},
		nil,
		[]string{"management"},
		time.Hour,
	)
	runtime := &bootstrap.Runtime{Store: opened, ChainMonitor: monitor}
	monitor.RunChainVerificationOnce(context.Background())
	require.False(t, runtime.AuditWriterReady("management"))

	recorder := httptest.NewRecorder()
	readinessHandler(runtime).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"status":"ready",`+b2FeatureOffJSON+`}`, recorder.Body.String())
}

func TestReadinessRemainsReadyWhenAuditHistoryIsBroken(t *testing.T) {
	manifest := readinessChainManifest{mode: "keyless"}
	opened, database := openReadinessChainStore(t, manifest)
	_, err := database.Exec(`DROP TRIGGER trg_audit_logs_chain_contract_update`)
	require.NoError(t, err)
	_, err = database.Exec(`UPDATE audit_logs SET details_json = '{"tampered":true}' WHERE id = (SELECT MIN(id) FROM audit_logs)`)
	require.NoError(t, err)
	monitor := bootstrap.NewChainMonitor(
		opened,
		readinessChainProvider{manifest: manifest},
		nil,
		[]string{"management"},
		time.Hour,
	)
	runtime := &bootstrap.Runtime{Store: opened, ChainMonitor: monitor}
	monitor.RunChainVerificationOnce(context.Background())
	require.True(t, runtime.AuditWriterReady("management"))

	recorder := httptest.NewRecorder()
	readinessHandler(runtime).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"status":"ready",`+b2FeatureOffJSON+`}`, recorder.Body.String())
}

type readinessChainManifest struct {
	mode    string
	version int
	keys    map[int][]byte
}

func (manifest readinessChainManifest) ExpectedMode(context.Context) (string, error) {
	return manifest.mode, nil
}

func (manifest readinessChainManifest) CurrentKeyVersion(context.Context) (int, error) {
	return manifest.version, nil
}

func (manifest readinessChainManifest) ChainKeyForVersion(_ context.Context, version int) ([]byte, error) {
	key, exists := manifest.keys[version]
	if !exists {
		return nil, fmt.Errorf("chain key version %d is unavailable", version)
	}
	return key, nil
}

type readinessChainProvider struct {
	manifest store.ChainManifest
}

func (provider readinessChainProvider) ChainManifestForDomain(context.Context, string) (store.ChainManifest, error) {
	return provider.manifest, nil
}

func openReadinessChainStore(t *testing.T, manifest store.ChainManifest) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chain-readiness.db")
	opened, err := store.OpenWithSecret(context.Background(), path, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	details := `{"test":true}`
	_, err = opened.AuditLogs().Insert(context.Background(), model.AuditLog{Decision: "allow", DetailsJSON: &details})
	require.NoError(t, err)
	require.NoError(t, store.NewChainProvisioner(
		database, store.DialectSQLite, "management", manifest, store.BackfillConfig{},
	).Provision(context.Background(), "readiness-test"))
	return opened, database
}

func TestRedactionReadinessRequiresFreshReconciliation(t *testing.T) {
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "readiness.db")
	// This test isolates redaction readiness; B5 production-default readiness
	// has its own coverage and is explicitly rolled back here.
	cfg.MCP = config.MCPConfig{Sessions: config.MCPSessionsConfig{Enabled: false, IdleTTLMS: 600_000, AbsoluteTTLMS: 3_600_000},
		Transactions: config.MCPTransactionsConfig{Postgres: false, IdleTimeoutMS: 15_000, WallTimeoutMS: 60_000, StatementTimeoutMS: 5_000, ShutdownDrainMS: 5_000}}
	encoded := base64.StdEncoding.EncodeToString([]byte("0123456789abcdefghijklmnopqrstuv"))
	cfg.Redaction.HashKeys = &config.RedactionHashKeysConfig{
		ActiveVersion: 1,
		Keys:          []config.RedactionHashKeySpec{{ID: 1, KeyB64: &encoded}},
	}
	runtime, err := bootstrap.Assemble(context.Background(), cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)

	assertReady := func(status int, body string) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, status, recorder.Code, recorder.Body.String())
		require.JSONEq(t, body, recorder.Body.String())
	}
	assertReady(http.StatusServiceUnavailable, `{"status":"not ready","redaction_unsatisfied":[3],`+b2FeatureOffJSON+`}`)

	result, available := runtime.RedactionReconciliation()
	require.True(t, available)
	require.Len(t, result.Observed.Keys, 1)
	require.NoError(t, runtime.Store.RedactionKeys().RegisterStandby(
		context.Background(), "1", result.Observed.Keys[0].Commitment, "current", result.Observed.Revision, nil,
	))
	_, _, _, err = runtime.Store.RedactionKeys().MarkActiveCAS(context.Background(), "1")
	require.NoError(t, err)
	assertReady(http.StatusServiceUnavailable, `{"status":"not ready","redaction_unsatisfied":[3],`+b2FeatureOffJSON+`}`)

	require.NoError(t, runtime.RerunRedactionReconciliation(context.Background()))
	assertReady(http.StatusOK, `{"status":"ready",`+b2FeatureOffJSON+`}`)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
}

func TestStreamableHTTPEndToEndSevenTools(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	for _, body := range []string{initializeRequest, listToolsRequest, listSourcesRequest} {
		status, responseBody, err := doMCPRequest(server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, responseBody)
		if body == listToolsRequest {
			for _, name := range []string{
				"list_datasources", "list_schema", "explain_query", "query",
				"execute_write", "request_approval", "get_approval_result",
			} {
				require.Equal(t, 1, strings.Count(responseBody, `"`+name+`"`), name)
			}
			require.NotContains(t, responseBody, "execute_raw_sql")
		}
		if body == initializeRequest {
			require.Contains(t, responseBody, `"version":"`+version.Version+`"`)
		}
		if body == listSourcesRequest {
			require.Contains(t, responseBody, "ds-allowed")
			require.NotContains(t, responseBody, "ds-hidden")
			require.NotContains(t, responseBody, "database-password")
			require.NotContains(t, responseBody, "db.internal")
		}
	}
	require.Equal(t, 1, registry.serverCount())
}

func TestHTTPAuthenticationFailuresStopBeforeSDK(t *testing.T) {
	tests := []struct {
		name              string
		authorization     string
		wellFormedUnknown bool
		disableAgent      bool
		invalidLevel      bool
	}{
		{name: "missing header"},
		{name: "wrong scheme", authorization: "Basic abc"},
		{name: "non exact Bearer spacing", authorization: "Bearer  asql_wrong"},
		{name: "illegal key", authorization: "Bearer not-an-agentsql-key"},
		{name: "well formed unknown key", wellFormedUnknown: true},
		{name: "disabled Agent", disableAgent: true},
		{name: "invalid Agent level", invalidLevel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCPFixture(t, "dml")
			key := fixture.handlers.apiKey
			authorization := test.authorization
			if test.wellFormedUnknown {
				unknownKey, _, err := store.GenerateAPIKey()
				require.NoError(t, err)
				authorization = "Bearer " + unknownKey
			}
			if test.disableAgent || test.invalidLevel {
				agent, err := fixture.runtime.Store.Agents().Get(context.Background(), fixture.agent.ID)
				require.NoError(t, err)
				if test.disableAgent {
					agent.Status = "disabled"
				}
				if test.invalidLevel {
					agent.Level = "invalid"
				}
				_, err = fixture.runtime.Store.Agents().Update(context.Background(), agent)
				require.NoError(t, err)
			} else if authorization != "" {
				key = strings.TrimPrefix(authorization, "Bearer ")
			}
			handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeRequest))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			} else if test.disableAgent || test.invalidLevel {
				request.Header.Set("Authorization", "Bearer "+key)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			require.Equal(t, `{"error":"unauthorized"}`, recorder.Body.String())
			require.Zero(t, registry.serverCount())
			require.Zero(t, fixture.provider.calls())
			page, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 1)
			require.NoError(t, err)
			require.Zero(t, page.Total)
		})
	}
}

func TestHTTPMultiTenantFiftyConcurrentRequestsDoNotCross(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	keyB, hashB, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agentB, err := fixture.runtime.Store.Agents().Create(context.Background(), model.Agent{
		ID: "agent-b", Name: "Agent B", Status: "active", APIKeyHash: hashB, Level: "dml",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(context.Background(), model.Datasource{
		ID: "ds-b", Name: "B", DBType: "postgres", Host: "b.internal", Port: 5432,
		Database: "b", Username: "b", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "database-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(context.Background(), model.Policy{
		ID: "agent-b-policy", AgentID: agentB.ID, DatasourceID: "ds-b",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)

	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(1_000), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	type outcome struct {
		expected  string
		forbidden string
		body      string
		err       error
		status    int
	}
	results := make(chan outcome, 50)
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		key, expected, forbidden := fixture.handlers.apiKey, "ds-allowed", "ds-b"
		if index%2 == 1 {
			key, expected, forbidden = keyB, "ds-b", "ds-allowed"
		}
		wait.Add(1)
		go func(id int, key, expected, forbidden string) {
			defer wait.Done()
			body := strings.Replace(listSourcesRequest, `"id":3`, fmt.Sprintf(`"id":%d`, id+10), 1)
			status, responseBody, err := doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, body)
			results <- outcome{expected: expected, forbidden: forbidden, body: responseBody, err: err, status: status}
		}(index, key, expected, forbidden)
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status, result.body)
		require.Contains(t, result.body, result.expected)
		require.NotContains(t, result.body, result.forbidden)
	}
	require.Equal(t, 2, registry.serverCount())
	require.Equal(t, 2, registry.buildCount())
}

func TestHTTPStatusMatrix(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(1), zerolog.Nop())
		require.NoError(t, err)
		first := httptest.NewRecorder()
		handler.ServeHTTP(first, authorizedRequest(http.MethodPost, initializeRequest, fixture.handlers.apiKey))
		second := httptest.NewRecorder()
		handler.ServeHTTP(second, authorizedRequest(http.MethodPost, listToolsRequest, fixture.handlers.apiKey))
		require.Equal(t, http.StatusTooManyRequests, second.Code)
		require.Equal(t, `{"error":"rate limited"}`, second.Body.String())

		require.Eventually(t, func() bool {
			recovered := httptest.NewRecorder()
			handler.ServeHTTP(recovered, authorizedRequest(http.MethodPost, listToolsRequest, fixture.handlers.apiKey))
			return recovered.Code == http.StatusOK
		}, 2*time.Second, 25*time.Millisecond, "the per-Agent token bucket must recover")
	})
	t.Run("method not allowed", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodGet, "", fixture.handlers.apiKey))
		require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	})
	t.Run("malformed JSON", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, "{", fixture.handlers.apiKey))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
	t.Run("request too large", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		body := strings.Repeat("x", maxMCPRequestBodyBytes+1)
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, body, fixture.handlers.apiKey))
		require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	})
	t.Run("not found", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1", nil))
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Equal(t, `{"error":"not found"}`, recorder.Body.String())
	})
}

func TestAgentServerRegistryCacheKeyAndCapacity(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	registry := newAgentServerRegistry(fixture.runtime, zerolog.Nop(), 10)
	agent := fixture.agent
	agent.UpdatedAt = time.Date(2026, time.September, 16, 8, 0, 0, 0, time.UTC)
	first, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	reused, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	require.True(t, first == reused)
	agent.APIKeyHash = "rotated-hash"
	rebuilt, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	require.False(t, first == rebuilt)
	require.Equal(t, 2, registry.buildCount())

	registry = newAgentServerRegistry(fixture.runtime, zerolog.Nop(), 10)
	for index := 0; index <= agentServerRegistryLimit; index++ {
		candidate := fixture.agent
		candidate.ID = fmt.Sprintf("agent-%03d", index)
		candidate.UpdatedAt = agent.UpdatedAt
		_, err := registry.getOrCreate(candidate, fixture.handlers.apiKey)
		require.NoError(t, err)
	}
	require.Equal(t, 1, registry.serverCount())
	require.Equal(t, agentServerRegistryLimit+1, registry.buildCount())
}

func TestHTTPAgentKeyRotationRebuildsServerWithoutResettingLimiter(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	oldPlaintext := fixture.handlers.apiKey
	newPlaintext, newHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	fixedUpdatedAt := time.Date(2026, time.September, 16, 8, 0, 0, 0, time.UTC)
	oldSnapshot := fixture.agent
	oldSnapshot.UpdatedAt = fixedUpdatedAt
	newSnapshot := oldSnapshot
	newSnapshot.APIKeyHash = newHash
	otherSnapshot := newSnapshot
	otherSnapshot.ID = "agent-other"
	require.NotEqual(t, agentServerKey(oldSnapshot), agentServerKey(newSnapshot))
	require.NotEqual(t, agentServerKey(newSnapshot), agentServerKey(otherSnapshot))

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), logger)
	require.NoError(t, err)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, authorizedRequest(http.MethodPost, listSourcesRequest, oldPlaintext))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, 1, registry.buildCount())

	stored, err := fixture.runtime.Store.Agents().Get(context.Background(), fixture.agent.ID)
	require.NoError(t, err)
	stored.APIKeyHash = newHash
	_, err = fixture.runtime.Store.Agents().Update(context.Background(), stored)
	require.NoError(t, err)

	oldRequest := httptest.NewRecorder()
	handler.ServeHTTP(oldRequest, authorizedRequest(http.MethodPost, listSourcesRequest, oldPlaintext))
	require.Equal(t, http.StatusUnauthorized, oldRequest.Code)
	newRequest := httptest.NewRecorder()
	handler.ServeHTTP(newRequest, authorizedRequest(http.MethodPost, listSourcesRequest, newPlaintext))
	require.Equal(t, http.StatusOK, newRequest.Code, newRequest.Body.String())
	require.Equal(t, 2, registry.buildCount())
	require.Equal(t, 2, registry.serverCount())
	require.NotContains(t, logs.String(), oldPlaintext)
	require.NotContains(t, logs.String(), newPlaintext)

	limiterRegistry := newAgentServerRegistry(fixture.runtime, zerolog.Nop(), 1)
	require.True(t, limiterRegistry.allow(oldSnapshot))
	require.False(t, limiterRegistry.allow(newSnapshot), "API key rotation must not create a fresh per-Agent bucket")
}

func TestRecoverMiddlewareReturnsSafe500(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler := recoverMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret stack detail")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, `{"error":"internal error"}`, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "secret stack detail")
	require.Contains(t, logs.String(), "stack")
}

func TestAccessLogContainsMetadataButNoKeyOrSQL(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler := accessLogMiddleware(logger, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	identity := requestIdentity{
		agent:    model.Agent{ID: "agent-log"},
		plainKey: "asql_must_not_be_logged",
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("SELECT secret FROM vault"))
	request.Header.Set("Authorization", "Bearer "+identity.plainKey)
	request = request.WithContext(context.WithValue(request.Context(), requestIdentityContextKey{}, identity))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Contains(t, logs.String(), "agent-log")
	require.Contains(t, logs.String(), "POST")
	require.Contains(t, logs.String(), "/mcp")
	require.Contains(t, logs.String(), "204")
	require.NotContains(t, logs.String(), identity.plainKey)
	require.NotContains(t, logs.String(), "SELECT secret")
}

func TestNewHTTPHandlerFailsClosed(t *testing.T) {
	_, err := NewHTTPHandler(nil, httpTestConfig(10), zerolog.Nop())
	require.Error(t, err)
	fixture := newMCPFixture(t, "dml")
	cfg := httpTestConfig(10)
	cfg.Defaults.QPSPerAgent = 0
	_, err = NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.Error(t, err)
}

func authorizedRequest(method, body, key string) *http.Request {
	request := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	return request
}

func doMCPRequest(
	client *http.Client,
	url string,
	key string,
	method string,
	body string,
) (int, string, error) {
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, string(contents), nil
}

func httpTestConfig(qps int) config.Config {
	return config.Config{
		Server: config.ServerConfig{HTTPListen: "127.0.0.1:8650", EventStreamMaxConnections: 100},
		Store:  config.StoreConfig{SQLitePath: "test.db"},
		Defaults: config.DefaultsConfig{
			StatementTimeoutMS:    5_000,
			RowLimit:              1_000,
			MaxConnsPerDatasource: 5,
			QPSPerAgent:           qps,
		},
		Theme: config.ThemeConfig{Default: "dark"},
	}
}

func TestClassifyRoute(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "auth action", path: "/api/v1/auth/login", want: "/api/v1/auth/login"},
		{name: "audit action", path: "/api/v1/audit/export", want: "/api/v1/audit/export"},
		{name: "dashboard action", path: "/api/v1/dashboard/summary", want: "/api/v1/dashboard/summary"},
		{name: "playground action", path: "/api/v1/playground/assess", want: "/api/v1/playground/assess"},
		{name: "redaction keys", path: "/api/v1/redaction/keys", want: "/api/v1/redaction/keys"},
		{name: "b5 sessions", path: "/api/v1/b5/sessions", want: "/api/v1/b5/sessions"},
		{name: "b5 transaction id", path: "/api/v1/b5/transactions/tx-secret", want: "/api/v1/b5/transactions/{id}"},
		{name: "b5 quarantine operation", path: "/api/v1/b5/quarantine/lease-secret/confirm-discard", want: "/api/v1/b5/quarantine/{id}/confirm-discard"},
		{name: "unknown b5 operation", path: "/api/v1/b5/quarantine/lease-secret/release", want: "/other"},
		{name: "event stream", path: "/api/v1/stream", want: "/api/v1/stream"},
		{name: "unknown auth action", path: "/api/v1/auth/unknown", want: "/other"},
		{name: "unknown audit action", path: "/api/v1/audit/unknown", want: "/other"},
		{name: "unknown dashboard action", path: "/api/v1/dashboard/unknown", want: "/other"},
		{name: "unknown playground action", path: "/api/v1/playground/unknown", want: "/other"},
		{name: "action with extra segment", path: "/api/v1/audit/export/extra", want: "/other"},
		{name: "agent id", path: "/api/v1/agents/ag-1", want: "/api/v1/agents/{id}"},
		{name: "datasource id", path: "/api/v1/datasources/ds-1", want: "/api/v1/datasources/{id}"},
		{name: "policy id", path: "/api/v1/policies/p-1", want: "/api/v1/policies/{id}"},
		{name: "rule id", path: "/api/v1/rules/r-1", want: "/api/v1/rules/{id}"},
		{name: "mask rule id", path: "/api/v1/mask_rules/m-1", want: "/api/v1/mask_rules/{id}"},
		{name: "approval id", path: "/api/v1/approvals/a-1", want: "/api/v1/approvals/{id}"},
		{name: "agent action", path: "/api/v1/agents/ag-1/rotate-key", want: "/api/v1/agents/{id}/rotate-key"},
		{name: "datasource action", path: "/api/v1/datasources/ds-1/ping", want: "/api/v1/datasources/{id}/ping"},
		{name: "approval action", path: "/api/v1/approvals/a-1/decide", want: "/api/v1/approvals/{id}/decide"},
		{name: "unknown id action", path: "/api/v1/agents/ag-1/unknown", want: "/other"},
		{name: "unknown resource", path: "/api/v1/widgets/widget-1", want: "/other"},
		{name: "non api path", path: "/not-api", want: "/other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, classifyRoute(test.path))
		})
	}
}

func TestStatusResponseWriterUnwrapsForResponseController(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapped := &statusResponseWriter{ResponseWriter: underlying, status: http.StatusOK}
	require.Same(t, underlying, wrapped.Unwrap())
	require.NoError(t, http.NewResponseController(wrapped).Flush())
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
