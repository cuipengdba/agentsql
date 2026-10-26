package adminapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/chatbi"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Run only against a disposable PostgreSQL database. The first HTTP response
// is deliberately discarded after publication, then the exact request is
// retried in a separate process against the same durable store.
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
	cfg.ColumnAuthorization = config.ColumnAuthorizationConfig{Enabled: true, InstanceID: "managed-http-test"}
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
	base, baseToken, err := runtime.Store.SQLBotBindings().Create(ctx, store.SQLBotBinding{
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
	type observedReply struct {
		status int
		body   []byte
	}
	observed := make(chan observedReply, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, request)
		observed <- observedReply{status: recorded.Code, body: append([]byte(nil), recorded.Body.Bytes()...)}
		connection, _, hijackErr := writer.(http.Hijacker).Hijack()
		if hijackErr == nil {
			_ = connection.Close() // Commit completed; drop the response on the wire.
		}
	}))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+"/api/v1/integrations/sqlbot/managed-files/import", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+humanToken)
	request.Header.Set("Content-Type", "application/json")
	response, networkErr := (&http.Client{Timeout: 90 * time.Second}).Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	server.Close()
	require.Error(t, networkErr, "the first response must be lost at the network boundary")
	first := <-observed
	require.Equal(t, http.StatusOK, first.status, string(first.body))
	lostResponse := first.body
	// Restart the runtime in another OS process before replaying the request.
	require.NoError(t, runtime.Close())
	requestPath := filepath.Join(t.TempDir(), "replay-request.json")
	responsePath := filepath.Join(t.TempDir(), "replay-response.json")
	require.NoError(t, os.WriteFile(requestPath, body, 0600))
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedFileImportReplayChildProcess$")
	child.Env = append(os.Environ(), "AGENTSQL_TEST_MANAGED_REPLAY=1",
		"AGENTSQL_TEST_REPLAY_STORE="+cfg.Store.SQLitePath,
		"AGENTSQL_TEST_REPLAY_REQUEST="+requestPath,
		"AGENTSQL_TEST_REPLAY_RESPONSE="+responsePath,
		"AGENTSQL_TEST_REPLAY_API_KEY="+apiKey,
		"AGENTSQL_TEST_REPLAY_HUMAN_TOKEN="+humanToken)
	output, err := child.CombinedOutput()
	require.NoError(t, err, string(output))
	rawReply, err := os.ReadFile(responsePath)
	require.NoError(t, err)
	var childReply struct {
		Status int    `json:"status"`
		Body   []byte `json:"body"`
	}
	require.NoError(t, json.Unmarshal(rawReply, &childReply))
	runtime, err = bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	handler = newHandler()
	replay := childReply.Body
	require.Equal(t, http.StatusOK, childReply.Status, string(replay))
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
	status, _ := call(changed)
	require.Equal(t, http.StatusConflict, status, "changed valid sheet selection must not republish the same staged file")
	require.NoError(t, admin.QueryRow(ctx, `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname='agentsql_files'`).Scan(&physicalTables))
	require.Equal(t, 1, physicalTables)
	runOriginalSQLBotFileLifecycle(t, ctx, runtime, cfg, handler, base, baseToken, apiKey, data, admin)
}

