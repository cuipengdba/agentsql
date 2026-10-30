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

func TestOriginalDatabaseRemarkImportKeepsNumericExcelFieldName(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "123", DataType: "text"},
	}}
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	book, err := managedRemarkWorkbook([]managedRemarkSheet{
		{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}, {"Sheet0", "sales", ""}}},
		{name: "Sheet0", rows: [][]string{{"字段名", "字段备注"}, {"123", "Numeric field"}}},
	})
	require.NoError(t, err)

	// Excel stores this field-name cell as a number. The import must retain the
	// textual identifier rather than dropping or converting it to a float.
	archive, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	require.NoError(t, err)
	var encoded bytes.Buffer
	writer := zip.NewWriter(&encoded)
	for _, file := range archive.File {
		part, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(part)
		require.NoError(t, err)
		require.NoError(t, part.Close())
		if file.Name == "xl/worksheets/sheet2.xml" {
			const old = `<c r="A2" t="inlineStr"><is><t xml:space="preserve">123</t></is></c>`
			require.Equal(t, 1, strings.Count(string(body), old))
			body = []byte(strings.Replace(string(body), old, `<c r="A2" t="n"><v>123</v></c>`, 1))
		}
		out, err := writer.Create(file.Name)
		require.NoError(t, err)
		_, err = out.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	var requestBody bytes.Buffer
	form := multipart.NewWriter(&requestBody)
	file, err := form.CreateFormFile("file", "remarks.xlsx")
	require.NoError(t, err)
	_, err = file.Write(encoded.Bytes())
	require.NoError(t, err)
	require.NoError(t, form.Close())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/uploadDsSchema/10", &requestBody)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	fields, err := databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "Numeric field", fields[databaseFieldCommentKey("public", "sales", "123")])
}

func TestOriginalDatabaseRemarkImportAppliesOnlyVisibleRowsWithAudit(t *testing.T) {
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
	binding.TableComments = `{"public\u0000hidden":"preserve hidden"}`
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	upload := func(tableRemark, fieldRemark, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		book, err := managedRemarkWorkbook([]managedRemarkSheet{
			{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"},
				{"Sheet0", "sales", tableRemark}, {"Sheet1", "hidden", "forged"}}},
			{name: "Sheet0", rows: [][]string{{"字段名", "字段备注"},
				{"amount", fieldRemark}, {"secret", "forged field"}}},
			{name: "Sheet1", rows: [][]string{{"字段名", "字段备注"}, {"secret", "forged field"}}},
		})
		require.NoError(t, err)
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		file, err := writer.CreateFormFile("file", "remarks.xlsx")
		require.NoError(t, err)
		_, err = file.Write(book)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/uploadDsSchema/10", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, upload("private", "private", "").Code)
	require.Equal(t, http.StatusOK, upload("Sales report", "Sales amount", token).Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tables, err := databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "Sales report", tables[databaseTableCommentKey("public", "sales")])
	require.Equal(t, "preserve hidden", tables[databaseTableCommentKey("public", "hidden")])
	fields, err := databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "Sales amount", fields[databaseFieldCommentKey("public", "sales", "amount")])
	require.NotContains(t, current.FieldComments, "forged field")
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_bulk_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, upload("rollback", "rollback", token).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tables, err = databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "Sales report", tables[databaseTableCommentKey("public", "sales")])
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_bulk_audit`)
	require.NoError(t, err)
	gateway.columns = []Column{{Schema: "public", Table: "other", Name: "name", DataType: "text"}}
	require.Equal(t, http.StatusOK, upload("revoked", "revoked", token).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tables, err = databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "Sales report", tables[databaseTableCommentKey("public", "sales")])
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		var event struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "database.remarks-bulk" {
			found = true
		}
		require.NotContains(t, string(raw), "Sales amount")
	}
	require.True(t, found)
}
