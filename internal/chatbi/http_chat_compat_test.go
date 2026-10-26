package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSQLBotRecommendationStreamAndUsageStayOwned(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	config := RecommendationConfig{Workspace: "1", Datasource: "10", Mode: 2,
		Questions: []RecommendedProblem{
			{ID: "31", Datasource: "10", Question: "Show amount", Sort: 1},
			{ID: "32", Datasource: "10", Question: "Show sales", Sort: 2},
		}}
	if err := service.Store.put(ctx, recommendationKey("1", "10"), config, 0); err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, principal, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Ask(ctx, principal, chat.ID, "amount", func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "v4.2.0")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/chat/recommend_questions/" + string(chat.ID) + "?articles_number=1"
	if got := request(http.MethodPost, path, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous recommendation returned %d", got.Code)
	}
	first := request(http.MethodPost, path, token)
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(first.Body.String(), `Show amount`) || strings.Contains(first.Body.String(), `Show sales`) {
		t.Fatalf("bounded first-chat stream: %d %s", first.Code, first.Body.String())
	}
	followup := request(http.MethodPost, "/api/v1/chat/recommend_questions/"+string(record.ID), token)
	if followup.Code != http.StatusOK || !strings.Contains(followup.Body.String(), `Show sales`) {
		t.Fatalf("record recommendation stream: %d %s", followup.Code, followup.Body.String())
	}
	usage := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/usage", token)
	if usage.Code != http.StatusOK || strings.Contains(usage.Body.String(), "123") {
		t.Fatalf("usage exposed result data: %d %s", usage.Code, usage.Body.String())
	}
	var body struct {
		Data struct {
			TotalTokens int   `json:"total_tokens"`
			Steps       []any `json:"steps"`
		} `json:"data"`
	}
	if json.Unmarshal(usage.Body.Bytes(), &body) != nil || body.Data.TotalTokens != 12 || len(body.Data.Steps) != 0 {
		t.Fatalf("usage summary mismatch: %s", usage.Body.String())
	}
	if got := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/usage", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous usage returned %d", got.Code)
	}
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke datasource binding")
	}
	if got := request(http.MethodPost, path, token); got.Code != http.StatusNotFound {
		t.Fatalf("revoked recommendation returned %d", got.Code)
	}
}
