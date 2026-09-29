package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/discovery"
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
		hashKey          = "pg18-redaction-hash-key-0123456789ab"
		hashRaw          = "T41_PG18_RAW_SENTINEL_b7a3"
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
  name text NOT NULL,
  email text NOT NULL,
  phone text NOT NULL,
  balance integer NOT NULL,
  price numeric NOT NULL,
  stock integer NOT NULL,
  amount numeric NOT NULL,
  created_at timestamp NOT NULL,
  ordered_at date NOT NULL,
  full_name text NOT NULL,
  id_card text NOT NULL,
  legacy_id_card text NOT NULL,
  bank_card_plain text NOT NULL,
  bank_card_formatted text NOT NULL,
  client_ip inet NOT NULL,
  ip_network cidr NOT NULL,
  birth_date date NOT NULL
)`)
	require.NoError(t, err)
	_, err = businessPool.Exec(ctx, `
INSERT INTO public.customers (
  id, name, email, phone, balance, price, stock, amount, created_at, ordered_at, full_name,
  id_card, legacy_id_card, bank_card_plain, bank_card_formatted,
  client_ip, ip_network, birth_date
)
VALUES
 (1, '`+hashRaw+`', 'alice@example.com', '13812345678', 100, 19.99, 10, 49.99, TIMESTAMP '2026-09-01 08:30:00', DATE '2026-09-02', 'Alice Example', '11010519491231002X', '130503670401001',
  '4111111111111111', '4111 1111-1111 1111', '192.0.2.10', '198.51.100.0/24', DATE '2000-02-29'),
 (2, 'Ordinary Bob', 'bob@example.com', '13987654321', 200, 29.99, 20, 59.99, TIMESTAMP '2026-09-03 09:30:00', DATE '2026-09-04', 'Bob Example', '11010519491231002X', '130503670401001',
  '5555555555554444', '5555-5555-5555-4444', '2001:db8::1', '2001:db8::/48', DATE '1990-01-02'),
 (3, 'Ordinary Carol', 'carol@example.com', '13711112222', 300, 39.99, 30, 69.99, TIMESTAMP '2026-09-05 10:30:00', DATE '2026-09-06', 'Carol Example', 'PG_CONTROL_INVALID_ID_SENTINEL_1c93', '130503670401001',
  '378282246310005', '3782 822463 10005', '203.0.113.9', '203.0.113.0/24', DATE '1988-12-31')`)
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
	seed, err := store.OpenMetadata(ctx, store.MetadataOptions{
		Driver: store.DialectPostgres, PostgresDSN: metadataDSN,
		MaxOpenConns: 4, MaxIdleConns: 2, AutoMigrate: true,
	}, []byte(adminTestSecret))
	require.NoError(t, err)
	_, err = seed.MaskRules().Create(ctx, model.MaskRule{
		ID: "pg-hash-gate-probe-a", ColumnName: "hash_gate_probe_a", SensitiveType: string(mask.TypeGeneric),
		Algo: string(mask.AlgoHash), Enabled: true,
	})
	require.NoError(t, err)
	_, err = seed.MaskRules().Create(ctx, model.MaskRule{
		ID: "pg-hash-gate-probe-b", ColumnName: "hash_gate_probe_b", SensitiveType: string(mask.TypeGeneric),
		Algo: string(mask.AlgoHash), Enabled: true,
	})
	require.NoError(t, err)
	require.NoError(t, seed.Close())
	previousHashKey, hashKeyWasSet := os.LookupEnv(config.RedactionHashKeyEnv)
	require.NoError(t, os.Unsetenv(config.RedactionHashKeyEnv))
	t.Cleanup(func() {
		if hashKeyWasSet {
			require.NoError(t, os.Setenv(config.RedactionHashKeyEnv, previousHashKey))
		} else {
			require.NoError(t, os.Unsetenv(config.RedactionHashKeyEnv))
		}
	})
	failedRuntime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.Nil(t, failedRuntime)
	require.ErrorIs(t, err, mask.ErrHashKeyRequired)
	staging, err := store.OpenMetadata(ctx, store.MetadataOptions{
		Driver: store.DialectPostgres, PostgresDSN: metadataDSN,
		MaxOpenConns: 4, MaxIdleConns: 2, AutoMigrate: true,
	}, []byte(adminTestSecret))
	require.NoError(t, err)
	for _, id := range []string{"pg-hash-gate-probe-a", "pg-hash-gate-probe-b"} {
		rule, getErr := staging.MaskRules().Get(ctx, id)
		require.NoError(t, getErr)
		rule.Enabled = false
		_, updateErr := staging.MaskRules().Update(ctx, rule)
		require.NoError(t, updateErr)
	}
	require.NoError(t, staging.Close())
	disabledRuntime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err, "disabled hash drafts must not require a key")
	require.NoError(t, disabledRuntime.Close())
	staging, err = store.OpenMetadata(ctx, store.MetadataOptions{
		Driver: store.DialectPostgres, PostgresDSN: metadataDSN,
		MaxOpenConns: 4, MaxIdleConns: 2, AutoMigrate: true,
	}, []byte(adminTestSecret))
	require.NoError(t, err)
	for _, id := range []string{"pg-hash-gate-probe-a", "pg-hash-gate-probe-b"} {
		rule, getErr := staging.MaskRules().Get(ctx, id)
		require.NoError(t, getErr)
		rule.Enabled = true
		_, updateErr := staging.MaskRules().Update(ctx, rule)
		require.NoError(t, updateErr)
	}
	require.NoError(t, staging.Close())
	t.Setenv(config.RedactionHashKeyEnv, hashKey)
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NoError(t, runtime.Store.Ping(ctx))

	var handlerLogs bytes.Buffer
	logger := zerolog.New(&handlerLogs)
	adminHandler, err := NewHandler(Deps{
		Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: adminPassword,
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
	}, logger)
	require.NoError(t, err)
	handler, err := mcpserver.NewHTTPHandler(
		runtime, cfg, logger, mcpserver.WithAdminAPI(adminHandler),
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
	notificationBodies := make(chan []byte, 32)
	notificationReceiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		select {
		case notificationBodies <- body:
		default:
		}
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
				"decisions": []string{"deny", "allow"}, "allow_private_endpoints": true,
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
	adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", adminAuthorization, map[string]any{
			"id": "pg-name-hash", "datasource_id": "pg-demo", "table_name": "customers",
			"column_name": "name", "sensitive_type": "generic", "algo": "hash",
		})
	rangeRuleBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", adminAuthorization, map[string]any{
			"id": "pg-balance-range", "datasource_id": "pg-demo", "table_name": "customers",
			"column_name": "balance_range_probe", "sensitive_type": "number", "algo": "range",
			"range_bucket_width": 100,
		})
	var rangeRule adminE2EEnvelope[maskRuleView]
	require.NoError(t, json.Unmarshal(rangeRuleBody, &rangeRule))
	require.Zero(t, rangeRule.Code)
	require.Equal(t, "range", rangeRule.Data.Algo)
	require.Equal(t, "number", rangeRule.Data.SensitiveType)
	require.Equal(t, int64(100), *rangeRule.Data.RangeBucketWidth)
	require.Zero(t, *rangeRule.Data.RangeBucketOffset)
	require.Nil(t, rangeRule.Data.RangeGranularity)
	storedRangeRule, err := runtime.Store.MaskRules().Get(ctx, "pg-balance-range")
	require.NoError(t, err)
	require.Equal(t, int64(100), *storedRangeRule.RangeBucketWidth)
	require.Zero(t, *storedRangeRule.RangeBucketOffset)
	blockRuleBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/mask_rules", adminAuthorization, map[string]any{
			"id": "pg-secret-block", "datasource_id": "pg-demo", "table_name": "customers",
			"column_name": "blocked_secret", "sensitive_type": "generic", "algo": "block",
		})
	var blockRule adminE2EEnvelope[maskRuleView]
	require.NoError(t, json.Unmarshal(blockRuleBody, &blockRule))
	require.Equal(t, "block", blockRule.Data.Algo)
	require.Equal(t, "generic", blockRule.Data.SensitiveType)
	require.True(t, blockRule.Data.Enabled)
	for _, enabled := range []bool{false, true} {
		updatedBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPut,
			"/api/v1/mask_rules/pg-secret-block", adminAuthorization, map[string]any{
				"datasource_id": "pg-demo", "table_name": "customers", "column_name": "blocked_secret",
				"sensitive_type": "generic", "algo": "block", "enabled": enabled,
			})
		var updated adminE2EEnvelope[maskRuleView]
		require.NoError(t, json.Unmarshal(updatedBody, &updated))
		require.Equal(t, enabled, updated.Data.Enabled)
		require.Equal(t, "block", updated.Data.Algo)
	}
	storedBlockRule, err := runtime.Store.MaskRules().Get(ctx, "pg-secret-block")
	require.NoError(t, err)
	require.Equal(t, "block", storedBlockRule.Algo)
	require.Equal(t, "generic", storedBlockRule.SensitiveType)
	require.True(t, storedBlockRule.Enabled)
	ruleBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/rules", adminAuthorization, map[string]any{
			"id": "R105", "db_type": "postgres", "title": "Approve unindexed writes",
			"risk_level": 3, "pattern_type": "ast_match", "definition": `{}`, "enabled": true,
		})
	var createdRule adminE2EEnvelope[ruleView]
	require.NoError(t, json.Unmarshal(ruleBody, &createdRule))
	require.True(t, createdRule.Data.Enabled, "PostgreSQL boolean values must scan through the rule repository")

	sessionID := adminE2EMCPInitialize(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, 0)
	queryResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID, 1,
		"query", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "SELECT id, email, balance FROM public.customers ORDER BY id LIMIT 10",
		})
	require.Equal(t, "allow", queryResponse.Decision)
	queryData := decodeAdminE2EPipelineData(t, queryResponse.Data)
	require.NotNil(t, queryData.Result)
	require.Equal(t, []string{"id", "email", "balance"}, queryData.Result.Columns)
	require.Equal(t, [][]string{
		{"1", "a***@example.com", "100"},
		{"2", "b***@example.com", "200"},
		{"3", "c***@example.com", "300"},
	}, queryData.Result.Rows)
	require.Equal(t, 3, queryData.Redact.MaskedCells)
	require.Positive(t, queryData.AuditID)

	hashEvents, cancelHashEvents := runtime.Events.SubscribeLive()
	hashResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID, 2,
		"query", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "SELECT name FROM public.customers WHERE id = 1 LIMIT 1",
		})
	require.Equal(t, "allow", hashResponse.Decision)
	hashData := decodeAdminE2EPipelineData(t, hashResponse.Data)
	require.NotNil(t, hashData.Result)
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, hashData.Result.Rows[0][0])
	expectedHashRedactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "name", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash,
	}}, mask.WithHashKey([]byte(hashKey)))
	require.NoError(t, err)
	expectedHashResult, _ := expectedHashRedactor.Apply(model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{hashRaw}}})
	require.Equal(t, expectedHashResult.Rows[0][0], hashData.Result.Rows[0][0])
	hashResponseJSON, err := json.Marshal(hashResponse)
	require.NoError(t, err)
	require.NotContains(t, string(hashResponseJSON), hashRaw)
	hashPage, err := runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	var hashAudit model.AuditLog
	for _, candidate := range hashPage.List {
		if candidate.ID == hashData.AuditID {
			hashAudit = candidate
			break
		}
	}
	require.Equal(t, hashData.AuditID, hashAudit.ID)
	hashAuditJSON, err := json.Marshal(hashAudit)
	require.NoError(t, err)
	require.NotContains(t, string(hashAuditJSON), hashRaw)
	select {
	case hashEvent := <-hashEvents:
		hashEventJSON, marshalErr := json.Marshal(hashEvent)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(hashEventJSON), hashRaw)
		hashStreamJSON, marshalErr := json.Marshal(auditToStreamView(hashEvent.Audit))
		require.NoError(t, marshalErr)
		require.NotContains(t, string(hashStreamJSON), hashRaw)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hash audit event")
	}
	cancelHashEvents()
	require.NotContains(t, handlerLogs.String(), hashRaw)
	hashNotificationDeadline := time.NewTimer(2 * time.Second)
	defer hashNotificationDeadline.Stop()
	for {
		select {
		case notificationBody := <-notificationBodies:
			var projected map[string]any
			require.NoError(t, json.Unmarshal(notificationBody, &projected))
			if projected["audit_id"] != float64(hashData.AuditID) {
				continue
			}
			require.NotContains(t, string(notificationBody), hashRaw)
			require.NotContains(t, projected, "result")
			require.NotContains(t, projected, "redact")
			goto hashNotificationVerified
		case <-hashNotificationDeadline.C:
			t.Fatal("timed out waiting for hash query notification")
		}
	}

hashNotificationVerified:

	before := adminE2EBusinessRows(t, ctx, businessPool)
	deniedAuditIDs := make([]int64, 0, 2)
	for id, write := range []struct {
		sql    string
		reason string
	}{
		{sql: "UPDATE public.customers SET balance = 0", reason: "unsafe update regression"},
		{sql: "DELETE FROM public.customers", reason: "unsafe delete regression"},
	} {
		response := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID,
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

	approvalResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID, 4,
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
	require.EqualValues(t, 5, audits.Data.Total)
	require.Len(t, audits.Data.List, 5)
	wantAudits := map[int64]string{
		queryData.AuditID:    "allow",
		hashData.AuditID:     "allow",
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
	require.Equal(t, int64(5), dashboard.Data.KPI.TotalRequests)
	require.Equal(t, int64(2), dashboard.Data.KPI.Blocked)
	require.Zero(t, dashboard.Data.KPI.PendingApprovals)
	require.Equal(t, int64(1), dashboard.Data.KPI.ActiveAgents)
	require.Equal(t, int64(1), dashboard.Data.KPI.DatasourcesTotal)
	require.Equal(t, map[string]int64{"allow": 2, "deny": 2, "approve": 1, "warn": 0},
		adminE2EDecisionCounts(dashboard.Data.DecisionDistribution))
	require.Len(t, dashboard.Data.Trend14D, 1)
	require.Equal(t, int64(5), dashboard.Data.Trend14D[0].Total)

	// T40-b: the same database-row sentinels cross discovery, disabled draft
	// persistence, explicit enablement, MCP query redaction, audit/event/SSE
	// projection, handler logging, and notification projection.
	rawSentinels := []string{
		hashRaw,
		"13812345678", "19.99", "2026-09-01 08:30:00", "Alice Example",
		"11010519491231002X", "130503670401001", "4111111111111111", "4111 1111-1111 1111",
		"192.0.2.10", "192.0.2.10/32", "198.51.100.0/24", "2000-02-29", "2000-02-29T00:00:00Z",
	}
	discoverBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources/pg-demo/discover", adminAuthorization, map[string]any{
			"tables":      []map[string]string{{"schema": "public", "table": "customers"}},
			"categories":  []string{"phone", "email", "idcard", "bankcard", "ip", "birthdate", "number", "date", "generic"},
			"sample_rows": 3,
		})
	var discovered adminE2EEnvelope[discovery.ScanResult]
	require.NoError(t, json.Unmarshal(discoverBody, &discovered))
	require.Zero(t, discovered.Code)
	wantedColumns := map[string]bool{
		"id_card": true, "legacy_id_card": true, "bank_card_plain": true, "bank_card_formatted": true,
		"client_ip": true, "ip_network": true, "birth_date": true,
		"price": true, "stock": true, "amount": true,
		"created_at": true, "ordered_at": true, "full_name": true,
	}
	wantCategories := map[discovery.Category]int{
		discovery.CategoryPhone: 1, discovery.CategoryEmail: 1, discovery.CategoryIDCard: 2,
		discovery.CategoryBankCard: 2, discovery.CategoryIP: 2, discovery.CategoryBirthdate: 1,
		discovery.CategoryNumber: 4, discovery.CategoryDate: 2, discovery.CategoryGeneric: 2,
	}
	gotCategories := make(map[discovery.Category]int, len(wantCategories))
	applyItems := make([]map[string]any, 0, len(wantedColumns))
	for _, finding := range discovered.Data.Findings {
		gotCategories[finding.Category]++
		if !wantedColumns[finding.Column] {
			continue
		}
		require.True(t, finding.Applicable, finding.Column)
		require.NotNil(t, finding.RecommendedRule, finding.Column)
		item := map[string]any{
			"schema": finding.Schema, "table": finding.Table, "column": finding.Column,
			"category":       string(finding.Category),
			"sensitive_type": string(finding.RecommendedRule.SensitiveType),
			"algo":           string(finding.RecommendedRule.Algo),
		}
		if finding.RecommendedRule.Range != nil {
			item["range"] = finding.RecommendedRule.Range
		}
		applyItems = append(applyItems, item)
	}
	require.Equal(t, wantCategories, gotCategories, string(discoverBody))
	require.Len(t, applyItems, len(wantedColumns), string(discoverBody))
	managementEvents, cancelManagementEvents := runtime.Events.SubscribeLive()
	applyBody := adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPost,
		"/api/v1/datasources/pg-demo/discover/apply", adminAuthorization, map[string]any{"items": applyItems})
	var applyEvent eventbus.Event
	select {
	case applyEvent = <-managementEvents:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for discovery apply management event")
	}
	cancelManagementEvents()
	require.NotNil(t, applyEvent.Audit.Action)
	require.Equal(t, "discover.apply", *applyEvent.Audit.Action)
	applyEventJSON, err := json.Marshal(applyEvent)
	require.NoError(t, err)
	applyStreamJSON, err := json.Marshal(auditToStreamView(applyEvent.Audit))
	require.NoError(t, err)
	applyStreamFrame := fmt.Sprintf("event: audit\nid: %d\ndata:%s\n\n", applyEvent.Audit.ID, applyStreamJSON)
	for _, raw := range rawSentinels {
		require.NotContains(t, string(applyEventJSON), raw)
		require.NotContains(t, applyStreamFrame, raw)
	}
	var applied adminE2EEnvelope[discoveryApplyResponse]
	require.NoError(t, json.Unmarshal(applyBody, &applied))
	require.Equal(t, len(wantedColumns), applied.Data.Counts.Created)

	storedRules, err := runtime.Store.MaskRules().ListByDatasource(ctx, "pg-demo")
	require.NoError(t, err)
	enabledCount := 0
	for _, storedRule := range storedRules {
		if !wantedColumns[storedRule.ColumnName] {
			continue
		}
		require.False(t, storedRule.Enabled, storedRule.ColumnName)
		require.Empty(t, storedRule.SchemaName)
		require.Equal(t, "customers", storedRule.TableName)
		switch storedRule.ColumnName {
		case "price", "stock", "amount":
			require.Equal(t, "number", storedRule.SensitiveType)
			require.Equal(t, "block", storedRule.Algo)
			require.Nil(t, storedRule.RangeBucketWidth)
			continue
		case "created_at", "ordered_at":
			require.Equal(t, "date", storedRule.SensitiveType)
			require.Equal(t, "range", storedRule.Algo)
			require.NotNil(t, storedRule.RangeGranularity)
			require.Equal(t, "month", *storedRule.RangeGranularity)
			continue
		case "full_name":
			require.Equal(t, "generic", storedRule.SensitiveType)
			require.Equal(t, "block", storedRule.Algo)
			continue
		}
		adminE2ERequest(t, ctx, server.Client(), server.URL, http.MethodPut,
			"/api/v1/mask_rules/"+storedRule.ID, adminAuthorization, map[string]any{
				"datasource_id": "pg-demo", "schema_name": storedRule.SchemaName,
				"table_name": storedRule.TableName, "column_name": storedRule.ColumnName,
				"sensitive_type": storedRule.SensitiveType, "algo": storedRule.Algo, "enabled": true,
			})
		enabledCount++
	}
	require.Equal(t, 7, enabledCount)

	for {
		select {
		case <-notificationBodies:
		default:
			goto notificationsDrained
		}
	}

notificationsDrained:
	events, cancelEvents := runtime.Events.SubscribeLive()
	defer cancelEvents()
	sensitiveResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID, 40,
		"query", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "SELECT id_card, legacy_id_card, bank_card_plain, bank_card_formatted, client_ip, ip_network, birth_date FROM public.customers WHERE id = 1 LIMIT 1",
		})
	require.Equal(t, "allow", sensitiveResponse.Decision)
	sensitiveData := decodeAdminE2EPipelineData(t, sensitiveResponse.Data)
	require.NotNil(t, sensitiveData.Result)
	require.Equal(t, [][]string{{"110105********002X", "130503******001", "411111******1111", "411111******1111", "192.0.*.*", "198.51.*.*", "2000-**-**"}}, sensitiveData.Result.Rows)
	require.Equal(t, 7, sensitiveData.Redact.MaskedCells)
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeIDCard, 1: mask.TypeIDCard, 2: mask.TypeBankCard, 3: mask.TypeBankCard, 4: mask.TypeIP, 5: mask.TypeIP, 6: mask.TypeBirthDate}, sensitiveData.Redact.TouchedColumns)

	sensitiveResponseJSON, err := json.Marshal(sensitiveResponse)
	require.NoError(t, err)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 100)
	require.NoError(t, err)
	var persistedAudit model.AuditLog
	for _, candidate := range page.List {
		if candidate.ID == sensitiveData.AuditID {
			persistedAudit = candidate
			break
		}
	}
	require.Equal(t, sensitiveData.AuditID, persistedAudit.ID)
	persistedAuditJSON, err := json.Marshal(persistedAudit)
	require.NoError(t, err)
	var queryEvent eventbus.Event
	select {
	case queryEvent = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sensitive query audit event")
	}
	require.Equal(t, sensitiveData.AuditID, queryEvent.Audit.ID)
	eventJSON, err := json.Marshal(queryEvent)
	require.NoError(t, err)
	streamFrameJSON, err := json.Marshal(auditToStreamView(queryEvent.Audit))
	require.NoError(t, err)
	streamFrame := fmt.Sprintf("event: audit\nid: %d\ndata:%s\n\n", queryEvent.Audit.ID, streamFrameJSON)
	for _, raw := range rawSentinels {
		require.NotContains(t, string(discoverBody), raw)
		require.NotContains(t, string(applyBody), raw)
		require.NotContains(t, string(sensitiveResponseJSON), raw)
		require.NotContains(t, string(persistedAuditJSON), raw)
		require.NotContains(t, string(eventJSON), raw)
		require.NotContains(t, streamFrame, raw)
		require.NotContains(t, handlerLogs.String(), raw)
	}

	notificationDeadline := time.NewTimer(2 * time.Second)
	defer notificationDeadline.Stop()
	for {
		select {
		case notificationBody := <-notificationBodies:
			var projected map[string]any
			require.NoError(t, json.Unmarshal(notificationBody, &projected))
			if projected["audit_id"] != float64(sensitiveData.AuditID) {
				continue
			}
			for _, raw := range rawSentinels {
				require.NotContains(t, string(notificationBody), raw)
			}
			require.NotContains(t, projected, "result")
			require.NotContains(t, projected, "redact")
			goto notificationVerified
		case <-notificationDeadline.C:
			t.Fatal("timed out waiting for sensitive query notification")
		}
	}

notificationVerified:

	fallbackResponse := adminE2EMCPCall(t, ctx, server.Client(), server.URL, createdAgent.Data.APIKey, sessionID, 41,
		"query", map[string]any{
			"datasource_id": "pg-demo",
			"sql":           "SELECT id_card FROM public.customers WHERE id = 3 LIMIT 1",
		})
	fallbackData := decodeAdminE2EPipelineData(t, fallbackResponse.Data)
	require.Equal(t, [][]string{{mask.RedactedFallback}}, fallbackData.Result.Rows)
	fallbackJSON, err := json.Marshal(fallbackResponse)
	require.NoError(t, err)
	require.NotContains(t, string(fallbackJSON), "PG_CONTROL_INVALID_ID_SENTINEL_1c93")
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

func adminE2EMCPInitialize(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	apiKey string,
	id int,
) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "admin-e2e", "version": "1"},
		},
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
	sessionID := response.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must return a transport session ID")
	return sessionID
}

func adminE2EMCPCall(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	apiKey string,
	sessionID string,
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
	request.Header.Set("Mcp-Session-Id", sessionID)
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
