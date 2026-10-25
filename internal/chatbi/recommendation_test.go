package chatbi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecommendationsRequireCurrentBindingAndAtomicConfiguration(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	config := RecommendationConfig{Workspace: "1", Datasource: "10", Mode: 2, Questions: []RecommendedProblem{
		{ID: "2", Datasource: "10", Question: "second question", Sort: 2},
		{ID: "1", Datasource: "10", Question: "first question", Sort: 1},
	}}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{Recommendations: []RecommendationConfig{config}}); err != nil {
		t.Fatal(err)
	}
	got, err := service.Recommendations(ctx, principal, "10")
	if err != nil || len(got.Questions) != 2 || got.Questions[0].ID != "1" {
		t.Fatalf("authorized recommendations not sorted: %v", err)
	}
	router, err := NewHandler(service, "v4.2.0")
	if err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]string{
		"/api/v1/recommended_problem/get_datasource_recommended/10":      "first question",
		"/api/v1/recommended_problem/get_datasource_recommended_base/10": "recommended_config",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("authorized route failed: %s: %d", path, response.Code)
		}
	}
	config.Mode = 1 // Unsupported automatic generation must not silently downgrade.
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{Recommendations: []RecommendationConfig{config}}); err == nil {
		t.Fatal("unsupported recommendation mode accepted")
	}
	stored, err := service.Recommendations(ctx, principal, "10")
	if err != nil || stored.Mode != 2 {
		t.Fatal("invalid configuration changed stored recommendations")
	}
	config.Mode = 2
	config.Questions[0].Question = "replacement"
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_recommendation_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{Recommendations: []RecommendationConfig{config}}); err == nil {
		t.Fatal("recommendations changed without durable configuration audit")
	}
	stored, err = service.Recommendations(ctx, principal, "10")
	if err != nil || stored.Questions[1].Question != "second question" {
		t.Fatal("audit fault changed stored recommendations")
	}
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke personal binding")
	}
	if _, err := service.Recommendations(ctx, principal, "10"); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked user read recommendations")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/recommended_problem/get_datasource_recommended/10", nil)
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatal("revoked recommendation route did not close")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/recommended_problem/get_datasource_recommended/10", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated recommendation route was open")
	}
}
