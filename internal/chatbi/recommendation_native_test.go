package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeRecommendationConfigurationIsAuditedAndRevisioned(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, userRevision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, userRevision); err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1/agentsql/recommendations/10", strings.NewReader(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if initial := call(http.MethodGet, ""); initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"revision":0`) {
		t.Fatalf("initial settings: %d %s", initial.Code, initial.Body.String())
	}
	created := call(http.MethodPut, `{"revision":0,"questions":[{"question":"Revenue by region"}]}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"revision":1`) {
		t.Fatalf("save recommendations: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data struct {
			Questions []RecommendedProblem `json:"questions"`
		} `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil || len(envelope.Data.Questions) != 1 || !envelope.Data.Questions[0].ID.Valid() {
		t.Fatal("server did not assign a valid suggestion ID")
	}
	if stale := call(http.MethodPut, `{"revision":0,"questions":[]}`); stale.Code != http.StatusConflict {
		t.Fatalf("stale update: %d", stale.Code)
	}
	if invalid := call(http.MethodPut, `{"revision":1,"questions":[{"id":"999","question":"forged"}]}`); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("foreign suggestion ID: %d", invalid.Code)
	}
	read := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/recommended_problem/get_datasource_recommended/10", nil)
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	router.ServeHTTP(read, request)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "Revenue by region") {
		t.Fatal("native configuration is missing from compatibility read")
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_recommendation_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	failed := call(http.MethodPut, `{"revision":1,"questions":[]}`)
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit fault was not closed: %d", failed.Code)
	}
	config, revision, err := service.RecommendationSettings(ctx, principal, "10")
	if err != nil || revision != 1 || len(config.Questions) != 1 {
		t.Fatal("audit failure changed the recommendation configuration")
	}
	user, userRevision, err = service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = false
	if err := service.Store.put(ctx, userKey(user.ID), user, userRevision); err != nil {
		t.Fatal(err)
	}
	if nonAdmin := call(http.MethodGet, ""); nonAdmin.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin read: %d", nonAdmin.Code)
	}
}
