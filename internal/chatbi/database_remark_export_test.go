package chatbi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseRemarkWorkbookHasBoundedMultiTableLayout(t *testing.T) {
	sheets := []managedRemarkSheet{{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}}}}
	for i := 0; i < 256; i++ {
		name := "Sheet" + strconv.Itoa(i)
		sheets[0].rows = append(sheets[0].rows, []string{name, "table" + strconv.Itoa(i), ""})
		sheets = append(sheets, managedRemarkSheet{name: name,
			rows: [][]string{{"字段名", "字段备注"}, {"id", ""}}})
	}
	data, err := managedRemarkWorkbook(sheets)
	require.NoError(t, err)
	book, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	require.NoError(t, err)
	require.Len(t, book.File, 261)
	parsed, err := parseXLSXSheetsLimited(data, 257)
	require.NoError(t, err)
	require.Len(t, parsed, 257)
	sheets = append(sheets, managedRemarkSheet{name: "Sheet256",
		rows: [][]string{{"字段名", "字段备注"}, {"id", ""}}})
	_, err = managedRemarkWorkbook(sheets)
	require.Error(t, err)
}

func TestOriginalDatabaseRemarkExportUsesCurrentSchemaAndAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	binding, revision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	binding.Configuration = "server-owned-database-credential"
	binding.TableComments = `{"public\u0000sales":"Sales & revenue","public\u0000hidden":"Hidden remark"}`
	binding.FieldComments = `{"public\u0000sales\u0000amount":"Amount field"}`
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/exportDsSchema/10", nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("").Code)
	response := request(token)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		response.Header().Get("Content-Type"))
	require.NotContains(t, response.Body.String(), binding.Configuration)
	require.NotContains(t, response.Body.String(), binding.ConnectorToken)
	book, err := zip.NewReader(strings.NewReader(response.Body.String()), int64(response.Body.Len()))
	require.NoError(t, err)
	sheets := map[string]string{}
	for _, file := range book.File {
		if !strings.HasPrefix(file.Name, "xl/worksheets/sheet") {
			continue
		}
		reader, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		sheets[file.Name] = string(body)
	}
	require.Len(t, sheets, 2)
	require.Contains(t, sheets["xl/worksheets/sheet1.xml"], "Sales &amp; revenue")
	require.NotContains(t, sheets["xl/worksheets/sheet1.xml"], "Hidden remark")
	require.Contains(t, sheets["xl/worksheets/sheet2.xml"], "Amount field")

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_export_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(token).Code)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_export_audit`)
	require.NoError(t, err)
	gateway.columns = []Column{{Schema: "public", Table: "other", Name: "name", DataType: "text"}}
	response = request(token)
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "Sales & revenue")
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		var event struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "datasource.database-remark.export" {
			found = true
		}
		require.NotContains(t, string(raw), "Sales & revenue")
	}
	require.True(t, found)
}
