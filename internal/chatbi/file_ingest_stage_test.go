package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeManagedFileGateway struct {
	*fakeGateway
	calls   int
	deletes int
}

func (gateway *fakeManagedFileGateway) DeleteFile(_ context.Context, _ Principal, binding Binding, operationID string) error {
	if binding.BindingID != "22222222-2222-4222-8222-222222222222" ||
		operationID != "22222222222222222222222222222222" {
		return ErrGateway
	}
	gateway.deletes++
	return nil
}

func (*fakeManagedFileGateway) Schema(_ context.Context, _ Principal, binding Binding) ([]Column, error) {
	if binding.Type != "excel" || binding.BindingID != "22222222-2222-4222-8222-222222222222" ||
		binding.ResolveName != "agentsql-import-33333333333333333333333333333333" {
		return nil, ErrGateway
	}
	return []Column{{Schema: "agentsql_files", Table: "agentsql_file_11111111111111111111111111111111", Name: "region"},
		{Schema: "agentsql_files", Table: "agentsql_file_11111111111111111111111111111111", Name: "amount"}}, nil
}

func (gateway *fakeManagedFileGateway) ImportFile(_ context.Context, _ Principal, _ Binding,
	stage fileStage, _ FileImportRequest, plan fileImportPlan) (managedFileImportResult, error) {
	gateway.calls++
	if string(stage.Data) != "region,amount\neast,10\nwest,20\n" || len(plan.Sheets) != 1 || len(plan.Sheets[0].Rows) != 2 {
		return managedFileImportResult{}, ErrGateway
	}
	return managedFileImportResult{OperationID: "22222222222222222222222222222222",
		ChildBindingID: "22222222-2222-4222-8222-222222222222",
		ChildName:      "agentsql-import-33333333333333333333333333333333",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Sheets:         []importedFileSheet{{SheetName: "Sheet1", TableName: "agentsql_file_11111111111111111111111111111111", Rows: 2}}}, nil
}

