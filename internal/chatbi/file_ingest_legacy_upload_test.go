package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalUploadExcelUsesManagedImportAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	service.ManagedFileDatasource = "10"
	managed := &fakeManagedFileGateway{fakeGateway: fake}
	service.Gateway = managed
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	upload := func(name, contents, bearer string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, createErr := writer.CreateFormFile("file", name)
		require.NoError(t, createErr)
		_, createErr = part.Write([]byte(contents))
		require.NoError(t, createErr)
		require.NoError(t, writer.Close())
		request := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/uploadExcel", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		if bearer != "" {
			request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	const csv = "region,amount\neast,10\nwest,20\n"
	require.Equal(t, http.StatusUnauthorized, upload("source.csv", csv, "").Code)
	require.Equal(t, http.StatusUnauthorized, upload("source.csv", csv, token).Code,
		"non-admin user cannot publish a managed datasource")
	require.Zero(t, managed.calls)

	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnprocessableEntity, upload("source.xls", csv, token).Code,
		"the bounded importer does not accept legacy XLS bytes")
	require.Zero(t, managed.calls)
	response := upload("source.csv", csv, token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result struct {
		Data struct {
			Filename string              `json:"filename"`
			Sheets   []importedFileSheet `json:"sheets"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Data.Sheets, 1)
	require.Equal(t, "agentsql_file_11111111111111111111111111111111", result.Data.Sheets[0].TableName)
	require.Equal(t, 1, managed.calls)
	var receipt managedFileReceipt
	_, err = service.Store.get(ctx, managedFileReceiptKey(principal, result.Data.Filename), &receipt)
	require.NoError(t, err)
	require.Equal(t, result.Data.Sheets, receipt.Sheets)
	configuration, err := encodeSQLBotExcelConfig(receipt.Filename, receipt.Sheets)
	require.NoError(t, err)
	addBody, err := json.Marshal(map[string]any{
		"name": "Legacy sales file", "type": "excel", "configuration": configuration,
		"tables": []map[string]string{{"table_name": receipt.Sheets[0].TableName, "table_comment": ""}},
	})
	require.NoError(t, err)
	addRequest := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/add", bytes.NewReader(addBody))
	addRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	added := httptest.NewRecorder()
	handler.ServeHTTP(added, addRequest)
	require.Equal(t, http.StatusOK, added.Code, added.Body.String())
	require.Equal(t, 1, managed.calls, "claim must not reimport the file")

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_legacy_upload_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, upload("source.csv", csv, token).Code)
	require.Equal(t, 1, managed.calls, "audit failure must prevent a second gateway import")
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_legacy_upload_audit`)
	require.NoError(t, err)

	require.NotEqual(t, http.StatusOK, upload("different.csv", "different,values\none,1\n", token).Code)
	require.Equal(t, 2, managed.calls, "gateway rejection cannot return a published sheet")
}
