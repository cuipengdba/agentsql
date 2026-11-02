package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalManagedFileTableRemarkPersistsWithAuditAndRejectsUnsafeEdits(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	const table = "agentsql_file_11111111111111111111111111111111"
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
	}}
	configuration, err := encodeSQLBotExcelConfig("agentsql-file-123.csv", []importedFileSheet{
		{SheetName: "Sheet1", TableName: table, Rows: 4},
	})
	require.NoError(t, err)
	binding := Binding{Datasource: "11", Workspace: principal.Workspace, User: principal.User.ID,
		Name: "Forecast", ResolveName: "agentsql-import-33333333333333333333333333333333",
		Type: "excel", Configuration: configuration, ManagedFileID: "22222222222222222222222222222222",
		Dialect: "pg", BindingID: "22222222-2222-4222-8222-222222222222",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AgentKey: "fixture-agent"}
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, binding.Datasource), binding, 0))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	id := strconv.FormatInt(browserTableID("11", "agentsql_files", table), 10)
	edit := func(comment string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": id, "ds_id": "11", "table_name": table,
			"table_comment": "", "custom_comment": comment, "checked": true, "embedding": nil})
		require.NoError(t, err)
		return string(body)
	}
	path := "/api/v1/datasource/editTable"
	require.Equal(t, http.StatusUnauthorized, request(path, edit("private"), "").Code)
	require.NotEqual(t, http.StatusOK, request(path, strings.Replace(edit("private"), `"id":"`+id+`"`, `"id":"1"`, 1), token).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(path, strings.Replace(edit("private"), `"checked":true`, `"checked":false`, 1), token).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(path, edit(strings.Repeat("x", 501)), token).Code)

	const comment = "月份金额测试表"
	require.Equal(t, http.StatusOK, request(path, edit(comment), token).Code)
	current, err := service.binding(ctx, principal, "11")
	require.NoError(t, err)
	comments, err := managedFileTableComments(current)
	require.NoError(t, err)
	require.Equal(t, comment, comments[table])
	list := request("/api/v1/datasource/tableList/11", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), comment)
	require.NotContains(t, list.Body.String(), binding.ConnectorToken)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_table_remark_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(path, edit("should roll back"), token).Code)
	current, err = service.binding(ctx, principal, "11")
	require.NoError(t, err)
	comments, err = managedFileTableComments(current)
	require.NoError(t, err)
	require.Equal(t, comment, comments[table])
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_table_remark_audit`)
	require.NoError(t, err)

	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		var event struct {
			Action        string `json:"action"`
			Table         string `json:"table"`
			CommentLength int    `json:"comment_length"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "file-import.table-comment" {
			found = true
			require.Equal(t, table, event.Table)
			require.Equal(t, len([]rune(comment)), event.CommentLength)
			require.NotContains(t, string(raw), comment)
		}
	}
	require.True(t, found)
}
