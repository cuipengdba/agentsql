package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

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
