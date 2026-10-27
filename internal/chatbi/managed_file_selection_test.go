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

func TestOriginalFileDatasourceChooseTablesChangesQueryScopeWithAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	const east = "agentsql_file_11111111111111111111111111111111"
	const west = "agentsql_file_22222222222222222222222222222222"
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: east, Name: "region", DataType: "text"},
		{Schema: "agentsql_files", Table: west, Name: "region", DataType: "text"},
	}}
	configuration, err := encodeSQLBotExcelConfig("agentsql-file-123.xlsx", []importedFileSheet{
		{SheetName: "East", TableName: east, Rows: 1}, {SheetName: "West", TableName: west, Rows: 1},
	})
	require.NoError(t, err)
	binding := Binding{Datasource: "11", Workspace: principal.Workspace, User: principal.User.ID,
		Name: "Two sheets", ResolveName: "agentsql-import-33333333333333333333333333333333",
		Type: "excel", Configuration: configuration, ManagedFileID: "22222222222222222222222222222222",
		Dialect: "pg", BindingID: "22222222-2222-4222-8222-222222222222",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AgentKey: "fixture-agent"}
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, binding.Datasource), binding, 0))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(path, body, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	choices := func(credential string, eastChecked, westChecked bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agentsql/datasource/fileTables/11", nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		var response struct {
			Data []struct {
				TableName string `json:"table_name"`
				Checked   bool   `json:"checked"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Data, 2)
		require.Equal(t, east, response.Data[0].TableName)
		require.Equal(t, eastChecked, response.Data[0].Checked)
		require.Equal(t, west, response.Data[1].TableName)
		require.Equal(t, westChecked, response.Data[1].Checked)
		require.NotContains(t, w.Body.String(), binding.ConnectorToken)
		require.NotContains(t, w.Body.String(), binding.Configuration)
	}
	choose := "/api/v1/datasource/chooseTables/11"
	assertSelected := func(want string) Binding {
		t.Helper()
		current, err := service.binding(ctx, principal, binding.Datasource)
		require.NoError(t, err)
		require.Equal(t, want, current.SelectedTables)
		return current
	}
	require.Equal(t, http.StatusUnprocessableEntity,
		request(choose, `[{"table_name":"unknown"}]`, token).Code)
	assertSelected("")
	choices(token, true, true)
	require.Equal(t, http.StatusUnprocessableEntity,
		request(choose, `[{"table_name":"`+west+`","table_comment":"ignored"}]`, token).Code)
	other := testUser(t, "2", "other-admin")
	other.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, other))
	_, otherToken, err := service.Identity.Login(ctx, other.Account, "Synthetic-password-1")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound,
		request(choose, `[{"table_name":"`+west+`"}]`, otherToken).Code)
	foreignRead := httptest.NewRequest(http.MethodGet, "/api/v1/agentsql/datasource/fileTables/11", nil)
	foreignRead.Header.Set("X-SQLBOT-TOKEN", "Bearer "+otherToken)
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreignRead)
	require.Equal(t, http.StatusNotFound, foreignResponse.Code)

	require.Equal(t, http.StatusOK,
		request(choose, `[{"table_name":"`+west+`"}]`, token).Code)
	current := assertSelected(west)
	choices(token, false, true)
	_, err = service.query(ctx, principal, current, "SELECT region FROM agentsql_files."+east)
	require.ErrorIs(t, err, ErrGateway)
	tables := request("/api/v1/datasource/tableList/11", "", token)
	require.Equal(t, http.StatusOK, tables.Code)
	require.Contains(t, tables.Body.String(), west)
	require.NotContains(t, tables.Body.String(), east)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_selection_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(choose, `[]`, token).Code)
	assertSelected(west)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_selection_audit`)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, request(choose, `[]`, token).Code)
	current = assertSelected("!")
	choices(token, false, false)
	columns, err := service.schema(ctx, principal, current)
	require.NoError(t, err)
	require.Empty(t, columns)
	_, err = service.query(ctx, principal, current, "SELECT region FROM agentsql_files."+west)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, http.StatusOK,
		request(choose, `[{"table_name":"`+west+`"},{"table_name":"`+east+`"}]`, token).Code)
	current = assertSelected("")
	choices(token, true, true)
	columns, err = service.schema(ctx, principal, current)
	require.NoError(t, err)
	require.Len(t, columns, 2)

	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	counts := []int{}
	for _, raw := range audits {
		var event struct {
			Action   string `json:"action"`
			Selected int    `json:"selected"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "file-import.tables-select" {
			counts = append(counts, event.Selected)
		}
	}
	require.ElementsMatch(t, []int{1, 0, 2}, counts)
}
