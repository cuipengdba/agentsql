package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSemanticXLSXUploadReportsRowsAndAuditsWrites(t *testing.T) {
	service, p, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(route string, data []byte) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "synthetic.xlsx")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, route, &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	terms, err := semanticWorkbook([][]string{semanticHeaders("terminology", "en", true), {"GMV", "", "synthetic total", "fixture", "N", ""}, {"GMV", "", "duplicate", "fixture", "N", ""}, {"invalid", "", "", "", "Y", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/system/terminology/uploadExcel", terms); w.Code == http.StatusOK {
		t.Fatal("ordinary user uploaded terminology")
	}
	user, revision, err := service.Store.User(context.Background(), p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(context.Background(), userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	p, err = service.Identity.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	w := request("/api/v1/system/terminology/uploadExcel", terms)
	if w.Code != http.StatusOK {
		t.Fatalf("safe terminology upload failed: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data semanticUploadReport `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.SuccessCount != 1 || response.Data.DuplicateCount != 1 || response.Data.FailedCount != 1 || response.Data.OriginalCount != 3 {
		t.Fatalf("unexpected import report: %s", w.Body.String())
	}
	if response.Data.ErrorExcelFile == nil {
		t.Fatal("failed import row did not return an error workbook")
	}
	download := func(file string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"file": file})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/system/download-fail-info", bytes.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := download("../../other_error.xlsx"); w.Code == http.StatusOK {
		t.Fatal("path-like error report name was accepted")
	}
	workbook := download(*response.Data.ErrorExcelFile)
	if workbook.Code != http.StatusOK || !bytes.HasPrefix(workbook.Body.Bytes(), []byte("PK")) {
		t.Fatalf("error workbook download failed: %d", workbook.Code)
	}
	failed, err := readSemanticWorkbook(workbook.Body.Bytes(), 7)
	if err != nil || len(failed) != 1 || failed[0][0] != "invalid" || failed[0][6] == "" {
		t.Fatal("error workbook did not retain the invalid row as literal data")
	}
	other := testUser(t, "2", "other-admin")
	other.Admin = true
	if err := service.Store.CreateUser(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := service.Identity.Login(context.Background(), other.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	otherBody, err := json.Marshal(map[string]string{"file": *response.Data.ErrorExcelFile})
	if err != nil {
		t.Fatal(err)
	}
	otherRequest := httptest.NewRequest(http.MethodPost, "/api/v1/system/download-fail-info", bytes.NewReader(otherBody))
	otherRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+otherToken)
	otherResponse := httptest.NewRecorder()
	handler.ServeHTTP(otherResponse, otherRequest)
	if otherResponse.Code == http.StatusOK || bytes.HasPrefix(otherResponse.Body.Bytes(), []byte("PK")) {
		t.Fatal("other workspace admin downloaded another user's error workbook")
	}
	stored, err := service.Terminologies(context.Background(), p)
	if err != nil || len(stored) != 1 || len(stored[0].DatasourceNames) != 1 || stored[0].DatasourceNames[0] != "fixture" {
		t.Fatal("imported term or datasource name was not persisted")
	}
	examples, err := semanticWorkbook([][]string{semanticHeaders("sql-example", "en", true), {"Total sales?", "SELECT amount FROM sales", "fixture", ""}})
	if err != nil {
		t.Fatal(err)
	}
	w = request("/api/v1/system/data-training/uploadExcel", examples)
	if w.Code != http.StatusOK {
		t.Fatalf("safe SQL example upload failed: %d %s", w.Code, w.Body.String())
	}
	storedExamples, err := service.Examples(context.Background(), p)
	if err != nil || len(storedExamples) != 1 || storedExamples[0].Datasource != 10 {
		t.Fatal("SQL example was not scoped to the authorized datasource")
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 4 {
		t.Fatalf("successful imported rows lacked durable audits: %d %v", len(audits), err)
	}
	downloadAudited := false
	for _, raw := range audits {
		var item struct {
			Action string `json:"action"`
			File   string `json:"file"`
			User   ID     `json:"user"`
		}
		if json.Unmarshal(raw, &item) != nil {
			t.Fatal("invalid management audit")
		}
		if item.Action == "semantic-error.download" {
			downloadAudited = item.File == *response.Data.ErrorExcelFile && item.User == p.User.ID
		}
	}
	if !downloadAudited {
		t.Fatal("error workbook download audit lacks the user or file")
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_semantic_upload_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if w := download(*response.Data.ErrorExcelFile); w.Code == http.StatusOK || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatal("audit failure released an error workbook")
	}
	newTerm, err := semanticWorkbook([][]string{semanticHeaders("terminology", "en", true), {"Net revenue", "", "synthetic net", "", "Y", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/system/terminology/uploadExcel", newTerm); w.Code == http.StatusOK {
		t.Fatal("audit failure accepted imported term")
	}
	stored, err = service.Terminologies(context.Background(), p)
	if err != nil || len(stored) != 1 {
		t.Fatal("term persisted despite failed audit")
	}
}
