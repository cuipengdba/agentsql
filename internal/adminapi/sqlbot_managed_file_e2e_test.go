package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Run only against a disposable PostgreSQL database. The first HTTP response
// is deliberately discarded after publication, then the exact request is
// retried after closing and reopening the runtime against the durable store.
func TestManagedFileImportHTTPReplayPostgres(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_MANAGED_FILE_PG_URL")
	if dsn == "" {
		t.Skip("set AGENTSQL_MANAGED_FILE_PG_URL to a disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())
	var existing bool
	require.NoError(t, admin.QueryRow(ctx, `SELECT to_regnamespace('agentsql_files') IS NOT NULL OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname IN ('file_reader','file_writer'))`).Scan(&existing))
	if existing {
		t.Skip("refusing to touch an existing managed schema or reader role")
	}
	_, err = admin.Exec(ctx, `CREATE ROLE file_reader LOGIN PASSWORD 'reader-password'`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE ROLE file_writer LOGIN PASSWORD 'writer-password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE SCHEMA agentsql_files AUTHORIZATION file_writer`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `GRANT USAGE ON SCHEMA agentsql_files TO file_reader`)
	require.NoError(t, err)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, `DROP SCHEMA agentsql_files CASCADE`)
		_, _ = admin.Exec(cleanup, `DROP ROLE file_writer`)
		_, _ = admin.Exec(cleanup, `DROP ROLE file_reader`)
	}()

	cfg := adminTestConfig(filepath.Join(t.TempDir(), "managed-http.db"))
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close()) }()
	apiKey, apiHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "managed-http-agent", Name: "managed-http-agent",
		Status: "active", Level: "readonly", APIKeyHash: apiHash})
	require.NoError(t, err)
	reader, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "managed-http-read", Name: "managed-http-read",
		DBType: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"),
		Username: "file_reader", TLSMode: "disable", ConnLimit: 2, StmtTimeoutMS: 10000, RowLimit: 100}, "reader-password")
	require.NoError(t, err)
	writer := reader
	writer.ID, writer.Username = "managed-http-writer", "file_writer"
	physical, err := executor.NewManagedFileProvisioner(reader, writer, "writer-password")
	require.NoError(t, err)
	key := DeriveTokenKey([]byte(adminTestSecret))
	newHandler := func() http.Handler {
		handler, handlerErr := NewHandler(Deps{Runtime: runtime, ManagedFiles: physical, Config: cfg,
			AdminUsername: "admin", AdminPassword: "password", TokenKey: key}, zerolog.Nop())
		require.NoError(t, handlerErr)
		return handler
	}
	handler := newHandler()
	base, _, err := runtime.Store.SQLBotBindings().Create(ctx, store.SQLBotBinding{
		WorkspaceID: "1", DatasourceName: "managed-http-root", SQLBotUserID: "1",
		AgentSQLUserID: rbac.BootstrapUserID, AgentID: agent.ID, DatasourceID: reader.ID,
		SchemaName: "agentsql_files", Enabled: true}, rbac.BootstrapUserID)
	require.NoError(t, err)
	verifier := NewSQLBotDelegationVerifier(key, runtime.Store, rbac.NewService(runtime.Store.RBAC()))
	humanToken, _, err := verifier.issue(base, time.Now().UTC())
	require.NoError(t, err)
	data := []byte("region,amount\neast,10\nwest,20\n")
	selection := map[string]any{"filePath": "agentsql-file-123.csv", "sheets": []map[string]any{{"sheetName": "Sheet1",
		"fields": []map[string]string{{"fieldName": "region", "fieldType": "string"}, {"fieldName": "amount", "fieldType": "int"}}}}}
	call := func(body []byte) (int, []byte) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/sqlbot/managed-files/import", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+humanToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.Bytes()
	}
	body, err := json.Marshal(map[string]any{"binding_id": base.ID, "filename": "source.csv", "file_data": data, "selection": selection})
	require.NoError(t, err)
	status, lostResponse := call(body)
	require.Equal(t, http.StatusOK, status, string(lostResponse))
	// Reopen the control store and runtime to rule out an in-memory receipt.
	require.NoError(t, runtime.Close())
	runtime, err = bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	handler = newHandler()
	status, replay := call(body)
	require.Equal(t, http.StatusOK, status, string(replay))
	require.JSONEq(t, string(lostResponse), string(replay))
	var result struct {
		Data struct {
			OperationID string `json:"operation_id"`
			BindingID   string `json:"binding_id"`
			Sheets      []struct {
				TableName string `json:"tableName"`
			} `json:"sheets"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(replay, &result))
	require.Len(t, result.Data.Sheets, 1)
	item, err := runtime.Store.ManagedFileImports().GetByRequestKey(ctx, result.Data.BindingID)
	require.NoError(t, err)
	require.Equal(t, result.Data.OperationID, item.ID)
	require.Equal(t, "published", item.Status)
	var count int
	require.NoError(t, admin.QueryRow(ctx, `SELECT COUNT(*) FROM agentsql_files.`+pgx.Identifier{result.Data.Sheets[0].TableName}.Sanitize()).Scan(&count))
	require.Equal(t, 2, count)
	var physicalTables int
	require.NoError(t, admin.QueryRow(ctx, `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname='agentsql_files'`).Scan(&physicalTables))
	require.Equal(t, 1, physicalTables, "gateway replay must not create a second table")
	selection["sheets"] = []map[string]any{{"sheetName": "Sheet1", "fields": []map[string]string{
		{"fieldName": "region", "fieldType": "string"}, {"fieldName": "amount", "fieldType": "string"}}}}
	changed, err := json.Marshal(map[string]any{"binding_id": base.ID, "filename": "source.csv", "file_data": data, "selection": selection})
	require.NoError(t, err)
	status, _ = call(changed)
	require.Equal(t, http.StatusConflict, status, "changed valid sheet selection must not republish the same staged file")
	require.NoError(t, admin.QueryRow(ctx, `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname='agentsql_files'`).Scan(&physicalTables))
	require.Equal(t, 1, physicalTables)
}
