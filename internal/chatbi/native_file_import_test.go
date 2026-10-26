package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeFileImportClaimsOnlyCurrentUnclaimedReceipt(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	service.ManagedFileDatasource = "10"
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake}
	stage := fileStage{FilePath: "agentsql-file-native.csv", Original: "source.csv",
		Data: []byte("region,amount\neast,10\nwest,20\n"), Expires: time.Now().UTC().Add(time.Hour)}
	sheets, err := parseFileImport(stage.Original, stage.Data)
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	call := func(path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	claimPath := "/api/v1/agentsql/datasource/claim-import"
	claimBody := `{"filePath":"agentsql-file-native.csv","name":"Native CSV","description":"synthetic"}`
	require.Equal(t, http.StatusUnauthorized, call(claimPath, claimBody, "").Code)
	require.NotEqual(t, http.StatusOK, call(claimPath, claimBody, token).Code,
		"a staged file is not a published import receipt")
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	require.NoError(t, service.saveFileStage(ctx, principal, stage, sheets))
	importBody := `{"filePath":"agentsql-file-native.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"region","fieldType":"string"},{"fieldName":"amount","fieldType":"int"}]}]}`
	imported := call("/api/v1/datasource/importToDb", importBody, token)
	require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = false
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	require.NotEqual(t, http.StatusOK, call(claimPath, claimBody, token).Code,
		"losing administrator status must block an unclaimed published import")
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	require.NotEqual(t, http.StatusOK, call(claimPath, `{"filePath":"../agentsql-file-native.csv","name":"Forged"}`, token).Code)
	require.NotEqual(t, http.StatusOK, call(claimPath, `{"filePath":"agentsql-file-other.csv","name":"Forged"}`, token).Code)
	created := call(claimPath, claimBody, token)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Contains(t, created.Body.String(), `"name":"Native CSV"`)
	require.NotEqual(t, http.StatusOK, call(claimPath, claimBody, token).Code,
		"the native route must share the one-use receipt with SQLBot compatibility")
	var receipt managedFileReceipt
	_, err = service.Store.get(ctx, managedFileReceiptKey(principal, stage.FilePath), &receipt)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.ClaimedDatasource)
	encoded, err := encodeSQLBotExcelConfig(receipt.Filename, receipt.Sheets)
	require.NoError(t, err)
	filename, decoded, err := decodeSQLBotExcelConfig(encoded)
	require.NoError(t, err)
	require.Equal(t, receipt.Filename, filename)
	require.Equal(t, receipt.Sheets, decoded)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.NewDecoder(bytes.NewReader(created.Body.Bytes())).Decode(&envelope))
	require.Equal(t, "excel", envelope.Data["type"])
}
