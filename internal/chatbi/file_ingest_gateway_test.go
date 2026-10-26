package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedFileGatewayUsesSeparateAgentAndHumanCredentials(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/integrations/sqlbot/resolve":
			if request.Header.Get("Authorization") != "Bearer connector-secret" {
				http.Error(writer, "denied", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{
				"binding_id": "binding-1", "revision": 1, "agent_id": "agent-1", "datasource_id": "read-1",
				"schema": "agentsql_files", "dialect": "pg", "user_token": "SQB1.fixture",
				"expires_at": time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
			}})
		case "/api/v1/integrations/sqlbot/managed-files/import":
			requests++
			if request.Header.Get("Authorization") != "Bearer agent-secret" ||
				request.Header.Get("X-AgentSQL-User-Authorization") != "Bearer SQB1.fixture" {
				http.Error(writer, "denied", http.StatusUnauthorized)
				return
			}
			var payload struct {
				BindingID string            `json:"binding_id"`
				Filename  string            `json:"filename"`
				Data      []byte            `json:"file_data"`
				Selection FileImportRequest `json:"selection"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil || payload.BindingID != "binding-1" ||
				payload.Filename != "source.csv" || string(payload.Data) != "amount\n10\n" || payload.Selection.FilePath != "agentsql-file-1.csv" {
				http.Error(writer, "invalid", http.StatusUnprocessableEntity)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{
				"operation_id":    "11111111111111111111111111111111",
				"binding_id":      "11111111-1111-4111-8111-111111111111",
				"datasource_name": "agentsql-import-33333333333333333333333333333333",
				"connector_token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"sheets":          []map[string]any{{"sheetName": "Sheet1", "tableName": "agentsql_file_22222222222222222222222222222222", "tableComment": "", "rows": 1}},
			}})
		case "/api/v1/integrations/sqlbot/managed-files/delete":
			if request.Header.Get("Authorization") != "Bearer agent-secret" ||
				request.Header.Get("X-AgentSQL-User-Authorization") != "Bearer SQB1.fixture" {
				http.Error(writer, "denied", http.StatusUnauthorized)
				return
			}
			var payload struct {
				BindingID   string `json:"binding_id"`
				OperationID string `json:"operation_id"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil || payload.BindingID != "binding-1" ||
				payload.OperationID != "11111111111111111111111111111111" {
				http.Error(writer, "invalid", http.StatusUnprocessableEntity)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{
				"operation_id": payload.OperationID, "status": "deleted",
			}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	stage := fileStage{FilePath: "agentsql-file-1.csv", Original: "source.csv", Data: []byte("amount\n10\n"), Expires: time.Now().UTC().Add(time.Hour)}
	var selection FileImportRequest
	require.NoError(t, json.Unmarshal([]byte(`{"filePath":"agentsql-file-1.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"amount","fieldType":"int"}]}]}`), &selection))
	plan, err := planFileImport(stage, selection, time.Now().UTC())
	require.NoError(t, err)
	gateway := &ControlledGateway{Endpoint: server.URL + "/mcp", AllowHTTP: true}
	p := Principal{User: User{ID: "7"}, Workspace: "1"}
	b := Binding{Datasource: "10", Workspace: "1", User: "7", Name: "Managed", Dialect: "pg",
		BindingID: "binding-1", ConnectorToken: "connector-secret", AgentKey: "agent-secret"}
	result, err := gateway.ImportFile(context.Background(), p, b, stage, selection, plan)
	require.NoError(t, err)
	require.Equal(t, 1, requests)
	require.Equal(t, "11111111111111111111111111111111", result.OperationID)
	require.Equal(t, "agentsql_file_22222222222222222222222222222222", result.Sheets[0].TableName)
	require.NoError(t, gateway.DeleteFile(context.Background(), p, b, result.OperationID))
	require.ErrorIs(t, gateway.DeleteFile(context.Background(), p, b, "../forged"), ErrGateway)
	stage.FilePath = "../forged.csv"
	_, err = gateway.ImportFile(context.Background(), p, b, stage, selection, plan)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 1, requests)
}
