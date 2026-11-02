package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseRemarkWorkbookOriginalRoutesAreScopedAndAudited(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "regions", Name: "label", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
	}}
	binding, bindingRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	initialTable, _ := json.Marshal(map[string]string{databaseTableRemarkKey("public", "regions"): "Region & label"})
	initialField, _ := json.Marshal(map[string]string{databaseFieldRemarkKey("public", "regions", "label"): "English label"})
	binding.TableComments, binding.FieldComments = string(initialTable), string(initialField)
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, bindingRevision))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(method, path, contentType string, body []byte, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	export := func() *httptest.ResponseRecorder {
		return request(http.MethodGet, "/api/v1/datasource/exportDsSchema/10", "", nil, token)
	}
	require.Equal(t, http.StatusUnauthorized,
		request(http.MethodGet, "/api/v1/datasource/exportDsSchema/10", "", nil, "").Code)
	require.NotEqual(t, http.StatusOK,
		request(http.MethodGet, "/api/v1/datasource/exportDsSchema/11", "", nil, token).Code)
	result := export()
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", result.Header().Get("Content-Type"))
	require.NotContains(t, result.Body.String(), binding.ConnectorToken)
	require.NotContains(t, result.Body.String(), binding.AgentKey)
	sheets, err := parseXLSXSheetsLimited(result.Body.Bytes(), 257)
	require.NoError(t, err)
	require.Len(t, sheets, 3)
	require.Equal(t, "数据表列表", sheets[0].Name)
	require.Contains(t, fmt.Sprint(sheets[0].Rows), "Region & label")
	require.Contains(t, fmt.Sprint(sheets[1].Rows), "English label")

	makeUpload := func(regionTable, regionField string) (string, []byte) {
		t.Helper()
		workbook, err := managedRemarkWorkbook([]managedRemarkSheet{
			{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"},
				{"Sheet0", "regions", regionTable}, {"Sheet1", "sales", "sales"}, {"Sheet2", "other", "forged"}}},
			{name: "Sheet0", rows: [][]string{{"字段名", "字段备注"}, {"label", regionField}, {"ghost", "forged"}}},
			{name: "Sheet1", rows: [][]string{{"字段名", "字段备注"}, {"amount", "amount note"}}},
			{name: "Sheet2", rows: [][]string{{"字段名", "字段备注"}, {"secret", "forged"}}},
		})
		require.NoError(t, err)
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "remarks.xlsx")
		require.NoError(t, err)
		_, err = part.Write(workbook)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return writer.FormDataContentType(), body.Bytes()
	}
	upload := func(table, field string) *httptest.ResponseRecorder {
		t.Helper()
		contentType, body := makeUpload(table, field)
		return request(http.MethodPost, "/api/v1/datasource/uploadDsSchema/10", contentType, body, token)
	}
	require.Equal(t, http.StatusOK, upload("New table remark", "New field remark").Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tableComments, err := databaseRemarks(current, false)
	require.NoError(t, err)
	fieldComments, err := databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "New table remark", tableComments[databaseTableRemarkKey("public", "regions")])
	require.Equal(t, "New field remark", fieldComments[databaseFieldRemarkKey("public", "regions", "label")])
	require.Equal(t, "amount note", fieldComments[databaseFieldRemarkKey("public", "sales", "amount")])
	require.NotContains(t, current.TableComments, "forged")
	require.NotContains(t, current.FieldComments, "forged")
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_workbook_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, export().Code)
	require.NotEqual(t, http.StatusOK, upload("rollback", "rollback").Code)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_workbook_audit`)
	require.NoError(t, err)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tableComments, err = databaseRemarks(current, false)
	require.NoError(t, err)
	fieldComments, err = databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "New table remark", tableComments[databaseTableRemarkKey("public", "regions")])
	require.Equal(t, "New field remark", fieldComments[databaseFieldRemarkKey("public", "regions", "label")])

	selection := []byte(`[{"table_name":"sales","table_comment":""}]`)
	require.Equal(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/chooseTables/10", "application/json", selection, token).Code)
	result = export()
	require.Equal(t, http.StatusOK, result.Code)
	sheets, err = parseXLSXSheetsLimited(result.Body.Bytes(), 257)
	require.NoError(t, err)
	require.Len(t, sheets, 2)
	require.NotContains(t, fmt.Sprint(sheets), "regions")
	require.Equal(t, http.StatusOK, upload("hidden table edit", "hidden field edit").Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tableComments, err = databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "New table remark", tableComments[databaseTableRemarkKey("public", "regions")])
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		if strings.Contains(string(raw), `"database.remarks-bulk"`) {
			found = true
			require.NotContains(t, string(raw), "New table remark")
			require.NotContains(t, string(raw), "New field remark")
		}
	}
	require.True(t, found)
}

func TestDatabaseRemarkWorkbookSupportsMoreThanLegacyEightTables(t *testing.T) {
	sheets := []managedRemarkSheet{{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}}}}
	for index := 0; index < 125; index++ {
		name := fmt.Sprintf("Sheet%d", index)
		sheets[0].rows = append(sheets[0].rows, []string{name, fmt.Sprintf("table_%d", index), ""})
		sheets = append(sheets, managedRemarkSheet{name: name,
			rows: [][]string{{"字段名", "字段备注"}, {"id", ""}}})
	}
	workbook, err := managedRemarkWorkbook(sheets)
	require.NoError(t, err)
	parsed, err := parseXLSXSheetsLimited(workbook, 257)
	require.NoError(t, err)
	require.Len(t, parsed, len(sheets))
}