// Exercise the exact parse/import/add/get/list/delete request shapes emitted
// by the pinned v1.10.2 browser, through ChatBI, the real gateway MCP/admin
// handlers, the dedicated PostgreSQL writer, and the control-plane journal.
func runOriginalSQLBotFileLifecycle(t *testing.T, ctx context.Context, runtime *bootstrap.Runtime,
	cfg config.Config, adminHandler http.Handler, base store.SQLBotBinding, baseToken, apiKey string,
	data []byte, admin *pgx.Conn,
) {
	t.Helper()
	verifier := NewSQLBotDelegationVerifier(DeriveTokenKey([]byte(adminTestSecret)), runtime.Store,
		rbac.NewService(runtime.Store.RBAC()))
	mcpHandler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(),
		mcpserver.WithAdminAPI(adminHandler), mcpserver.WithSQLBotDelegation(verifier))
	require.NoError(t, err)
	gateway := httptest.NewServer(mcpHandler)
	defer gateway.Close()
	chatPath := filepath.Join(t.TempDir(), "sqlbot-client.db")
	chatStore, err := chatbi.OpenStore(ctx, chatPath, []byte(adminTestSecret))
	require.NoError(t, err)
	installation, err := chatbi.NewInstallation("sqlbot-admin", "Synthetic-SQLBot-password-42")
	require.NoError(t, err)
	require.NoError(t, chatStore.ImportSnapshot(ctx, installation))
	require.NoError(t, chatStore.ConfigureConnections(ctx, chatbi.ConnectionConfig{Bindings: []chatbi.Binding{{
		Datasource: "10", Workspace: "1", User: "1", Name: base.DatasourceName, Dialect: "pg",
		BindingID: base.ID, ConnectorToken: baseToken, AgentKey: apiKey,
	}}}))
	require.NoError(t, chatStore.Close())
	t.Setenv("AGENTSQL_TEST_CHATBI_SECRET", adminTestSecret)
	service, err := chatbi.Assemble(ctx, chatbi.Config{StorePath: chatPath, SecretEnv: "AGENTSQL_TEST_CHATBI_SECRET",
		Gateway: gateway.URL + "/mcp", AllowGatewayHTTP: true, ManagedFileDatasource: "10"})
	require.NoError(t, err)
	defer func() { require.NoError(t, service.Store.Close()) }()
	clientHandler, err := chatbi.NewHandler(service, "v4.2.0")
	require.NoError(t, err)
	_, userToken, err := service.Identity.Login(ctx, "sqlbot-admin", "Synthetic-SQLBot-password-42")
	require.NoError(t, err)
	call := func(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+userToken)
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		response := httptest.NewRecorder()
		clientHandler.ServeHTTP(response, request)
		return response
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	file, err := form.CreateFormFile("file", "source.csv")
	require.NoError(t, err)
	_, err = file.Write(data)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	parsed := call(http.MethodPost, "/api/v1/datasource/parseExcel", upload.Bytes(), form.FormDataContentType())
	require.Equal(t, http.StatusOK, parsed.Code, parsed.Body.String())
	var parseEnvelope struct {
		Data struct {
			FilePath string             `json:"filePath"`
			Sheets   []chatbi.FileSheet `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(parsed.Body.Bytes(), &parseEnvelope))
	require.Len(t, parseEnvelope.Data.Sheets, 1)
	selection := map[string]any{"filePath": parseEnvelope.Data.FilePath,
		"sheets": []map[string]any{{"sheetName": parseEnvelope.Data.Sheets[0].Name,
			"fields": parseEnvelope.Data.Sheets[0].Fields}}}
	selectionBody, err := json.Marshal(selection)
	require.NoError(t, err)
	imported := call(http.MethodPost, "/api/v1/datasource/importToDb", selectionBody, "application/json")
	require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
	var importEnvelope struct {
		Data struct {
			Filename string `json:"filename"`
			Sheets   []struct {
				SheetName    string `json:"sheetName"`
				TableName    string `json:"tableName"`
				TableComment string `json:"tableComment"`
				Rows         int    `json:"rows"`
			} `json:"sheets"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(imported.Body.Bytes(), &importEnvelope))
	require.Equal(t, parseEnvelope.Data.FilePath, importEnvelope.Data.Filename)
	require.Len(t, importEnvelope.Data.Sheets, 1)
	require.Equal(t, 2, importEnvelope.Data.Sheets[0].Rows)
	configBody, err := json.Marshal(map[string]any{"host": "", "port": 0, "username": "", "password": "", "database": "",
		"extraJdbc": "", "dbSchema": "", "filename": importEnvelope.Data.Filename, "sheets": importEnvelope.Data.Sheets,
		"mode": "service_name", "timeout": 30, "lowVersion": false, "ssl": false, "poolSize": 5})
	require.NoError(t, err)
	configuration := encryptSQLBotClientConfig(t, configBody)
	addBody, err := json.Marshal(map[string]any{"name": "SyntheticSQLBot", "description": "synthetic source client flow",
		"type": "excel", "configuration": configuration,
		"tables": []map[string]string{{"table_name": importEnvelope.Data.Sheets[0].TableName, "table_comment": ""}}})
	require.NoError(t, err)
	added := call(http.MethodPost, "/api/v1/datasource/add", addBody, "application/json")
	require.Equal(t, http.StatusOK, added.Code, added.Body.String())
	var addEnvelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(added.Body.Bytes(), &addEnvelope))
	require.Greater(t, addEnvelope.Data.ID, int64(0))
	id := strconv.FormatInt(addEnvelope.Data.ID, 10)
	got := call(http.MethodPost, "/api/v1/datasource/get/"+id, []byte(`{}`), "application/json")
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	require.Contains(t, got.Body.String(), `"SyntheticSQLBot"`)
	listed := call(http.MethodGet, "/api/v1/datasource/list", nil, "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Contains(t, listed.Body.String(), `"SyntheticSQLBot"`)
	tables := call(http.MethodPost, "/api/v1/datasource/getTables/"+id, []byte(`{}`), "application/json")
	require.Equal(t, http.StatusOK, tables.Code, tables.Body.String())
	require.Contains(t, tables.Body.String(), importEnvelope.Data.Sheets[0].TableName)
	fields := call(http.MethodPost, "/api/v1/datasource/getFields/"+id+"/"+importEnvelope.Data.Sheets[0].TableName,
		[]byte(`{}`), "application/json")
	require.Equal(t, http.StatusOK, fields.Code, fields.Body.String())
	require.Contains(t, fields.Body.String(), `"region"`)
	previewBody, err := json.Marshal(map[string]any{"table": map[string]string{
		"id": "1", "table_name": importEnvelope.Data.Sheets[0].TableName}})
	require.NoError(t, err)
	preview := call(http.MethodPost, "/api/v1/datasource/previewData/"+id, previewBody, "application/json")
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `"east"`)
	deleted := call(http.MethodPost, "/api/v1/datasource/delete/"+id+"/SyntheticSQLBot", []byte(`{}`), "application/json")
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	var remaining int
	require.NoError(t, admin.QueryRow(ctx, `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname='agentsql_files'`).Scan(&remaining))
	require.Equal(t, 1, remaining, "deleting the SQLBot datasource must drop only its own physical table")
}