func TestImportToDbUsesCurrentEncryptedStageAndManagedGateway(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	service.ManagedFileDatasource = "10"
	managed := &fakeManagedFileGateway{fakeGateway: fake}
	service.Gateway = managed
	stage := fileStage{FilePath: "agentsql-file-123.csv", Original: "source.csv",
		Data: []byte("region,amount\neast,10\nwest,20\n"), Expires: time.Now().UTC().Add(time.Hour)}
	sheets, err := parseFileImport(stage.Original, stage.Data)
	require.NoError(t, err)
	require.NoError(t, service.saveFileStage(ctx, principal, stage, sheets))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	requestBody := `{"filePath":"agentsql-file-123.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"region","fieldType":"string"},{"fieldName":"amount","fieldType":"int"}]}]}`
	call := func(body, authorization string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/importToDb", bytes.NewBufferString(body))
		if authorization != "" {
			request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+authorization)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := call(requestBody, token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"filename":"agentsql-file-123.csv"`)
	require.Contains(t, response.Body.String(), `"tableName":"agentsql_file_11111111111111111111111111111111"`)
	var receipt managedFileReceipt
	_, err = service.Store.get(ctx, managedFileReceiptKey(principal, stage.FilePath), &receipt)
	require.NoError(t, err)
	require.Equal(t, "22222222222222222222222222222222", receipt.OperationID)
	require.Equal(t, "22222222-2222-4222-8222-222222222222", receipt.BindingID)
	require.Len(t, receipt.Sheets, 1)
	require.Equal(t, 1, managed.calls)
	require.Equal(t, http.StatusUnauthorized, call(requestBody, "").Code)
	require.NotEqual(t, http.StatusOK, call(strings.Replace(requestBody, "agentsql-file-123.csv", "../other.csv", 1), token).Code)
	require.Equal(t, 1, managed.calls, "invalid stage must not reach the gateway")

	// This configuration was encrypted independently with the pinned SQLBot
	// browser's AES-128-ECB wire format. The original add route claims exactly
	// the tables returned by importToDb and returns a JS-safe numeric ID.
	const sqlbotConfiguration = "LQQP8jNf0ZLnRc/4JTH7Va9ch431JNH/bcAoanv6BU9bnR5Lu2ZH8ulnMQEbkSNUlLwClnUx0ZKW/5p9kqd8XFvbd+/N/FwyZezvTv+gCWJSJ8bdl8exAt419FK2bvb7lziEfUHaD7V+Cvdzt/pmGtw3POKxxsHWj9Qs+i0sPwzPJ0RkbGqy5C11Uk+CpvGWcYgK5sNEM9AMF7nNenIXNHo/61WFe/tGQgl2VSPQc+99TpOWN4QEGITip8wuh2Y0YmHvkR0g54+oP4C/boqHtZYZchNdh1i36+YCoSQoMN/tLVaBffgpkyed2V2fsex/6W9OOTcKfe7qxcP/VNeF8Q=="
	filename, decoded, err := decodeSQLBotExcelConfig(sqlbotConfiguration)
	require.NoError(t, err)
	require.Equal(t, stage.FilePath, filename)
	require.Equal(t, receipt.Sheets, decoded)
	addBody, err := json.Marshal(map[string]any{"name": "Sales CSV", "description": "synthetic",
		"type": "excel", "configuration": sqlbotConfiguration,
		"tables": []map[string]string{{"table_name": receipt.Sheets[0].TableName, "table_comment": ""}}})
	require.NoError(t, err)
	add := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/add", bytes.NewReader(addBody))
		req.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	created := add()
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var createdEnvelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &createdEnvelope))
	createdID := int64(createdEnvelope.Data["id"].(float64))
	require.Greater(t, createdID, int64(0))
	require.LessOrEqual(t, createdID, int64((1<<53)-1))
	require.ElementsMatch(t, []string{"type", "id", "configuration", "create_by", "num", "table_relation",
		"recommended_config", "description", "type_name", "name", "create_time", "status", "oid", "embedding"},
		func() []string {
			keys := make([]string, 0, len(createdEnvelope.Data))
			for key := range createdEnvelope.Data {
				keys = append(keys, key)
			}
			return keys
		}())
	require.NotEqual(t, http.StatusOK, add().Code, "the same import receipt is one-use")
	list := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/list", nil)
		req.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	require.Contains(t, list().Body.String(), `"type":"excel"`)
	get := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/get/"+strconv.FormatInt(createdID, 10), nil)
	get.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, get)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	require.Contains(t, got.Body.String(), `"type":"excel"`)
	remove := func(name string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/delete/"+
			strconv.FormatInt(createdID, 10)+"/"+name, nil)
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	require.NotEqual(t, http.StatusOK, remove("wrong-name").Code)
	require.Zero(t, managed.deletes)
	require.Equal(t, http.StatusOK, remove("Sales%20CSV").Code)
	require.Equal(t, 1, managed.deletes)
	require.NotEqual(t, http.StatusOK, remove("Sales%20CSV").Code)
	require.NotContains(t, list().Body.String(), `"name":"Sales CSV"`)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	actions := map[string]bool{}
	for _, raw := range audits {
		var item struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &item))
		actions[item.Action] = true
	}
	require.True(t, actions["file-import.published"])
	require.True(t, actions["file-import.claim"])
	require.True(t, actions["file-import.datasource-delete"])
}

func TestParseExcelStagesEncryptedOwnerScopedFileOnlyWithAudit(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	upload := func(filename string, data []byte) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
		require.NoError(t, form.Close())
		request := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/parseExcel", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	data := []byte("region,amount\neast,10\nwest,20\n")
	require.Equal(t, http.StatusUnauthorized, upload("sales.csv", data).Code, "ordinary user cannot stage a managed file")
	user, revision, err := service.Store.User(ctx, p.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	p, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	response := upload("sales.csv", data)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload struct {
		Data struct {
			FilePath string      `json:"filePath"`
			Sheets   []FileSheet `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Contains(t, payload.Data.FilePath, "agentsql-file-")
	require.Len(t, payload.Data.Sheets, 1)
	require.Equal(t, 2, payload.Data.Sheets[0].Count)
	var stage fileStage
	_, err = service.Store.get(ctx, fileStageKey(p), &stage)
	require.NoError(t, err)
	require.Equal(t, data, stage.Data)
	require.Equal(t, payload.Data.FilePath, stage.FilePath)
	var importRequest FileImportRequest
	require.NoError(t, json.Unmarshal([]byte(`{"filePath":"placeholder","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"region","fieldType":"string"},{"fieldName":"amount","fieldType":"int"}]}]}`), &importRequest))
	importRequest.FilePath = stage.FilePath
	plan, err := service.prepareFileImport(ctx, p, importRequest)
	require.NoError(t, err)
	require.Equal(t, []any{"east", int64(10)}, plan.Sheets[0].Rows[0])
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	foundAudit := false
	for _, raw := range audits {
		var item struct {
			Action string `json:"action"`
			Owner  string `json:"owner"`
			Rows   int    `json:"rows"`
			Hash   string `json:"file_sha256"`
		}
		require.NoError(t, json.Unmarshal(raw, &item))
		if item.Action == "file-stage.parse" {
			foundAudit = item.Owner == string(p.User.ID) && item.Rows == 2 && len(item.Hash) == 64
		}
	}
	require.True(t, foundAudit, "staged upload needs durable actor, row count and file digest")
	other := testUser(t, "2", "other-admin")
	other.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, other))
	otherPrincipal := Principal{User: other, Workspace: other.Workspace}
	_, err = service.Store.get(ctx, fileStageKey(otherPrincipal), &fileStage{})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.prepareFileImport(ctx, otherPrincipal, importRequest)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_file_stage_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	failed := upload("replacement.csv", []byte("region,amount\nnorth,30\n"))
	require.NotEqual(t, http.StatusOK, failed.Code)
	var retained fileStage
	_, err = service.Store.get(ctx, fileStageKey(p), &retained)
	require.NoError(t, err)
	require.Equal(t, stage.FilePath, retained.FilePath, "audit failure must roll back replacement")
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_file_stage_audit`)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, upload("invalid.xls", []byte("legacy")).Code)
	_, err = service.Store.get(ctx, fileStageKey(p), &retained)
	require.False(t, errors.Is(err, ErrNotFound))
	user, revision, err = service.Store.User(ctx, p.User.ID)
	require.NoError(t, err)
	user.Admin = false
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	_, err = service.prepareFileImport(ctx, p, importRequest)
	require.ErrorIs(t, err, ErrUnauthenticated)
}
