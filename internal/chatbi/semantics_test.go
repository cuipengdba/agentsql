package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSemanticManagementMatchesPinnedListShapeAndIsAudited(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, token := serviceFixture(t)
	if _, err := service.SaveTerminology(ctx, p, Terminology{Word: "revenue"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("ordinary user wrote terminology")
	}
	user, rev, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, rev); err != nil {
		t.Fatal(err)
	}
	p, err = service.Identity.Verify(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any) (int, map[string]json.RawMessage) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var response map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		return w.Code, response
	}
	code, saved := request(http.MethodPut, "/api/v1/system/terminology", map[string]any{"word": "Synthetic revenue", "description": "Synthetic definition"})
	if code != 200 {
		t.Fatalf("terminology save status %d", code)
	}
	var termID int64
	if json.Unmarshal(saved["data"], &termID) != nil || termID <= 0 {
		t.Fatal("terminology save did not return a numeric ID")
	}
	code, page := request(http.MethodGet, "/api/v1/system/terminology/page/1/10", nil)
	if code != 200 {
		t.Fatalf("terminology list status %d", code)
	}
	var body struct {
		Data struct {
			CurrentPage int           `json:"current_page"`
			PageSize    int           `json:"page_size"`
			TotalCount  int           `json:"total_count"`
			TotalPages  int           `json:"total_pages"`
			Rows        []Terminology `json:"data"`
		} `json:"data"`
	}
	data, _ := json.Marshal(page)
	if json.Unmarshal(data, &body) != nil || body.Data.CurrentPage != 1 || body.Data.PageSize != 10 || body.Data.TotalCount != 1 || body.Data.TotalPages != 1 || len(body.Data.Rows) != 1 || body.Data.Rows[0].ID != termID {
		t.Fatal("terminology page differs from pinned SQLBot shape")
	}
	if code, _ = request(http.MethodGet, "/api/v1/system/terminology/"+stringID(termID)+"/enable/false", nil); code != 200 {
		t.Fatal("disable terminology failed")
	}
	terms, err := service.Terminologies(ctx, p)
	if err != nil || terms[0].Enabled {
		t.Fatal("disable state was not persisted")
	}
	code, saved = request(http.MethodPut, "/api/v1/system/data-training", map[string]any{"oid": 1, "datasource": 10, "question": "Synthetic revenue?", "description": "SELECT 1 AS synthetic_total"})
	if code != 200 {
		t.Fatalf("example save status %d", code)
	}
	var exampleID int64
	if json.Unmarshal(saved["data"], &exampleID) != nil || exampleID <= 0 {
		t.Fatal("example save did not return a numeric ID")
	}
	code, page = request(http.MethodGet, "/api/v1/system/data-training/page/1/10", nil)
	if code != 200 || !strings.Contains(string(page["data"]), "total_count") {
		t.Fatal("SQL example page was unavailable")
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("expected 3 durable metadata audits, got %d: %v", len(audits), err)
	}
	if code, _ = request(http.MethodDelete, "/api/v1/system/terminology", []int64{termID}); code != 200 {
		t.Fatal("terminology deletion failed")
	}
	if code, _ = request(http.MethodDelete, "/api/v1/system/data-training", []int64{exampleID}); code != 200 {
		t.Fatal("SQL example deletion failed")
	}
	if terms, _ = service.Terminologies(ctx, p); len(terms) != 0 {
		t.Fatal("deleted term remained")
	}
}

func TestSemanticAuditFailureRollsBackAndBindingRevocationCloses(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, _ := serviceFixture(t)
	user, rev, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, rev); err != nil {
		t.Fatal(err)
	}
	p.User = user
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_semantic_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveTerminology(ctx, p, Terminology{Word: "revenue", SpecificDS: true, DatasourceIDs: []int64{10}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit failure did not fail closed: %v", err)
	}
	terms, err := service.Terminologies(ctx, p)
	if err != nil || len(terms) != 0 {
		t.Fatal("term persisted without audit")
	}
	if _, err := service.SaveExample(ctx, p, SQLExample{Workspace: 1, Datasource: 10, Question: "total?", Description: "SELECT 1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("example persisted without audit")
	}
	if err := service.Store.delete(ctx, bindingKey(p, "10"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveTerminology(ctx, p, Terminology{Word: "revenue", SpecificDS: true, DatasourceIDs: []int64{10}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked datasource accepted term")
	}
	if _, err := service.SaveExample(ctx, p, SQLExample{Workspace: 1, Datasource: 10, Question: "total?", Description: "SELECT 1"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked datasource accepted example")
	}
}

func TestSemanticBatchDeletionIsAtomic(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, _ := serviceFixture(t)
	user, rev, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, rev); err != nil {
		t.Fatal(err)
	}
	p.User = user
	a, err := service.SaveTerminology(ctx, p, Terminology{Word: "synthetic A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.SaveTerminology(ctx, p, Terminology{Word: "synthetic B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSemanticBatch(ctx, p, "terminology", []int64{a, 999}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing batch item did not fail")
	}
	terms, err := service.Terminologies(ctx, p)
	if err != nil || len(terms) != 2 {
		t.Fatal("partial deletion escaped a failed batch")
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_semantic_delete_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSemanticBatch(ctx, p, "terminology", []int64{a, b}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("audit fault did not roll back batch")
	}
	terms, err = service.Terminologies(ctx, p)
	if err != nil || len(terms) != 2 {
		t.Fatal("audit failure partially deleted records")
	}
}

func stringID(id int64) string { return strconv.FormatInt(id, 10) }
