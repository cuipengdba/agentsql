package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalFileDatasourceMetadataUpdateIsAuditedAndCannotReplaceFile(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	const table = "agentsql_file_11111111111111111111111111111111"
	configuration, err := encodeSQLBotExcelConfig("file-one.xlsx", []importedFileSheet{{SheetName: "Sheet1", TableName: table, Rows: 1}})
	require.NoError(t, err)
	binding := Binding{Datasource: "11", Workspace: principal.Workspace, User: principal.User.ID,
		Name: "Old file", Description: "Old description", ResolveName: "agentsql-import-33333333333333333333333333333333",
		Type: "excel", Configuration: configuration, ManagedFileID: "22222222222222222222222222222222",
		Dialect: "pg", BindingID: "22222222-2222-4222-8222-222222222222",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AgentKey: "fixture-agent"}
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, binding.Datasource), binding, 0))
	other := binding
	other.Datasource, other.Name = "12", "Other file"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, other.Datasource), other, 0))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(credential, name, description, config string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": 11, "name": name, "description": description,
			"type": "excel", "configuration": config})
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/update", strings.NewReader(string(body)))
		if credential != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertName := func(want string) {
		t.Helper()
		current, err := service.binding(ctx, principal, binding.Datasource)
		require.NoError(t, err)
		require.Equal(t, want, current.Name)
		require.Equal(t, binding.Configuration, current.Configuration)
		require.Equal(t, binding.BindingID, current.BindingID)
	}
	require.Equal(t, http.StatusUnauthorized, request("", "Renamed", "Changed", configuration).Code)
	foreignAdmin := testUser(t, "2", "foreign-admin")
	foreignAdmin.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, foreignAdmin))
	_, foreignToken, err := service.Identity.Login(ctx, foreignAdmin.Account, "Synthetic-password-1")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, request(foreignToken, "Renamed", "Changed", configuration).Code)
	require.Equal(t, http.StatusConflict, request(token, other.Name, "Changed", configuration).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(token, "Renamed", "Changed", "tampered").Code)
	replaced, err := encodeSQLBotExcelConfig("other-file.xlsx", []importedFileSheet{{SheetName: "Sheet1", TableName: table, Rows: 1}})
	require.NoError(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, request(token, "Renamed", "Changed", replaced).Code)
	assertName(binding.Name)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_metadata_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit unavailable'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(token, "Renamed", "Changed", configuration).Code)
	assertName(binding.Name)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_metadata_audit`)
	require.NoError(t, err)

	response := request(token, "Renamed", "Changed", configuration)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"name":"Renamed"`)
	require.NotContains(t, response.Body.String(), binding.ConnectorToken)
	assertName("Renamed")
	current, err := service.binding(ctx, principal, binding.Datasource)
	require.NoError(t, err)
	require.Equal(t, "Changed", current.Description)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	count := 0
	for _, raw := range audits {
		var entry struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &entry))
		if entry.Action == "file-import.datasource-update" {
			count++
		}
	}
	require.Equal(t, 1, count)
}