func encryptSQLBotClientConfig(t *testing.T, plain []byte) string {
	t.Helper()
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	require.NoError(t, err)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(plain))
	for offset := 0; offset < len(plain); offset += aes.BlockSize {
		block.Encrypt(encrypted[offset:offset+aes.BlockSize], plain[offset:offset+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(encrypted)
}

func TestManagedFileImportReplayChildProcess(t *testing.T) {
	if os.Getenv("AGENTSQL_TEST_MANAGED_REPLAY") != "1" {
		t.Skip("only launched by the managed-file process-restart test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg := adminTestConfig(os.Getenv("AGENTSQL_TEST_REPLAY_STORE"))
	runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close()) }()
	reader, err := runtime.Store.Datasources().Get(ctx, "managed-http-read")
	require.NoError(t, err)
	writer := reader
	writer.ID, writer.Username = "managed-http-writer", "file_writer"
	physical, err := executor.NewManagedFileProvisioner(reader, writer, "writer-password")
	require.NoError(t, err)
	handler, err := NewHandler(Deps{Runtime: runtime, ManagedFiles: physical, Config: cfg,
		AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret))}, zerolog.Nop())
	require.NoError(t, err)
	body, err := os.ReadFile(os.Getenv("AGENTSQL_TEST_REPLAY_REQUEST"))
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/sqlbot/managed-files/import", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+os.Getenv("AGENTSQL_TEST_REPLAY_API_KEY"))
	request.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+os.Getenv("AGENTSQL_TEST_REPLAY_HUMAN_TOKEN"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	reply, err := json.Marshal(struct {
		Status int    `json:"status"`
		Body   []byte `json:"body"`
	}{Status: response.Code, Body: response.Body.Bytes()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("AGENTSQL_TEST_REPLAY_RESPONSE"), reply, 0600))
}
