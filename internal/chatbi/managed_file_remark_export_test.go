package chatbi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalManagedFileRemarkExportIsScopedAuditedWorkbook(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	const table = "agentsql_file_11111111111111111111111111111111"
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
		{Schema: "agentsql_files", Table: table, Name: "amount", DataType: "numeric"},
	}}
	configuration, err := encodeSQLBotExcelConfig("agentsql-file-123.csv", []importedFileSheet{
		{SheetName: "Sheet1", TableName: table, Rows: 4},
	})
	require.NoError(t, err)
	tableComments, err := json.Marshal(map[string]string{table: "月份&金额表"})
	require.NoError(t, err)
	fieldComments, err := json.Marshal(map[string]string{managedFileFieldCommentKey(table, "month"): "月份字段"})
	require.NoError(t, err)
	binding := Binding{Datasource: "11", Workspace: principal.Workspace, User: principal.User.ID,
		Name: "Forecast", ResolveName: "agentsql-import-33333333333333333333333333333333",
		Type: "excel", Configuration: configuration, ManagedFileID: "22222222222222222222222222222222",
		Dialect: "pg", BindingID: "22222222-2222-4222-8222-222222222222",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AgentKey: "fixture-agent",
		TableComments: string(tableComments), FieldComments: string(fieldComments)}
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, binding.Datasource), binding, 0))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(id, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/exportDsSchema/"+id, nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("11", "").Code)
	require.NotEqual(t, http.StatusOK, request("11", token).Code, "ordinary members cannot export")
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request("12", token).Code)
	result := request("11", token)
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", result.Header().Get("Content-Type"))
	require.NotContains(t, result.Body.String(), binding.ConnectorToken)
	book, err := zip.NewReader(strings.NewReader(result.Body.String()), int64(result.Body.Len()))
	require.NoError(t, err)
	sheets := make(map[string]string)
	for _, entry := range book.File {
		if !strings.HasPrefix(entry.Name, "xl/worksheets/sheet") {
			continue
		}
		reader, err := entry.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		sheets[entry.Name] = string(body)
	}
	require.Len(t, sheets, 2)
	require.Contains(t, sheets["xl/worksheets/sheet1.xml"], table)
	require.Contains(t, sheets["xl/worksheets/sheet1.xml"], "月份&amp;金额表")
	require.Contains(t, sheets["xl/worksheets/sheet2.xml"], "月份字段")
	require.NotContains(t, sheets["xl/worksheets/sheet2.xml"], binding.AgentKey)
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
		{Schema: "agentsql_files", Table: "agentsql_file_99999999999999999999999999999999", Name: "secret", DataType: "text"},
	}}
	require.NotEqual(t, http.StatusOK, request("11", token).Code, "another imported relation must not be exported")
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
		{Schema: "agentsql_files", Table: table, Name: "amount", DataType: "numeric"},
	}}

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_remark_export_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request("11", token).Code, "audit failure must close the export")
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_remark_export_audit`)
	require.NoError(t, err)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		if strings.Contains(string(raw), `"datasource.remark.export"`) {
			found = true
			require.NotContains(t, string(raw), "月份&金额表")
		}
	}
	require.True(t, found)

	makeUpload := func(tableName, tableRemark, fieldRemark string) string {
		t.Helper()
		book, err := managedRemarkWorkbook([]managedRemarkSheet{
			{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}, {"Sheet0", tableName, tableRemark}}},
			{name: "Sheet0", rows: [][]string{{"字段名", "字段备注"}, {"month", fieldRemark}, {"amount", ""}}},
		})
		require.NoError(t, err)
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "remarks.xlsx")
		require.NoError(t, err)
		_, err = part.Write(book)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return writer.FormDataContentType() + "\x00" + body.String()
	}
	upload := func(payload string) *httptest.ResponseRecorder {
		t.Helper()
		parts := strings.SplitN(payload, "\x00", 2)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/uploadDsSchema/11", strings.NewReader(parts[1]))
		r.Header.Set("Content-Type", parts[0])
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusOK, upload(makeUpload("agentsql_file_99999999999999999999999999999999", "forged", "forged")).Code)
	unchanged, err := service.binding(ctx, principal, "11")
	require.NoError(t, err)
	require.Equal(t, binding.TableComments, unchanged.TableComments)
	require.Equal(t, binding.FieldComments, unchanged.FieldComments)
	require.Equal(t, http.StatusOK, upload(makeUpload(table, "新的表备注", "新的字段备注")).Code)
	current, err := service.binding(ctx, principal, "11")
	require.NoError(t, err)
	tableMap, err := managedFileTableComments(current)
	require.NoError(t, err)
	fieldMap, err := managedFileFieldComments(current)
	require.NoError(t, err)
	require.Equal(t, "新的表备注", tableMap[table])
	require.Equal(t, "新的字段备注", fieldMap[managedFileFieldCommentKey(table, "month")])
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_remark_import_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, upload(makeUpload(table, "must roll back", "must roll back")).Code)
	current, err = service.binding(ctx, principal, "11")
	require.NoError(t, err)
	tableMap, err = managedFileTableComments(current)
	require.NoError(t, err)
	fieldMap, err = managedFileFieldComments(current)
	require.NoError(t, err)
	require.Equal(t, "新的表备注", tableMap[table])
	require.Equal(t, "新的字段备注", fieldMap[managedFileFieldCommentKey(table, "month")])
}
