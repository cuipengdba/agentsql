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
	if err != nil || len(audits) != 2 {
		t.Fatalf("successful imported rows lacked durable audits: %d %v", len(audits), err)
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_semantic_upload_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
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
